package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/mcphttp"
	actool "github.com/Autumn-27/norma/tool"
)

// 资产同步（ScopeSentry 数据源）。
//
// ScopeSentry 是一个 ASM 资产测绘平台，通过其 MCP 接口按【项目】/【任务】两个维度
// 拉取子域名、Web 应用、服务等资产，映射为 ARTEX 的公司 + 资产模型。数据源本身是
// 一个名为 "ScopeSentry" 的 http 传输 MCP 行（url + X-API-Key 头保存在 mcp_servers）。
//
// 与 agent 工具层不同，这里用 mcphttp.Client.Call 直接调用 MCP 工具、拿原始 JSON，
// 不触发 AskUser 权限弹窗（后台批量同步）。

const (
	scopeSentryMCPName = "ScopeSentry"
	syncMaxPerType     = 5000 // 单目标单类型的入库保护上限
	syncDefaultPage    = 100
)

// findMCPByName returns the MCP server row with the given name, or nil.
func (s *Server) findMCPByName(name string) (*db.MCPServer, error) {
	return s.findMCPByNameContext(context.Background(), name)
}
func (s *Server) findMCPByNameContext(ctx context.Context, name string) (*db.MCPServer, error) {
	all, err := s.m.pg.ListMCPContext(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range all {
		if m.Name == name {
			return m, nil
		}
	}
	return nil, nil
}

// scopeSentryClient dials the configured ScopeSentry MCP (http transport). The env
// map doubles as HTTP headers (X-API-Key). Callers must Close the client.
func (s *Server) scopeSentryClient(ctx context.Context) (*authorizedScopeSentryClient, error) {
	m, err := s.findMCPByNameContext(ctx, scopeSentryMCPName)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("数据源 %s 不存在，请先创建", scopeSentryMCPName)
	}
	if m.URL == "" {
		return nil, fmt.Errorf("数据源 %s 未配置 URL，请先配置", scopeSentryMCPName)
	}
	if m.Transport != "http" {
		return nil, fmt.Errorf("ScopeSentry requires http transport")
	}
	m, err = s.m.pg.AuthorizedMCPContext(ctx, m.ID, m.Revision)
	if err != nil {
		return nil, err
	}
	cl, err := mcphttp.New(ctx, m.Name, m.URL, jsonStrMap(m.Env), m.Insecure)
	if err != nil {
		return nil, err
	}
	if _, err = s.m.pg.AuthorizedMCPContext(ctx, m.ID, m.Revision); err != nil {
		cl.Close()
		return nil, err
	}
	return &authorizedScopeSentryClient{Client: cl, pg: s.m.pg, id: m.ID, revision: m.Revision}, nil
}

// envHasValue reports whether the env/header map has any non-empty value
// (i.e. an API key / Authorization header was filled in).
func envHasValue(raw json.RawMessage) bool {
	for _, v := range jsonStrMap(raw) {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// ---------- GET /api/sync/scopesentry/status ----------

func (s *Server) syncSSStatus(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	m, err := s.findMCPByNameContext(r.Context(), scopeSentryMCPName)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	resp := map[string]any{
		"id": int64(0), "revision": int64(0), "approved": false,
		"exists":     m != nil,
		"configured": false,
		"enabled":    false,
		"reachable":  false,
		"tools":      []string{},
	}
	if m == nil {
		writeJSON(w, 200, resp)
		return
	}
	resp["id"] = m.ID
	configured := m.URL != "" && envHasValue(m.Env)
	resp["configured"] = configured
	resp["enabled"] = m.Enabled
	resp["revision"] = m.Revision
	resp["approved"] = m.Approved
	resp["url"] = m.URL
	if m.Tools != nil {
		resp["tools"] = m.Tools
	}
	// Light reachability probe only when it can actually connect.
	if configured && m.Enabled && m.Approved {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		if cl, cerr := s.scopeSentryClient(ctx); cerr == nil {
			if _, terr := cl.Tools(ctx); terr == nil {
				resp["reachable"] = true
			}
			_ = cl.Close()
		}
	}
	writeJSON(w, 200, resp)
}

// ---------- POST /api/sync/scopesentry/datasource ----------
// Saves a pending configuration; explicit approval is granted on the MCP page.

func (s *Server) syncSSDatasource(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		Revision int64  `json:"revision"`
		URL      string `json:"url"`
		APIKey   string `json:"api_key"`
	}
	_ = decode(r, &body) // empty body = just create the placeholder
	body.URL = strings.TrimSpace(body.URL)
	body.APIKey = strings.TrimSpace(body.APIKey)

	m, err := s.findMCPByNameContext(r.Context(), scopeSentryMCPName)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if m == nil {
		m = &db.MCPServer{Name: scopeSentryMCPName, Transport: "http", Env: json.RawMessage(`{"X-API-Key":""}`)}
	}
	if m.ID != 0 && body.Revision != m.Revision {
		writeErr(w, 409, db.ErrMCPRevision.Error())
		return
	}
	if body.URL != "" {
		m.URL = body.URL
	}
	if body.APIKey != "" {
		env, _ := json.Marshal(map[string]string{"X-API-Key": body.APIKey})
		m.Env = env
	}
	// SaveMCP preserves unchanged activation, and disables changed configurations.

	id, err := pg.SaveMCPContext(r.Context(), m)
	if err != nil {
		writeMCPError(w, err)
		return
	}
	m.ID = id
	writeJSON(w, 200, mcpState(m))
}

// ---------- GET /api/sync/scopesentry/projects ----------

func (s *Server) syncSSProjects(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	cl, err := s.scopeSentryClient(ctx)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	defer cl.Close()

	args := map[string]any{
		"pageIndex": queryInt(r, "page", 1),
		"pageSize":  queryInt(r, "size", 50),
	}
	if q := r.URL.Query().Get("search"); q != "" {
		args["search"] = q
	}
	text, err := cl.Call(ctx, "list_projects_data", args)
	if err != nil {
		writeErr(w, 502, "list_projects_data 失败: "+err.Error())
		return
	}
	// {result:{All:[{id,name,logo,AssetCount,tag}], <tag>:[...]}, tag:{...}}
	var env struct {
		Result map[string]json.RawMessage `json:"result"`
		Tag    map[string]int             `json:"tag"`
	}
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		writeErr(w, 502, "解析项目列表失败: "+err.Error())
		return
	}
	projects := json.RawMessage("[]")
	if raw, ok := env.Result["All"]; ok && len(raw) > 0 {
		projects = raw
	}
	writeJSON(w, 200, map[string]any{"projects": projects, "tag": env.Tag})
}

// ---------- GET /api/sync/scopesentry/tasks ----------

func (s *Server) syncSSTasks(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	cl, err := s.scopeSentryClient(ctx)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	defer cl.Close()

	args := map[string]any{
		"pageIndex": queryInt(r, "page", 1),
		"pageSize":  queryInt(r, "size", 50),
	}
	if q := r.URL.Query().Get("search"); q != "" {
		args["search"] = q
	}
	text, err := cl.Call(ctx, "list_tasks", args)
	if err != nil {
		writeErr(w, 502, "list_tasks 失败: "+err.Error())
		return
	}
	var env struct {
		List json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		writeErr(w, 502, "解析任务列表失败: "+err.Error())
		return
	}
	tasks := env.List
	if len(tasks) == 0 {
		tasks = json.RawMessage("[]")
	}
	writeJSON(w, 200, map[string]any{"tasks": tasks})
}

// ---------- POST /api/sync/scopesentry/sync ----------

type ssSyncReq struct {
	Dimension     string   `json:"dimension"`   // "project" | "task"
	Targets       []string `json:"targets"`     // project ObjectIDs, or task names
	AssetTypes    []string `json:"asset_types"` // subdomain | app | service
	CreateCompany bool     `json:"create_company"`
	PageSize      int      `json:"page_size"`
}

func (s *Server) syncSSRun(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	as := s.assetStore()
	cs := s.companyStore()
	if as == nil || cs == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	var req ssSyncReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.Dimension != "project" && req.Dimension != "task" {
		writeErr(w, 400, "dimension 必须是 project 或 task")
		return
	}
	if len(req.Targets) == 0 {
		writeErr(w, 400, "targets 不能为空")
		return
	}
	if len(req.AssetTypes) == 0 {
		req.AssetTypes = []string{"subdomain", "service", "app"}
	}
	pageSize := req.PageSize
	if pageSize <= 0 || pageSize > 500 {
		pageSize = syncDefaultPage
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	cl, err := s.scopeSentryClient(ctx)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	defer cl.Close()

	writeJSON(w, 200, s.ssSync(ctx, cl, as, cs, req, pageSize))
}

// These are the operations a sync performs; the concrete stores keep their normal
// validation and transactions. Check cancellation before starting each operation:
// existing store methods cannot interrupt an SQL write already in flight.
type ssCaller interface {
	Call(context.Context, string, any) (string, error)
}

type ssAssetWriter interface {
	UpsertSubdomain(db.UpsertSubdomainReq) (int64, error)
	UpsertIP(db.UpsertIPReq) (int64, error)
	UpsertApp(db.UpsertAppReq) (int64, error)
	UpsertHTTPService(db.UpsertHTTPServiceReq) (int64, error)
	UpsertOtherService(db.UpsertOtherServiceReq) (int64, error)
}

type ssCompanyWriter interface {
	UpsertByName(string) (int64, error)
	AddScope(int64, []string, string) (int, int, int, []string)
	RecomputeAttribution() error
}

type ssSyncResult struct {
	Status    string         `json:"status"`
	Truncated bool           `json:"truncated"`
	Synced    map[string]int `json:"synced"`
	Companies []string       `json:"companies"`
	Warnings  []string       `json:"warnings"`
	Errors    []string       `json:"errors"`
}

func (result *ssSyncResult) setStatus() {
	writes := 0
	for _, n := range result.Synced {
		writes += n
	}
	switch {
	case len(result.Errors) > 0 && writes == 0:
		result.Status = "failed"
	case len(result.Errors) > 0 || len(result.Warnings) > 0 || result.Truncated:
		result.Status = "partial"
	default:
		result.Status = "success"
	}
}

func (s *Server) ssSync(ctx context.Context, cl ssCaller, as ssAssetWriter, cs ssCompanyWriter, req ssSyncReq, pageSize int) ssSyncResult {
	result := ssSyncResult{Synced: map[string]int{"subdomain": 0, "app": 0, "service": 0, "ip": 0}}
	madeCompany, interrupted := false, false
	stop := func() bool {
		if err := ctx.Err(); err != nil {
			if !interrupted {
				result.Errors = append(result.Errors, "同步未完成: "+err.Error())
				interrupted = true
			}
			return true
		}
		return false
	}

targets:
	for _, target := range req.Targets {
		if stop() {
			break
		}
		filter := map[string]any{req.Dimension: []string{target}}
		if req.Dimension == "project" && req.CreateCompany {
			name, roots, perr := s.ssProjectMeta(ctx, cl, target)
			if stop() {
				break
			}
			if perr != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("项目 %s 详情获取失败: %v", target, perr))
			} else if strings.TrimSpace(name) == "" {
				result.Warnings = append(result.Warnings, fmt.Sprintf("项目 %s 缺少 name，已跳过公司创建", target))
			} else {
				cid, cerr := cs.UpsertByName(name)
				if cerr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("公司 %s 创建失败: %v", name, cerr))
				} else {
					result.Companies = append(result.Companies, name)
					madeCompany = true
					if stop() {
						break
					}
					if len(roots) > 0 {
						_, _, invalid, scopeErrs := cs.AddScope(cid, roots, "scopesentry:"+name)
						for _, scopeErr := range scopeErrs {
							result.Errors = append(result.Errors, fmt.Sprintf("公司 %s 范围写入失败: %s", name, scopeErr))
						}
						if invalid > 0 && len(scopeErrs) == 0 {
							result.Warnings = append(result.Warnings, fmt.Sprintf("公司 %s 跳过 %d 条无效范围", name, invalid))
						}
					}
				}
			}
		}

		for _, at := range req.AssetTypes {
			if stop() {
				break targets
			}
			ssType, ok := map[string]string{"subdomain": "subdomain", "service": "asset", "app": "app"}[at]
			if !ok {
				result.Warnings = append(result.Warnings, "未知资产类型，已跳过: "+at)
				continue
			}
			items, truncated, ferr := s.ssPageAll(ctx, cl, ssType, filter, pageSize)
			if stop() {
				break targets
			}
			if ferr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s(%s) 拉取失败: %v", at, target, ferr))
			}
			if truncated {
				result.Truncated = true
				result.Warnings = append(result.Warnings, fmt.Sprintf("%s(%s) 达到 %d 条上限，已截断", at, target, syncMaxPerType))
			}
			// A later non-cancellation error does not discard validated earlier pages.
			for _, raw := range items {
				if stop() {
					break targets
				}
				result.Errors = append(result.Errors, s.ssIngest(ctx, as, at, raw, result.Synced)...)
			}
		}
	}

	if madeCompany && !stop() {
		if err := cs.RecomputeAttribution(); err != nil {
			result.Errors = append(result.Errors, "公司归属重建失败: "+err.Error())
		}
	}
	stop()
	result.setStatus()
	return result
}

// ssProjectMeta fetches a project's display name and its target root domains
// (get_project.target is a newline-separated list) for company + scope creation.
func (s *Server) ssProjectMeta(ctx context.Context, cl ssCaller, projectID string) (name string, roots []string, err error) {
	text, err := cl.Call(ctx, "get_project", map[string]any{"id": projectID})
	if err != nil {
		return "", nil, err
	}
	var p struct {
		Name   string `json:"name"`
		Target string `json:"target"`
	}
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		return "", nil, err
	}
	for _, line := range strings.Split(p.Target, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			roots = append(roots, t)
		}
	}
	return p.Name, roots, nil
}

// ssPageAll pages through list_assets for one asset_type + filter until a short/empty
// page or the protection cap. The {list:[...]} envelope has no total, so we stop when
// a page returns fewer than pageSize items.
func (s *Server) ssPageAll(ctx context.Context, cl ssCaller, ssType string, filter map[string]any, pageSize int) (items []json.RawMessage, truncated bool, err error) {
	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			return items, truncated, err
		}
		args := map[string]any{"asset_type": ssType, "pageIndex": page, "pageSize": pageSize}
		if len(filter) > 0 {
			args["filter"] = filter
		}
		text, cerr := cl.Call(ctx, "list_assets", args)
		if cerr != nil {
			return items, truncated, cerr
		}
		var env struct {
			List json.RawMessage `json:"list"`
		}
		if uerr := json.Unmarshal([]byte(text), &env); uerr != nil {
			return items, truncated, uerr
		}
		if len(env.List) == 0 || string(env.List) == "null" {
			return items, truncated, fmt.Errorf("list_assets 响应缺少 list 数组")
		}
		var list []json.RawMessage
		if uerr := json.Unmarshal(env.List, &list); uerr != nil {
			return items, truncated, uerr
		}
		if len(list) == 0 {
			break
		}
		remaining := syncMaxPerType - len(items)
		if len(list) > remaining {
			items = append(items, list[:remaining]...)
			return items, true, nil
		}
		items = append(items, list...)
		// An exact cap is complete if this page is short. Otherwise the next
		// request keeps the original page size and filter to probe for more.
		if len(list) < pageSize {
			break
		}
	}
	return items, truncated, nil
}

// ssIngest maps one ScopeSentry asset JSON to the ARTEX asset store and upserts it.
// Returns errors for invalid records and failed writes, including derived IPs.
// synced counts successful upserts, including updates to existing assets.
func (s *Server) ssIngest(ctx context.Context, as ssAssetWriter, assetType string, raw json.RawMessage, synced map[string]int) []string {
	if ctx.Err() != nil {
		return nil // The execution loop reports interruption once.
	}
	var errs []string
	switch assetType {
	case "subdomain":
		var it struct {
			Host  string   `json:"host"`
			Type  string   `json:"type"`
			Value []string `json:"value"`
			IP    []string `json:"ip"`
		}
		if err := json.Unmarshal(raw, &it); err != nil {
			return []string{"subdomain 解析失败: " + err.Error()}
		}
		if strings.TrimSpace(it.Host) == "" {
			return []string{"subdomain 缺少 host，已跳过"}
		}
		if ctx.Err() != nil {
			return errs
		}
		if _, err := as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: it.Host, RecordType: it.Type, RecordValue: it.Value}); err != nil {
			return []string{"subdomain " + it.Host + ": " + err.Error()}
		}
		synced["subdomain"]++
		for _, ip := range it.IP {
			if ctx.Err() != nil {
				return errs
			}
			if strings.TrimSpace(ip) == "" {
				errs = append(errs, "subdomain "+it.Host+" 的 ip 为空，已跳过")
				continue
			}
			if _, err := as.UpsertIP(db.UpsertIPReq{IP: ip, BoundDomains: []string{it.Host}}); err != nil {
				errs = append(errs, "ip "+ip+" ("+it.Host+"): "+err.Error())
			} else {
				synced["ip"]++
			}
		}
	case "app":
		var it struct {
			Name        string `json:"name"`
			Category    string `json:"category"`
			Description string `json:"description"`
			ICP         string `json:"icp"`
		}
		if err := json.Unmarshal(raw, &it); err != nil {
			return []string{"app 解析失败: " + err.Error()}
		}
		if strings.TrimSpace(it.Name) == "" {
			return []string{"app 缺少 name，已跳过"}
		}
		if ctx.Err() != nil {
			return errs
		}
		if _, err := as.UpsertApp(db.UpsertAppReq{Name: it.Name, Category: it.Category, Description: it.Description, ICP: it.ICP}); err != nil {
			return []string{"app " + it.Name + ": " + err.Error()}
		}
		synced["app"]++
	case "service":
		var it struct {
			Domain   string   `json:"domain"`
			IP       string   `json:"ip"`
			Port     string   `json:"port"`
			Service  string   `json:"service"`
			URL      string   `json:"url"`
			Title    string   `json:"title"`
			Status   *int     `json:"status"`
			Products []string `json:"products"`
			Icon     string   `json:"icon"`
		}
		if err := json.Unmarshal(raw, &it); err != nil {
			return []string{"service 解析失败: " + err.Error()}
		}
		if it.Service == "http" || it.URL != "" {
			if strings.TrimSpace(it.URL) == "" {
				return []string{"service http 缺少 url，已跳过"}
			}
			if ctx.Err() != nil {
				return errs
			}
			if _, err := as.UpsertHTTPService(db.UpsertHTTPServiceReq{
				URL: it.URL, Technologies: it.Products, StatusCode: it.Status,
				PageTitle: it.Title, FaviconMMH3: it.Icon, IP: it.IP,
			}); err != nil {
				return []string{"service " + it.URL + ": " + err.Error()}
			}
		} else {
			port, _ := strconv.Atoi(it.Port)
			if ctx.Err() != nil {
				return errs
			}
			if _, err := as.UpsertOtherService(db.UpsertOtherServiceReq{
				Domain: it.Domain, IP: it.IP, Port: port, ServiceName: it.Service,
			}); err != nil {
				return []string{"service " + it.IP + ":" + it.Port + ": " + err.Error()}
			}
		}
		synced["service"]++
	}
	return errs
}

// queryInt reads an int query param with a default fallback.
func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

type authorizedScopeSentryClient struct {
	*mcphttp.Client
	pg           *db.DB
	id, revision int64
}

func (c *authorizedScopeSentryClient) Call(ctx context.Context, name string, args any) (string, error) {
	if _, err := c.pg.AuthorizedMCPContext(ctx, c.id, c.revision); err != nil {
		return "", err
	}
	return c.Client.Call(ctx, name, args)
}
func (c *authorizedScopeSentryClient) Tools(ctx context.Context) ([]actool.CoreTool, error) {
	if _, err := c.pg.AuthorizedMCPContext(ctx, c.id, c.revision); err != nil {
		return nil, err
	}
	ts, err := c.Client.Tools(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = c.pg.AuthorizedMCPContext(ctx, c.id, c.revision); err != nil {
		return nil, err
	}
	return ts, nil
}
