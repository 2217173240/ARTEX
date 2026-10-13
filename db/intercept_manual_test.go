package db

import (
	"strconv"
	"testing"
)

func TestInterceptManualSource(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	create := func(audit *InterceptAudit) int64 {
		t.Helper()
		id, err := d.CreateInterceptPending(0, 0, "manual-source-fixture", "test", "Bash", []byte(`{}`), "[模型] original reason", audit)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_pending WHERE id=$1`, id) })
		return id
	}
	for _, tc := range []struct {
		name, status, action, reason, source string
		audit                                *InterceptAudit
	}{
		{"manual exact", "allowed", "allow", "人工允许执行", "manual", &InterceptAudit{Correlation: "exact", RuleName: "original rule", InitialReason: "initial"}},
		{"manual null audit", "denied", "deny", "人工拒绝执行", "manual", nil},
		{"manual ambiguous", "allowed", "allow", "人工允许执行", "manual", &InterceptAudit{Correlation: "ambiguous"}},
		{"timeout", "timeout", "allow", "审批超时，按超时策略处理", "", &InterceptAudit{Correlation: "exact"}},
		{"cancel", "denied", "deny", "工作已取消", "", &InterceptAudit{Correlation: "exact"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := create(tc.audit)
			var sources []string
			if tc.source != "" {
				sources = []string{tc.source}
			}
			ok, err := d.ResolveIntercept(id, tc.status, tc.action, tc.reason, sources...)
			if err != nil || !ok {
				t.Fatalf("resolve=%t %v", ok, err)
			}
			if ok, err = d.ResolveIntercept(id, "denied", "deny", "late decision", "manual"); err != nil || ok {
				t.Fatalf("final state rewritten=%t %v", ok, err)
			}
			got, err := d.GetInterceptDetail(id)
			wantSource := tc.source
			if wantSource == "" {
				wantSource = "model"
			}
			if err != nil || got.DecisionSource != wantSource || got.Reason != "[模型] original reason" || got.Status != tc.status {
				t.Fatalf("final state=%+v %v", got, err)
			}
			if tc.audit == nil {
				if got.Audit != nil {
					t.Fatal("null audit fabricated")
				}
			} else if got.Audit.DecisionReason != tc.reason || got.Audit.RuleName != tc.audit.RuleName || got.Audit.InitialReason != tc.audit.InitialReason {
				t.Fatalf("audit metadata changed: %+v", got.Audit)
			}
			if tc.name == "manual ambiguous" && got.Audit.ExecutionStatus != "unknown" {
				t.Fatal("ambiguous execution was presented as exact")
			}
		})
	}
	for _, tc := range []struct{ status, reason, want string }{
		{"allowed", "人工允许执行", "manual"}, {"denied", "人工拒绝执行", "manual"},
		{"timeout", "人工允许执行", "model"}, {"denied", "人工允许执行", "model"},
		{"allowed", "人工允许执行 extra", "model"}, {"allowed", "普通规则允许", "model"},
	} {
		id := create(&InterceptAudit{DecisionReason: tc.reason})
		if _, err := d.Exec(`UPDATE intercept_pending SET status=$2 WHERE id=$1`, id, tc.status); err != nil {
			t.Fatal(err)
		}
		got, err := d.GetInterceptDetail(id)
		rows, total, listErr := d.ListTaskInterceptsPage("manual-source-fixture", 1, 100, InterceptApprovalFilter{Search: "#" + strconv.FormatInt(id, 10), DecisionSource: tc.want})
		if err != nil || got.DecisionSource != tc.want || listErr != nil || total != 1 || len(rows) != 1 || rows[0].DecisionSource != tc.want {
			t.Fatalf("legacy %s/%s=%+v %v list=%v total=%d err=%v", tc.status, tc.reason, got, err, rows, total, listErr)
		}
	}
	for _, originalSource := range []string{"", "rule", "unknown"} {
		id := create(&InterceptAudit{DecisionReason: "人工允许执行"})
		if _, err := d.Exec(`UPDATE intercept_pending SET status='allowed', decision_source=$2 WHERE id=$1`, id, originalSource); err != nil {
			t.Fatal(err)
		}
		got, err := d.GetInterceptDetail(id)
		if err != nil || got.DecisionSource != "manual" {
			t.Fatalf("proven legacy human source %q not inferred: %+v %v", originalSource, got, err)
		}
	}
	id := create(nil)
	if ok, err := d.ResolveIntercept(id, "allowed", "allow", "invalid source", "model"); err == nil || ok {
		t.Fatal("unsupported source accepted")
	}
	got, err := d.GetInterceptDetail(id)
	if err != nil || got.Status != "pending" || got.Audit != nil {
		t.Fatal("invalid source mutated record")
	}
}
