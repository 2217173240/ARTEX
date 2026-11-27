package agent

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

type w3MockDB struct {
	interceptRules  []db.InterceptRule
	settings        map[string]string
	assetRules      []db.AssetInterceptRule
	taskAssetRules  []db.AssetInterceptRule
	constraints     []db.Constraint
	assetRulesErr   error
	taskRulesErr    error
	assetUpsertSeen bool
	pendingInserts  []string
}

func (m *w3MockDB) open(t *testing.T) *db.DB {
	t.Helper()
	conn := sql.OpenDB(w3MockConnector{m: m})
	t.Cleanup(func() { _ = conn.Close() })
	return &db.DB{DB: conn}
}

type w3MockConnector struct{ m *w3MockDB }

func (c w3MockConnector) Connect(context.Context) (driver.Conn, error) {
	return w3MockConn{m: c.m}, nil
}
func (c w3MockConnector) Driver() driver.Driver { return w3MockDriver{} }

type w3MockDriver struct{}

func (w3MockDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type w3MockConn struct{ m *w3MockDB }

func (w3MockConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected prepare") }
func (w3MockConn) Close() error                        { return nil }
func (w3MockConn) Begin() (driver.Tx, error)           { return nil, errors.New("unexpected tx") }

func (c w3MockConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "INSERT INTO assets") || strings.Contains(query, "UPDATE assets") {
		c.m.assetUpsertSeen = true
	}
	return driver.RowsAffected(1), nil
}

func (c w3MockConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "FROM intercept_rules"):
		cols := strings.Split("id,name,enabled,priority,match_target,match_type,pattern,action,message,timeout_enabled,timeout_seconds,timeout_action,created_at,updated_at", ",")
		rows := &w3MockRows{columns: cols}
		for _, r := range c.m.interceptRules {
			rows.values = append(rows.values, []driver.Value{r.ID, r.Name, r.Enabled, int64(r.Priority), r.MatchTarget, r.MatchType, r.Pattern, r.Action, r.Message, r.TimeoutEnabled, int64(r.TimeoutSeconds), r.TimeoutAction, r.CreatedAt, r.UpdatedAt})
		}
		return rows, nil
	case strings.Contains(query, "FROM settings"):
		key, _ := args[0].Value.(string)
		rows := &w3MockRows{columns: []string{"value"}}
		if v, ok := c.m.settings[key]; ok {
			rows.values = [][]driver.Value{{v}}
		}
		return rows, nil
	case strings.Contains(query, "INSERT INTO intercept_pending"):
		if len(args) > 2 {
			if taskID, ok := args[2].Value.(string); ok {
				c.m.pendingInserts = append(c.m.pendingInserts, taskID)
			}
		}
		return &w3MockRows{columns: []string{"id"}, values: [][]driver.Value{{int64(1)}}}, nil
	case strings.Contains(query, "FROM asset_intercept_rules"):
		if c.m.assetRulesErr != nil {
			return nil, c.m.assetRulesErr
		}
		cols := []string{"id", "enabled", "kind", "pattern", "note", "builtin", "created_at", "updated_at"}
		rows := &w3MockRows{columns: cols}
		for _, r := range c.m.assetRules {
			rows.values = append(rows.values, []driver.Value{r.ID, r.Enabled, r.Kind, r.Pattern, r.Note, r.Builtin, r.CreatedAt, r.UpdatedAt})
		}
		return rows, nil
	case strings.Contains(query, "FROM task_intercept_rules"):
		if c.m.taskRulesErr != nil {
			return nil, c.m.taskRulesErr
		}
		cols := []string{"id", "enabled", "action", "kind", "pattern", "note", "created_at", "updated_at"}
		rows := &w3MockRows{columns: cols}
		for _, r := range c.m.taskAssetRules {
			rows.values = append(rows.values, []driver.Value{r.ID, r.Enabled, r.Action, r.Kind, r.Pattern, r.Note, r.CreatedAt, r.UpdatedAt})
		}
		return rows, nil
	case strings.Contains(query, "FROM task_constraints"):
		cols := []string{"id", "kind", "text", "origin", "created_at"}
		rows := &w3MockRows{columns: cols}
		for _, ct := range c.m.constraints {
			rows.values = append(rows.values, []driver.Value{ct.ID, ct.Kind, ct.Text, ct.Origin, ct.CreatedAt})
		}
		return rows, nil
	case strings.Contains(query, "FROM assets"):
		c.m.assetUpsertSeen = true
		return &w3MockRows{columns: []string{"id"}}, nil
	default:
		// Benign empty result for secondary exploration/overview queries in unit tests.
		return &w3MockRows{columns: []string{"id"}}, nil
	}
}

type w3MockRows struct {
	columns []string
	values  [][]driver.Value
}

func (r *w3MockRows) Columns() []string { return r.columns }
func (r *w3MockRows) Close() error      { return nil }
func (r *w3MockRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}

type captureSystemProvider struct {
	onReq func(turn int, req llm.CompletionRequest) []llm.StreamEvent
	turn  int
}

func (p *captureSystemProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	p.turn++
	turn := p.turn
	return func(yield func(llm.StreamEvent, error) bool) {
		for _, ev := range p.onReq(turn, req) {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func (p *captureSystemProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	acc := llm.NewAccumulator()
	for ev, err := range p.Stream(ctx, req) {
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
		acc.Add(ev)
	}
	return acc.Message(), acc.StopReason, acc.Usage, nil
}

func TestPlannerAndMainAgentInvokeGuardAndInjectBlocks(t *testing.T) {
	oldAugment, oldResolve := ToolAugment, ToolResolve
	t.Cleanup(func() { ToolAugment, ToolResolve = oldAugment, oldResolve })
	ToolAugment = nil
	ToolResolve = nil

	now := time.Now()
	mock := &w3MockDB{
		interceptRules: []db.InterceptRule{
			{
				ID:          11,
				Name:        "禁止危险命令",
				Enabled:     true,
				Priority:    100,
				MatchTarget: "tool_input",
				MatchType:   "contains",
				Pattern:     "forbidden-probe",
				Action:      "deny",
				Message:     "blocked by test guard",
				CreatedAt:   now,
				UpdatedAt:   now,
			},
		},
		settings: map[string]string{
			"intercept_enabled_tools": `["Bash"]`,
			"llm_judge_enabled":       "false",
		},
		assetRules: []db.AssetInterceptRule{
			{ID: 1, Enabled: true, Kind: "fuzzy_domain", Pattern: ".gov.cn", Note: "政务拦截", Builtin: true},
		},
		taskAssetRules: []db.AssetInterceptRule{
			{ID: 2, Enabled: true, Action: "allow", Kind: "exact_domain", Pattern: "app.example.test", Note: "授权靶标"},
		},
		constraints: []db.Constraint{
			{ID: 1, Kind: "deny", Text: "禁止目录爆破", Origin: "human", CreatedAt: now},
		},
	}
	d := mock.open(t)
	ic := intercept.New(d)
	g := guard.NewWithInterceptor(ic)
	as := d.Assets()
	ts := d.Exploration(9)

	t.Run("planner", func(t *testing.T) {
		var seenSystem string
		prov := &captureSystemProvider{
			onReq: func(turn int, req llm.CompletionRequest) []llm.StreamEvent {
				if turn == 1 {
					seenSystem = strings.Join(req.System, "\n\n")
					return []llm.StreamEvent{
						{Type: llm.SEToolUseStart, ToolID: "call-p1", ToolName: "Bash"},
						{Type: llm.SEToolInputJSON, Text: `{"command":"forbidden-probe"}`},
						{Type: llm.SEMessageDelta, StopReason: "tool_use"},
					}
				}
				return []llm.StreamEvent{
					{Type: llm.SETextDelta, Text: "done"},
					{Type: llm.SEMessageDelta, StopReason: "end_turn"},
				}
			},
		}
		pl := NewPlanner(prov, "test-model", t.TempDir(), nil, 0, 3)
		pl.SetGuard(g)
		if _, _, err := pl.Plan(t.Context(), 42, as, ts, "goal", nil, nil); err != nil {
			t.Fatalf("Plan error: %v", err)
		}
		if !strings.Contains(seenSystem, "【资产闸门规则") || !strings.Contains(seenSystem, "app.example.test") || !strings.Contains(seenSystem, ".gov.cn") {
			t.Fatalf("planner system prompt missing asset gate block:\n%s", seenSystem)
		}
		if !strings.Contains(seenSystem, "禁止目录爆破") {
			t.Fatalf("planner system prompt missing constraint block:\n%s", seenSystem)
		}
	})

	t.Run("mainagent", func(t *testing.T) {
		var seenSystem string
		prov := &captureSystemProvider{
			onReq: func(turn int, req llm.CompletionRequest) []llm.StreamEvent {
				if turn == 1 {
					seenSystem = strings.Join(req.System, "\n\n")
					return []llm.StreamEvent{
						{Type: llm.SEToolUseStart, ToolID: "call-m1", ToolName: "Bash"},
						{Type: llm.SEToolInputJSON, Text: `{"command":"forbidden-probe"}`},
						{Type: llm.SEMessageDelta, StopReason: "tool_use"},
					}
				}
				return []llm.StreamEvent{
					{Type: llm.SETextDelta, Text: "done"},
					{Type: llm.SEMessageDelta, StopReason: "end_turn"},
				}
			},
		}
		ma := NewMainAgent(prov, "test-model", t.TempDir(), nil, 0, 3)
		ma.SetGuard(g)
		if _, err := ma.Chat(t.Context(), 42, 0, as, ts, "goal", "hi", nil, nil, nil, nil, nil); err != nil {
			t.Fatalf("MainAgent.Chat error: %v", err)
		}
		if !strings.Contains(seenSystem, "【资产闸门规则") || !strings.Contains(seenSystem, "app.example.test") || !strings.Contains(seenSystem, ".gov.cn") {
			t.Fatalf("mainagent system prompt missing asset gate block:\n%s", seenSystem)
		}
		if !strings.Contains(seenSystem, "禁止目录爆破") {
			t.Fatalf("mainagent system prompt missing constraint block:\n%s", seenSystem)
		}
	})

	var blockedCount int
	for _, entry := range g.Audit() {
		if entry.Tool == "Bash" && entry.Action == "block" {
			blockedCount++
		}
	}
	if blockedCount < 2 {
		t.Fatalf("expected Planner and MainAgent Bash calls to be blocked by guard hooks, audit=%+v", g.Audit())
	}
	if len(mock.pendingInserts) < 2 || mock.pendingInserts[0] != "42" || mock.pendingInserts[1] != "42" {
		t.Fatalf("expected intercept log to carry task_id=42 for Planner and MainAgent, got %v", mock.pendingInserts)
	}
}

func TestDecomposeGoalsWithProviderToolResolve(t *testing.T) {
	oldResolve := ToolResolve
	t.Cleanup(func() { ToolResolve = oldResolve })

	t.Run("catalog_failure_fails_closed_before_model", func(t *testing.T) {
		catalogErr := errors.New("catalog db offline")
		ToolResolve = func(_ context.Context, agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, error) {
			if agentKey != "goals" {
				t.Fatalf("agentKey = %q, want goals", agentKey)
			}
			return nil, catalogErr
		}
		modelCalled := false
		prov := &captureSystemProvider{
			onReq: func(_ int, _ llm.CompletionRequest) []llm.StreamEvent {
				modelCalled = true
				return nil
			},
		}
		got := DecomposeGoalsWithProvider(t.Context(), prov, t.TempDir(), "test goal", "", nil, nil, 1, false, 0, nil)
		if modelCalled || got != nil {
			t.Fatalf("catalog failure must fail closed without calling provider: modelCalled=%v got=%v", modelCalled, got)
		}
	})

	t.Run("uses_goals_binding_and_preserves_add_task_scope", func(t *testing.T) {
		var sawAgentKey string
		var seenToolNames []string
		ToolResolve = func(_ context.Context, agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, error) {
			sawAgentKey = agentKey
			var out []actool.CoreTool
			for _, tool := range tools {
				seenToolNames = append(seenToolNames, tool.Name())
				// Simulate DB dropping set_constraints while keeping set_goals.
				if tool.Name() == "set_goals" {
					out = append(out, DecorateTool(tool, "db-overridden-set-goals", nil))
				}
			}
			return out, nil
		}

		mock := &w3MockDB{}
		d := mock.open(t)
		var advertisedTools []string
		var seenSystem string
		prov := &captureSystemProvider{
			onReq: func(_ int, req llm.CompletionRequest) []llm.StreamEvent {
				seenSystem = strings.Join(req.System, "\n\n")
				for _, td := range req.Tools {
					advertisedTools = append(advertisedTools, td.Name)
				}
				return []llm.StreamEvent{
					{Type: llm.SETextDelta, Text: "done"},
					{Type: llm.SEMessageDelta, StopReason: "end_turn"},
				}
			},
		}

		DecomposeGoalsWithProvider(t.Context(), prov, t.TempDir(), "test https://app.example.test", "", d.Assets(), nil, 7, false, 0, nil)
		if sawAgentKey != "goals" {
			t.Fatalf("ToolResolve agentKey = %q, want goals", sawAgentKey)
		}
		if len(seenToolNames) != 2 || seenToolNames[0] != "set_goals" || seenToolNames[1] != "set_constraints" {
			t.Fatalf("unexpected tools passed to ToolResolve(goals): %v", seenToolNames)
		}
		if len(advertisedTools) != 2 || advertisedTools[0] != "set_goals" || advertisedTools[1] != "add_task_scope" {
			t.Fatalf("advertised tools = %v, want [set_goals add_task_scope]", advertisedTools)
		}
		if !strings.Contains(seenSystem, "add_task_scope") {
			t.Fatalf("system prompt missing goalsScopeTail")
		}
	})
}

func TestInsertAssetsFailsClosedOnAssetGateDBError(t *testing.T) {
	t.Run("global_rules_error", func(t *testing.T) {
		mock := &w3MockDB{assetRulesErr: errors.New("global gate query failed")}
		d := mock.open(t)
		ts := NewToolSet(nil, "worker-1")
		ts.SetAssetStore(d.Assets(), d.Companies())

		res, err := ts.insertAssets().Call(t.Context(), json.RawMessage(`{"assets":[{"type":"root_domain","domain":"example.com"}]}`), nil)
		if err != nil {
			t.Fatalf("unexpected transport error: %v", err)
		}
		if !res.IsError || !strings.Contains(res.Flatten(), "资产闸门校验失败") {
			t.Fatalf("expected fail-closed gate error, got IsError=%v %q", res.IsError, res.Flatten())
		}
		if mock.assetUpsertSeen {
			t.Fatal("asset insert reached DB despite global gate read failure")
		}
	})

	t.Run("task_rules_error", func(t *testing.T) {
		mock := &w3MockDB{taskRulesErr: errors.New("task gate query failed")}
		d := mock.open(t)
		ts := NewToolSet(nil, "worker-1")
		ts.SetAssetStore(d.Assets(), d.Companies())
		ts.SetTaskID(12)

		res, err := ts.insertAssets().Call(t.Context(), json.RawMessage(`{"assets":[{"type":"root_domain","domain":"example.com"}]}`), nil)
		if err != nil {
			t.Fatalf("unexpected transport error: %v", err)
		}
		if !res.IsError || !strings.Contains(res.Flatten(), "资产闸门校验失败") {
			t.Fatalf("expected fail-closed gate error, got IsError=%v %q", res.IsError, res.Flatten())
		}
		if mock.assetUpsertSeen {
			t.Fatal("asset insert reached DB despite task gate read failure")
		}
	})
}

func TestAssetGateBlockFormattingAndBounds(t *testing.T) {
	if got := assetGateBlock(nil, 1); got != "" {
		t.Fatalf("nil AssetStore should return empty string, got %q", got)
	}

	disabledOnly := (&w3MockDB{
		assetRules: []db.AssetInterceptRule{
			{ID: 1, Enabled: false, Kind: "exact_domain", Pattern: "disabled.example"},
		},
	}).open(t)
	if got := assetGateBlock(disabledOnly.Assets(), 0); got != "" {
		t.Fatalf("disabled-only rules should return empty string, got %q", got)
	}

	var manyGlobals []db.AssetInterceptRule
	for i := 1; i <= 25; i++ {
		manyGlobals = append(manyGlobals, db.AssetInterceptRule{
			ID:      int64(i),
			Enabled: true,
			Kind:    "exact_domain",
			Pattern: "blocked-" + string(rune('a'+i-1)) + ".example",
			Builtin: i <= 20,
		})
	}
	mock := &w3MockDB{
		assetRules: manyGlobals,
		taskAssetRules: []db.AssetInterceptRule{
			{ID: 100, Enabled: true, Action: "allow", Kind: "exact_domain", Pattern: "target.example", Note: "白名单"},
			{ID: 101, Enabled: true, Action: "block", Kind: "cidr", Pattern: "10.0.0.0/8", Note: "内网禁扫"},
			{ID: 102, Enabled: false, Action: "block", Kind: "exact_ip", Pattern: "192.0.2.99"},
		},
	}
	block := assetGateBlock(mock.open(t).Assets(), 5)
	for _, want := range []string{
		"【资产闸门规则",
		"允许范围（仅允许命中以下规则的资产）：",
		"- [exact_domain] target.example（白名单）",
		"禁止范围（命中以下规则将被系统拦截）：",
		"- [cidr] 10.0.0.0/8（内网禁扫）",
		"另有 7 条已启用规则未列出",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("assetGateBlock missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "192.0.2.99") {
		t.Fatalf("disabled rule leaked into assetGateBlock:\n%s", block)
	}

	// Verify Worker places assetGateBlock in the static cached prefix before DynamicBoundary.
	intent := &db.Node{ID: 77, Kind: db.KindIntent, Payload: json.RawMessage(`{"summary":"probe target"}`)}
	staticSys := workerStaticSystem("", "", "work") + block
	sys, boundary := buildWorkerSystemPrompt(staticSys, "", DeferredInfo{}, "work/tasks/5/i77", intent, "")
	prefix := strings.Join(sys[:boundary], "\n\n")
	if !strings.Contains(prefix, "target.example") || !strings.Contains(prefix, "10.0.0.0/8") {
		t.Fatalf("worker static prefix before DynamicBoundary missing assetGateBlock:\n%s", prefix)
	}
}
