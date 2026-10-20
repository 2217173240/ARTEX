package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/internal/processenv"
	"github.com/Autumn-27/artex/mcphttp"
	"github.com/Autumn-27/norma/mcp"
	actool "github.com/Autumn-27/norma/tool"
)

// mcpClient is the shared surface of a connected MCP server (stdio, Streamable HTTP,
// or legacy SSE),
// so tools/list and cleanup are handled uniformly regardless of transport.
type mcpClient interface {
	Tools(context.Context) ([]actool.CoreTool, error)
	Close() error
}

// connectMCP dials one MCP server per its transport. Callers must Close the client.
func connectMCP(ctx context.Context, m *db.MCPServer) (mcpClient, error) {
	switch m.Transport {
	case "stdio":
		if m.Command == "" {
			return nil, fmt.Errorf("stdio 传输缺少命令")
		}
		return mcp.NewStdioClient(ctx, m.Name, m.Command, processenv.StdioOverrides(jsonStrMap(m.Env)), jsonStrSlice(m.Args)...)
	case "http":
		if m.URL == "" {
			return nil, fmt.Errorf("http 传输缺少 URL")
		}
		// env map doubles as HTTP headers (e.g. Authorization).
		return mcphttp.New(ctx, m.Name, m.URL, jsonStrMap(m.Env), m.Insecure)
	case "sse":
		if m.URL == "" {
			return nil, fmt.Errorf("sse 传输缺少 URL")
		}
		return mcphttp.NewSSE(ctx, m.Name, m.URL, jsonStrMap(m.Env), m.Insecure)
	default:
		return nil, fmt.Errorf("未知传输方式 %q", m.Transport)
	}
}

// connectAuthorizedMCP only dials authoritative persisted, approved configuration.
func connectAuthorizedMCP(ctx context.Context, pg *db.DB, id, revision int64) (mcpClient, error) {
	m, err := pg.AuthorizedMCPContext(ctx, id, revision)
	if err != nil {
		return nil, err
	}
	cl, err := connectMCP(ctx, m)
	if err != nil {
		return nil, err
	}
	if _, err = pg.AuthorizedMCPContext(ctx, id, revision); err != nil {
		cl.Close()
		return nil, err
	}
	return &authorizedMCPClient{mcpClient: cl, pg: pg, id: id, revision: revision}, nil
}

type authorizedMCPClient struct {
	mcpClient
	pg           *db.DB
	id, revision int64
}

func (c *authorizedMCPClient) Tools(ctx context.Context) ([]actool.CoreTool, error) {
	if _, err := c.pg.AuthorizedMCPContext(ctx, c.id, c.revision); err != nil {
		return nil, err
	}
	ts, err := c.mcpClient.Tools(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = c.pg.AuthorizedMCPContext(ctx, c.id, c.revision); err != nil {
		return nil, err
	}
	for i, t := range ts {
		ts[i] = &authorizedMCPTool{CoreTool: t, pg: c.pg, id: c.id, revision: c.revision}
	}
	return ts, nil
}

type authorizedMCPTool struct {
	actool.CoreTool
	pg           *db.DB
	id, revision int64
}

func (t *authorizedMCPTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	if _, err := t.pg.AuthorizedMCPContext(ctx, t.id, t.revision); err != nil {
		return actool.Result{}, err
	}
	return t.CoreTool.Call(ctx, in, tc)
}

// discoverAndCacheMCP connects to one MCP, lists its tools, and persists the tool
// names to mcp_tools_cache so the UI shows them without a live connection.
func (s *Server) discoverAndCacheMCP(ctx context.Context, m *db.MCPServer) error {
	cl, err := connectAuthorizedMCP(ctx, s.m.pg, m.ID, m.Revision)
	if err != nil {
		return err
	}
	defer cl.Close()
	ts, err := cl.Tools(ctx)
	if err != nil {
		return err
	}
	tools := make([]db.MCPTool, 0, len(ts))
	for _, t := range ts {
		tools = append(tools, db.MCPTool{Name: t.Name(), Description: t.Description()})
	}
	if err := s.m.pg.SaveMCPToolsContext(ctx, m.ID, tools, m.Revision); err != nil {
		return err
	}
	log.Printf("[mcp] %s 发现 %d 个工具并已缓存", m.Name, len(tools))
	return nil
}

// discoverEmptyMCPsOnStartup fills the tool cache for any enabled MCP that has none
// yet (notably the seeded browser MCP on first run). Runs sequentially in one
// goroutine so we never spawn many stdio servers (npx) at once, and never blocks
// startup. Best-effort: a failure leaves the cache empty to retry next start.
func (s *Server) discoverEmptyMCPsOnStartup() {
	servers, err := s.m.pg.ListMCPContext(s.ctx)
	if err != nil {
		log.Printf("[mcp] 启动自动发现: 读取列表失败: %v", err)
		return
	}
	for _, m := range servers {
		if !m.Enabled || !m.Approved || len(m.Tools) > 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, 90*time.Second)
		if err := s.discoverAndCacheMCP(ctx, m); err != nil {
			log.Printf("[mcp] 启动自动发现 %s 失败: %v", m.Name, err)
		}
		cancel()
	}
}
