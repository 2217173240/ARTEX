package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/mcphttp"
)

// All requests go to this local MCP fixture, never to a configured provider.
func ssTestMCP(t *testing.T, call func(string, map[string]any) (string, error)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}
		case "tools/call":
			value, err := call(req.Params.Name, req.Params.Arguments)
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": value}}, "isError": err != nil}
			if err != nil {
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true}
			}
		}
		if req.ID == 0 {
			w.WriteHeader(202)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func ssTestClient(t *testing.T, call func(string, map[string]any) (string, error)) *mcphttp.Client {
	t.Helper()
	cl, err := mcphttp.New(context.Background(), "fixture", ssTestMCP(t, call), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

func TestSSPageAllCapEvidence(t *testing.T) {
	for _, tc := range []struct {
		name               string
		count, size, calls int
		truncated          bool
	}{
		{"below", 4999, 100, 50, false},
		{"exact full page", 5000, 100, 51, false},
		{"exact short page", 5000, 300, 17, false},
		{"above full page", 5001, 100, 51, true},
		{"above crossing page", 5001, 300, 17, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			filter := map[string]any{"project": []string{"synthetic-project"}}
			cl := ssTestClient(t, func(tool string, args map[string]any) (string, error) {
				calls++
				if tool != "list_assets" || args["asset_type"] != "subdomain" || args["pageSize"] != float64(tc.size) {
					t.Errorf("unexpected call %s %v", tool, args)
				}
				if !reflect.DeepEqual(args["filter"], map[string]any{"project": []any{"synthetic-project"}}) {
					t.Errorf("filter changed: %v", args)
				}
				page := int(args["pageIndex"].(float64))
				start, end := (page-1)*tc.size, page*tc.size
				if end > tc.count {
					end = tc.count
				}
				list := []json.RawMessage{}
				for i := start; i < end; i++ {
					list = append(list, json.RawMessage(fmt.Sprintf(`{"host":"host-%d.example"}`, i)))
				}
				b, _ := json.Marshal(map[string]any{"list": list})
				return string(b), nil
			})
			items, truncated, err := (&Server{}).ssPageAll(context.Background(), cl, "subdomain", filter, tc.size)
			wantCount := tc.count
			if wantCount > syncMaxPerType {
				wantCount = syncMaxPerType
			}
			if err != nil || len(items) != wantCount || truncated != tc.truncated || calls != tc.calls {
				t.Fatalf("items=%d truncated=%v err=%v calls=%d; want %d %v %d", len(items), truncated, err, calls, wantCount, tc.truncated, tc.calls)
			}
		})
	}
}

func TestSSPageAllRetainsValidatedPages(t *testing.T) {
	cl := ssTestClient(t, func(_ string, args map[string]any) (string, error) {
		if args["pageIndex"] == float64(1) {
			return `{"list":[{"host":"one.example"},{"host":"two.example"}]}`, nil
		}
		return "", fmt.Errorf("later page unavailable")
	})
	items, truncated, err := (&Server{}).ssPageAll(context.Background(), cl, "subdomain", nil, 2)
	if len(items) != 2 || truncated || err == nil || !strings.Contains(err.Error(), "later page unavailable") {
		t.Fatalf("items=%v truncated=%v err=%v", items, truncated, err)
	}
}

func TestSSIngestReportsMissingRequiredFields(t *testing.T) {
	for _, tc := range []struct{ kind, raw string }{{"subdomain", `{}`}, {"app", `{}`}, {"service", `{"service":"http"}`}} {
		t.Run(tc.kind, func(t *testing.T) {
			if got := (&Server{}).ssIngest(context.Background(), nil, tc.kind, json.RawMessage(tc.raw), map[string]int{}); len(got) == 0 {
				t.Fatal("invalid record was silently skipped")
			}
		})
	}
}

// Fake writers let failure/cancellation happen at an exact operation boundary.
type ssTestAssets struct {
	writes []string
	fail   string
	after  func(string)
}

func (a *ssTestAssets) write(kind string) (int64, error) {
	a.writes = append(a.writes, kind)
	if a.after != nil {
		a.after(kind)
	}
	if a.fail == kind {
		return 0, errors.New("fixture write failure")
	}
	return 1, nil
}
func (a *ssTestAssets) UpsertSubdomain(db.UpsertSubdomainReq) (int64, error) {
	return a.write("subdomain")
}
func (a *ssTestAssets) UpsertIP(db.UpsertIPReq) (int64, error)   { return a.write("ip") }
func (a *ssTestAssets) UpsertApp(db.UpsertAppReq) (int64, error) { return a.write("app") }
func (a *ssTestAssets) UpsertHTTPService(db.UpsertHTTPServiceReq) (int64, error) {
	return a.write("http")
}
func (a *ssTestAssets) UpsertOtherService(db.UpsertOtherServiceReq) (int64, error) {
	return a.write("other")
}

type ssTestCompanies struct {
	writes                    []string
	scopeErrs                 []string
	invalid                   int
	upsertErr, attributionErr error
	after                     func(string)
}

func (c *ssTestCompanies) write(kind string) {
	c.writes = append(c.writes, kind)
	if c.after != nil {
		c.after(kind)
	}
}
func (c *ssTestCompanies) UpsertByName(string) (int64, error) {
	c.write("company")
	return 1, c.upsertErr
}
func (c *ssTestCompanies) AddScope(int64, []string, string) (int, int, int, []string) {
	c.write("scope")
	return 0, 0, c.invalid, c.scopeErrs
}
func (c *ssTestCompanies) RecomputeAttribution() error {
	c.write("attribution")
	return c.attributionErr
}

type ssTestCaller func(context.Context, string, any) (string, error)

func (f ssTestCaller) Call(ctx context.Context, tool string, args any) (string, error) {
	return f(ctx, tool, args)
}
func ssTestReq() ssSyncReq {
	return ssSyncReq{Dimension: "task", Targets: []string{"fixture-task"}, AssetTypes: []string{"subdomain"}}
}

func TestSSSyncOutcomesAndSuccessfulWriteCounts(t *testing.T) {
	for _, tc := range []struct {
		name, list, fail, status string
		subdomains, ips, errs    int
	}{
		{"empty", `[]`, "", "success", 0, 0, 0},
		{"valid", `[{"host":"one.example","ip":["192.0.2.1"]}]`, "", "success", 1, 1, 0},
		{"duplicates count upserts", `[{"host":"one.example"},{"host":"one.example"}]`, "", "success", 2, 0, 0},
		{"missing host", `[{}]`, "", "failed", 0, 0, 1},
		{"malformed", `["invalid-record"]`, "", "failed", 0, 0, 1},
		{"IP failed after parent", `[{"host":"one.example","ip":["192.0.2.1","192.0.2.2"]}]`, "ip", "partial", 1, 0, 2},
		{"empty derived IP", `[{"host":"one.example","ip":[""]}]`, "", "partial", 1, 0, 1},
		{"parent write failed", `[{"host":"one.example","ip":["192.0.2.1"]}]`, "subdomain", "failed", 0, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &ssTestAssets{fail: tc.fail}
			c := &ssTestCompanies{}
			cl := ssTestClient(t, func(string, map[string]any) (string, error) { return `{"list":` + tc.list + `}`, nil })
			result := (&Server{}).ssSync(context.Background(), cl, a, c, ssTestReq(), 100)
			if result.Status != tc.status || result.Synced["subdomain"] != tc.subdomains || result.Synced["ip"] != tc.ips || len(result.Errors) != tc.errs || result.Truncated {
				t.Fatalf("result=%+v", result)
			}
			if tc.fail == "subdomain" && len(a.writes) != 1 {
				t.Fatalf("derived writes after parent failure: %v", a.writes)
			}
		})
	}
}

func TestSSSyncRetainsWritesAfterLaterPageFailure(t *testing.T) {
	cl := ssTestClient(t, func(_ string, args map[string]any) (string, error) {
		if args["pageIndex"] == float64(1) {
			return `{"list":[{"host":"one.example"},{"host":"two.example"}]}`, nil
		}
		return "", errors.New("fixture later-page failure")
	})
	result := (&Server{}).ssSync(context.Background(), cl, &ssTestAssets{}, &ssTestCompanies{}, ssTestReq(), 2)
	if result.Status != "partial" || result.Synced["subdomain"] != 2 || len(result.Errors) != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestSSSyncCompanyAndAttributionFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		company *ssTestCompanies
		warning bool
	}{
		{"company write", &ssTestCompanies{upsertErr: errors.New("fixture company failure")}, false},
		{"scope write", &ssTestCompanies{scopeErrs: []string{"fixture scope failure"}}, false},
		{"invalid scope", &ssTestCompanies{invalid: 1}, true},
		{"attribution", &ssTestCompanies{attributionErr: errors.New("fixture attribution failure")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cl := ssTestClient(t, func(tool string, _ map[string]any) (string, error) {
				if tool == "get_project" {
					return `{"name":"Fixture Company","target":"example.test"}`, nil
				}
				return `{"list":[{"name":"Fixture App"}]}`, nil
			})
			req := ssSyncReq{Dimension: "project", Targets: []string{"fixture-project"}, AssetTypes: []string{"app"}, CreateCompany: true}
			result := (&Server{}).ssSync(context.Background(), cl, &ssTestAssets{}, tc.company, req, 100)
			if result.Status != "partial" || result.Synced["app"] != 1 || (len(result.Warnings) > 0) != tc.warning || (!tc.warning && len(result.Errors) != 1) {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestSSSyncWarningAndTruncationStatuses(t *testing.T) {
	t.Run("unknown type", func(t *testing.T) {
		req := ssTestReq()
		req.AssetTypes = []string{"unknown"}
		result := (&Server{}).ssSync(context.Background(), ssTestCaller(func(context.Context, string, any) (string, error) { t.Fatal("unexpected call"); return "", nil }), &ssTestAssets{}, &ssTestCompanies{}, req, 100)
		if result.Status != "partial" || len(result.Warnings) != 1 {
			t.Fatalf("result=%+v", result)
		}
	})
	t.Run("cap plus one", func(t *testing.T) {
		cl := ssTestCaller(func(_ context.Context, _ string, args any) (string, error) {
			page := args.(map[string]any)["pageIndex"].(int)
			n := 500
			if page == 11 {
				n = 1
			}
			list := make([]json.RawMessage, n)
			for i := range list {
				list[i] = json.RawMessage(`{"name":"Fixture App"}`)
			}
			b, _ := json.Marshal(map[string]any{"list": list})
			return string(b), nil
		})
		req := ssTestReq()
		req.AssetTypes = []string{"app"}
		result := (&Server{}).ssSync(context.Background(), cl, &ssTestAssets{}, &ssTestCompanies{}, req, 500)
		if result.Status != "partial" || !result.Truncated || result.Synced["app"] != syncMaxPerType || len(result.Warnings) != 1 {
			t.Fatalf("result=%+v", result)
		}
	})
}

func TestSSSyncCancellationStopsFurtherOperations(t *testing.T) {
	for _, boundary := range []string{"before start", "page fetch", "parent write", "company write", "scope write"} {
		t.Run(boundary, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a, c := &ssTestAssets{}, &ssTestCompanies{}
			calls := []string{}
			cl := ssTestCaller(func(_ context.Context, tool string, args any) (string, error) {
				calls = append(calls, tool)
				if tool == "get_project" {
					return `{"name":"Fixture Company","target":"example.test"}`, nil
				}
				if boundary == "page fetch" {
					cancel()
				}
				return `{"list":[{"host":"one.example","ip":["192.0.2.1"]},{"host":"two.example"}]}`, nil
			})
			req := ssTestReq()
			req.Targets = append(req.Targets, "next-target")
			req.AssetTypes = append(req.AssetTypes, "app")
			if boundary == "before start" {
				cancel()
			}
			if boundary == "parent write" {
				a.after = func(string) { cancel() }
			}
			if boundary == "company write" || boundary == "scope write" {
				req.Dimension = "project"
				req.CreateCompany = true
				c.after = func(op string) {
					if (boundary == "company write" && op == "company") || (boundary == "scope write" && op == "scope") {
						cancel()
					}
				}
			}
			result := (&Server{}).ssSync(ctx, cl, a, c, req, 100)
			wantStatus := "failed"
			if boundary == "parent write" {
				wantStatus = "partial"
			}
			if result.Status != wantStatus || len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "context canceled") {
				t.Fatalf("result=%+v", result)
			}
			switch boundary {
			case "before start":
				if len(calls)+len(a.writes)+len(c.writes) != 0 {
					t.Fatalf("operations %v %v %v", calls, a.writes, c.writes)
				}
			case "page fetch":
				if len(calls) != 1 || len(a.writes) != 0 {
					t.Fatalf("operations %v %v", calls, a.writes)
				}
			case "parent write":
				if len(calls) != 1 || !reflect.DeepEqual(a.writes, []string{"subdomain"}) {
					t.Fatalf("operations %v %v", calls, a.writes)
				}
			case "company write":
				if !reflect.DeepEqual(calls, []string{"get_project"}) || !reflect.DeepEqual(c.writes, []string{"company"}) {
					t.Fatalf("operations %v %v", calls, c.writes)
				}
			case "scope write":
				if !reflect.DeepEqual(calls, []string{"get_project"}) || !reflect.DeepEqual(c.writes, []string{"company", "scope"}) {
					t.Fatalf("operations %v %v", calls, c.writes)
				}
			}
		})
	}
}

func TestSSSyncDeadlineStopsBeforeStart(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	result := (&Server{}).ssSync(ctx, ssTestCaller(func(context.Context, string, any) (string, error) {
		t.Fatal("unexpected provider call")
		return "", nil
	}), &ssTestAssets{}, &ssTestCompanies{}, ssTestReq(), 100)
	if result.Status != "failed" || len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "deadline exceeded") {
		t.Fatalf("result=%+v", result)
	}
}

// This route test requires an explicitly provided, fresh fixture database.
func TestSSSyncRouteIsolatedDatabase(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	host := fmt.Sprintf("sync-route-%d.example.test", time.Now().UnixNano())
	defer func() { _, _ = m.pg.Exec(`DELETE FROM assets WHERE type='subdomain' AND domain=$1`, host) }()
	sourceURL := ssTestMCP(t, func(string, map[string]any) (string, error) {
		return fmt.Sprintf(`{"list":[{"host":%q,"ip":["not-an-ip"]}]}`, host), nil
	})
	s := &Server{m: m}
	row, err := s.findMCPByName(scopeSentryMCPName)
	if err != nil {
		t.Fatal(err)
	}
	if row == nil {
		t.Fatal("missing seeded ScopeSentry fixture row")
	}
	original := *row
	defer func() { _, _ = m.pg.SaveMCP(&original) }()
	row.URL, row.Enabled, row.Env = sourceURL, true, json.RawMessage(`{}`)
	if _, err := m.pg.SaveMCP(row); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.syncSSRun(w, httptest.NewRequest("POST", "/api/sync/scopesentry/sync", strings.NewReader(`{"dimension":"task","targets":["fixture-task"],"asset_types":["subdomain"]}`)))
	var result ssSyncResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || result.Status != "partial" || result.Synced["subdomain"] != 1 || result.Synced["ip"] != 0 || len(result.Errors) != 1 {
		t.Fatalf("HTTP %d result=%+v", w.Code, result)
	}
	var count int
	if err := m.pg.QueryRow(`SELECT count(*) FROM assets WHERE type='subdomain' AND domain=$1`, host).Scan(&count); err != nil || count != 1 {
		t.Fatalf("persisted parent count=%d err=%v", count, err)
	}
}

func TestSSPageAllMalformedLaterPage(t *testing.T) {
	for _, bad := range []string{`{}`, `{"list":null}`, `{"list":{}}`, `{"list":`} {
		t.Run(bad, func(t *testing.T) {
			cl := ssTestClient(t, func(_ string, args map[string]any) (string, error) {
				if args["pageIndex"] == float64(1) {
					return `{"list":[{"host":"one.example"}]}`, nil
				}
				return bad, nil
			})
			items, truncated, err := (&Server{}).ssPageAll(context.Background(), cl, "subdomain", nil, 1)
			if len(items) != 1 || truncated || err == nil {
				t.Fatalf("items=%v truncated=%v err=%v", items, truncated, err)
			}
		})
	}
}

func TestSSPageAllCancellationStopsBeforeNextPage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	cl := ssTestCaller(func(context.Context, string, any) (string, error) {
		calls++
		cancel()
		return `{"list":[{"host":"one.example"}]}`, nil
	})
	items, truncated, err := (&Server{}).ssPageAll(ctx, cl, "subdomain", nil, 1)
	if calls != 1 || len(items) != 1 || truncated || !errors.Is(err, context.Canceled) {
		t.Fatalf("calls=%d items=%v truncated=%v err=%v", calls, items, truncated, err)
	}
}

func TestSSSyncProviderFailureBeforeAnyWrites(t *testing.T) {
	cl := ssTestClient(t, func(string, map[string]any) (string, error) { return "", errors.New("fixture provider failure") })
	assets := &ssTestAssets{}
	result := (&Server{}).ssSync(context.Background(), cl, assets, &ssTestCompanies{}, ssTestReq(), 100)
	if result.Status != "failed" || len(result.Errors) != 1 || len(assets.writes) != 0 {
		t.Fatalf("result=%+v writes=%v", result, assets.writes)
	}
}

func TestSSPageAllExactCapProbeFailure(t *testing.T) {
	calls := 0
	cl := ssTestCaller(func(_ context.Context, _ string, args any) (string, error) {
		calls++
		if calls == 11 {
			return "", errors.New("fixture probe failure")
		}
		list := make([]json.RawMessage, 500)
		for i := range list {
			list[i] = json.RawMessage(`{"host":"fixture.example"}`)
		}
		b, _ := json.Marshal(map[string]any{"list": list})
		return string(b), nil
	})
	items, truncated, err := (&Server{}).ssPageAll(context.Background(), cl, "subdomain", map[string]any{"task": []string{"fixture-task"}}, 500)
	if len(items) != syncMaxPerType || truncated || err == nil || calls != 11 {
		t.Fatalf("items=%d truncated=%v err=%v calls=%d", len(items), truncated, err, calls)
	}
}

func TestSSSyncProjectMetadataWarnings(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			companies := &ssTestCompanies{}
			cl := ssTestCaller(func(_ context.Context, tool string, _ any) (string, error) {
				if tool == "get_project" {
					if missing {
						return `{"target":"example.test"}`, nil
					}
					return "", errors.New("fixture metadata failure")
				}
				return `{"list":[]}`, nil
			})
			req := ssTestReq()
			req.Dimension = "project"
			req.CreateCompany = true
			result := (&Server{}).ssSync(context.Background(), cl, &ssTestAssets{}, companies, req, 100)
			if result.Status != "partial" || len(result.Warnings) != 1 || len(companies.writes) != 0 {
				t.Fatalf("result=%+v company writes=%v", result, companies.writes)
			}
		})
	}
}
