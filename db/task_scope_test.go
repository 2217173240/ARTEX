package db

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestStripHostPort covers the ip:port / [ipv6]:port stripping used by the
// ip/cidr scope path so a target like "10.0.188.136:3000" no longer 404s.
func TestStripHostPort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"10.0.188.136:3000", "10.0.188.136"},       // the reported IP case
		{"10.0.188.136", "10.0.188.136"},            // bare IPv4 unchanged
		{"[2001:db8::1]:8080", "2001:db8::1"},       // bracketed IPv6 + port
		{"2001:db8::1", "2001:db8::1"},              // bare IPv6 unchanged (has colons)
		{" 1.2.3.4:80 ", "1.2.3.4"},                 // trims surrounding space
		{"example.com:443", "example.com"},          // domain + port → bare host
		{"api.example.com:8080", "api.example.com"}, // subdomain + port
		{"example.com", "example.com"},              // bare domain unchanged
	}
	for _, c := range cases {
		if got := stripHostPort(c.in); got != c.want {
			t.Errorf("stripHostPort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAddAgentScopeCIDRFloor(t *testing.T) {
	t.Run("rejects overbroad CIDRs", func(t *testing.T) {
		store := &AssetStore{}
		rejected := []string{
			"0.0.0.0/0",
			"128.0.0.0/1",
			"192.0.0.0/2",
			"192.0.0.0/3",
			"192.0.0.0/4",
			"192.0.0.0/5",
			"192.0.0.0/6",
			"10.0.0.0/7",
			"::/0",
			"2000::/3",
			"2001:db8::/31",
		}
		for _, cidr := range rejected {
			if _, err := store.AddAgentScope(1, "cidr", cidr, "test", "agent"); err == nil {
				t.Errorf("AddAgentScope(%q) succeeded, want rejection", cidr)
			} else if !strings.Contains(err.Error(), "网段过宽") {
				t.Errorf("AddAgentScope(%q) error = %v, want overbroad CIDR message", cidr, err)
			}
		}
	})

	t.Run("allows valid lab and standard CIDRs", func(t *testing.T) {
		d, assets, _ := testSetup(t)
		t.Cleanup(func() { _ = d.Close() })

		task, err := d.CreateTask("cidr floor valid", "goal", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.DeleteTask(task.ID) })

		valid := []struct {
			kind  string
			value string
			want  string
		}{
			{"cidr", "10.0.0.0/8", "cidr"},
			{"cidr", "192.168.1.0/24", "cidr"},
			{"cidr", "2001:db8::/32", "cidr"},
			{"ip", "10.0.0.5", "ip"},
		}
		for _, tc := range valid {
			ts, err := assets.AddAgentScope(task.ID, tc.kind, tc.value, "test", "agent")
			if err != nil {
				t.Fatalf("AddAgentScope(%q, %q) failed: %v", tc.kind, tc.value, err)
			}
			if ts.Kind != tc.want {
				t.Fatalf("AddAgentScope(%q, %q).Kind = %q, want %q", tc.kind, tc.value, ts.Kind, tc.want)
			}
		}
	})
}

func TestTaskScopeIPLiteralDomainConsistency(t *testing.T) {
	d, assets, _ := testSetup(t)
	t.Cleanup(func() { _ = d.Close() })

	task, err := d.CreateTask("ip-literal domain scope consistency", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })

	if _, err := assets.AddAgentScope(task.ID, "cidr", "198.18.237.0/24", "test", "agent"); err != nil {
		t.Fatalf("AddAgentScope cidr: %v", err)
	}

	stamp := time.Now().UnixNano()
	ipLiteral := "198.18.237.42"
	marker := fmt.Sprintf("ipliteral%d", stamp)
	rawURL := fmt.Sprintf("http://%s:8080/%s", ipLiteral, marker)

	var assetID int64
	if err := d.QueryRow(`INSERT INTO assets(type,domain,root_domain,url,method,ip,task_ids,last_seen)
		VALUES ('endpoint',$1,$1,$2,'GET',NULL,ARRAY[$3]::bigint[],now())
		RETURNING id`, ipLiteral, rawURL, stamp+999).Scan(&assetID); err != nil {
		t.Fatalf("insert ip-literal endpoint: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM assets WHERE id=$1`, assetID) })

	// 1. scopeTargetCTE (list_assets)
	listed, err := assets.QueryDSLInScope(marker, "", task.ID, 50, 0)
	if err != nil {
		t.Fatalf("QueryDSLInScope: %v", err)
	}
	var inList bool
	for _, a := range listed {
		if a.ID == assetID {
			inList = true
			break
		}
	}
	if !inList {
		t.Fatalf("asset %d with IP-literal domain %q missing from scopeTargetCTE QueryDSLInScope", assetID, ipLiteral)
	}

	// 2. covTargetCTE (TaskCoverage + ListUntestedAssets)
	cov, err := assets.TaskCoverage(task.ID, task.ExplorationID)
	if err != nil {
		t.Fatalf("TaskCoverage: %v", err)
	}
	if cov.Denominator < 1 {
		t.Fatalf("TaskCoverage Denominator = %d, want >= 1", cov.Denominator)
	}
	untested, _, err := assets.ListUntestedAssets(task.ID, task.ExplorationID, "", 100, 0)
	if err != nil {
		t.Fatalf("ListUntestedAssets: %v", err)
	}
	var inCovUntested bool
	for _, a := range untested {
		if a.ID == assetID {
			inCovUntested = true
			break
		}
	}
	if !inCovUntested {
		t.Fatalf("asset %d missing from covTargetCTE ListUntestedAssets", assetID)
	}

	// 3. contextCoverageCTE (TaskCoverageWithSources + ListUntestedAssetsWithSources)
	covSrc, err := assets.TaskCoverageWithSources(task.ID)
	if err != nil {
		t.Fatalf("TaskCoverageWithSources: %v", err)
	}
	if covSrc.Denominator < 1 {
		t.Fatalf("TaskCoverageWithSources Denominator = %d, want >= 1", covSrc.Denominator)
	}
	untestedSrc, _, err := assets.ListUntestedAssetsWithSources(task.ID, "", 100, 0)
	if err != nil {
		t.Fatalf("ListUntestedAssetsWithSources: %v", err)
	}
	var inCtxUntested bool
	for _, a := range untestedSrc {
		if a.ID == assetID {
			inCtxUntested = true
			break
		}
	}
	if !inCtxUntested {
		t.Fatalf("asset %d missing from contextCoverageCTE ListUntestedAssetsWithSources", assetID)
	}

	// 4. HostsByTaskWithSources
	hosts, err := assets.HostsByTaskWithSources(task.ID)
	if err != nil {
		t.Fatalf("HostsByTaskWithSources: %v", err)
	}
	if !slices.Contains(hosts, ipLiteral) {
		t.Fatalf("HostsByTaskWithSources = %v, want %q", hosts, ipLiteral)
	}
}
