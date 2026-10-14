// Package mcphttp contains remote Streamable HTTP and legacy SSE MCP clients.
//
// The core mcp package only speaks stdio; this adapts an HTTP/JSON-RPC MCP server
// to the same tool.CoreTool shape (mcp__server__tool) WITHOUT touching the SDK.
// It implements the MCP "Streamable HTTP" transport: JSON-RPC over HTTP POST, with
// the server free to answer either as application/json or a text/event-stream (SSE)
// carrying the response. Custom headers (e.g. Authorization) come from the MCP
// row's env map, so a bearer token is configured as {"Authorization":"Bearer …"}.
package mcphttp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/internal/httpsecurity"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

const protocolVersion = "2025-06-18"
const legacySSEProtocolVersion = "2024-11-05"

// ToolName mirrors mcp.ToolName so remote tools share the mcp__server__tool scheme.
func ToolName(server, name string) string { return "mcp__" + server + "__" + name }

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp rpc error %d: %s", e.Code, e.Message) }

// Client is a connection to one remote MCP server. New uses Streamable HTTP;
// NewSSE uses the legacy GET /sse + POST /message transport.
type Client struct {
	guard     *circuit
	closeOnce sync.Once
	server    string
	url       string
	headers   map[string]string
	http      *http.Client

	mu        sync.Mutex
	nextID    int
	sessionID string

	// legacySSE is the pre-2025 MCP SSE transport used by servers such as GSL5.
	legacySSE    bool
	messageURL   string
	streamBody   io.ReadCloser
	streamCancel context.CancelFunc
	streamDone   chan struct{}
	pendingMu    sync.Mutex
	pending      map[int]chan *rpcResponse
	protocol     string
}

func normalizeHeaders(headers map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(headers))
	for key, value := range headers {
		clean := strings.TrimSpace(key)
		if clean == "" || strings.ContainsAny(clean, "\r\n") {
			return nil, fmt.Errorf("invalid HTTP header name %q", key)
		}
		out[clean] = value
	}
	return out, nil
}

// New connects to a remote MCP endpoint and performs the initialize handshake.
// headers are sent on every request (Authorization, custom API keys, …).
// When insecure is true, TLS certificate verification is skipped so servers that
// present a self-signed certificate can still be reached (issue #108).
func New(ctx context.Context, server, url string, headers map[string]string, insecure bool) (*Client, error) {
	return NewWithOptions(ctx, server, url, headers, insecure, Options{})
}

func NewWithOptions(ctx context.Context, server, url string, headers map[string]string, insecure bool, options Options) (*Client, error) {
	options, err := options.defaults()
	if err != nil {
		return nil, err
	}
	cleanHeaders, err := normalizeHeaders(headers)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Timeout: 120 * time.Second, CheckRedirect: httpsecurity.SameOriginRedirect}
	if insecure {
		hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	c := &Client{
		guard:    newCircuit(options),
		server:   server,
		url:      url,
		headers:  cleanHeaders,
		http:     hc,
		protocol: protocolVersion,
	}
	if err := c.initialize(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// NewSSE connects to the legacy MCP SSE transport: a long-lived GET /sse
// announces a per-session POST /message endpoint, while JSON-RPC responses are
// delivered asynchronously as SSE message events.
func NewSSE(ctx context.Context, server, sseURL string, headers map[string]string, insecure bool) (*Client, error) {
	return NewSSEWithOptions(ctx, server, sseURL, headers, insecure, Options{})
}

func NewSSEWithOptions(ctx context.Context, server, sseURL string, headers map[string]string, insecure bool, options Options) (*Client, error) {
	options, err := options.defaults()
	if err != nil {
		return nil, err
	}
	cleanHeaders, err := normalizeHeaders(headers)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{CheckRedirect: httpsecurity.SameOriginRedirect} // the SSE stream is intentionally long-lived.
	if insecure {
		hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	streamCtx, cancel := context.WithCancel(context.Background())
	setupCtx, setupCancel := context.WithTimeout(ctx, 120*time.Second)
	defer setupCancel()
	stopSetup := context.AfterFunc(setupCtx, cancel)
	defer stopSetup()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, sseURL, nil)
	if err != nil {
		cancel()
		return nil, errors.New("mcp: invalid SSE URL")
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range cleanHeaders {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		cancel()
		if setupCtx.Err() != nil {
			return nil, setupCtx.Err()
		}
		return nil, errors.New("mcp: SSE connection failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("mcp sse http %d", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	endpoint, err := readSSEEndpoint(reader, sseURL)
	if err != nil {
		resp.Body.Close()
		cancel()
		if setupCtx.Err() != nil {
			return nil, setupCtx.Err()
		}
		return nil, errors.New("mcp: SSE endpoint negotiation failed")
	}
	stopSetup()
	if err := setupCtx.Err(); err != nil {
		resp.Body.Close()
		cancel()
		return nil, err
	}
	c := &Client{
		guard:        newCircuit(options),
		server:       server,
		url:          sseURL,
		headers:      cleanHeaders,
		http:         hc,
		legacySSE:    true,
		messageURL:   endpoint,
		streamBody:   resp.Body,
		streamCancel: cancel,
		streamDone:   make(chan struct{}),
		pending:      make(map[int]chan *rpcResponse),
		protocol:     legacySSEProtocolVersion,
	}
	go c.readLegacySSE(reader)
	if err := c.initialize(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func readSSEEndpoint(r *bufio.Reader, base string) (string, error) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("mcp sse endpoint: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		candidate := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if candidate == "" {
			continue
		}
		u, err := url.Parse(candidate)
		if err != nil {
			return "", errors.New("mcp: invalid SSE endpoint URL")
		}
		b, err := url.Parse(base)
		if err != nil {
			return "", err
		}
		u = b.ResolveReference(u)
		if !httpsecurity.SameOrigin(b, u) {
			return "", fmt.Errorf("mcp sse endpoint has a different origin")
		}
		candidate = u.String()
		return candidate, nil
	}
}

func (c *Client) readLegacySSE(r *bufio.Reader) {
	defer close(c.streamDone)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var response rpcResponse
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &response); err != nil || response.ID == 0 {
			continue
		}
		c.pendingMu.Lock()
		ch := c.pending[response.ID]
		delete(c.pending, response.ID)
		c.pendingMu.Unlock()
		if ch != nil {
			ch <- &response
		}
	}
}

func (c *Client) initialize(ctx context.Context) error {
	version := c.protocol
	if version == "" {
		version = protocolVersion
	}
	if _, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "artex", "version": "0.2"},
	}); err != nil {
		return err
	}
	return c.notify(ctx, "notifications/initialized", map[string]any{})
}

// Tools lists the server's tools and adapts them to CoreTools.
func (c *Client) Tools(ctx context.Context) ([]actool.CoreTool, error) {
	raw, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var res struct {
		Tools []remoteTool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	out := make([]actool.CoreTool, 0, len(res.Tools))
	for _, rt := range res.Tools {
		out = append(out, c.wrap(rt))
	}
	return out, nil
}

type remoteTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (c *Client) wrap(rt remoteTool) actool.CoreTool {
	schema := rt.InputSchema
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	full := ToolName(c.server, rt.Name)
	return actool.Build(actool.Spec{
		Name:        full,
		Description: rt.Description,
		Schema:      schema,
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.AskUser("call MCP tool " + full + "?")
		},
		Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			var args any
			if len(in) > 0 {
				_ = json.Unmarshal(in, &args)
			}
			raw, err := c.call(ctx, "tools/call", map[string]any{"name": rt.Name, "arguments": args})
			if err != nil {
				return actool.Errorf("Error: " + err.Error()), nil
			}
			var res struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			}
			if err := json.Unmarshal(raw, &res); err != nil {
				return actool.Errorf("Error: bad MCP response: " + err.Error()), nil
			}
			var text string
			for _, blk := range res.Content {
				text += blk.Text
			}
			// 与内置/自定义工具一致：超长输出走 Capture——按会话 MaxOutputChars（默认
			// 30000）截断，配了 ToolOutputDir 时全量溢写到磁盘只留 head + 指针，避免大
			// MCP 结果整段灌爆上下文。
			return actool.Result{Content: []llm.ContentBlock{llm.TextBlock(actool.Capture(tc, text))}, IsError: res.IsError}, nil
		},
	})
}

// Call invokes one tool and returns the concatenated text of its content blocks
// (ScopeSentry answers with a single JSON text block). Unlike the CoreTool wrapper
// from Tools(), this bypasses the agent permission (AskUser) layer — it is meant
// for backend batch jobs (e.g. asset sync) that call MCP tools programmatically.
func (c *Client) Call(ctx context.Context, tool string, args any) (string, error) {
	raw, err := c.call(ctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return "", err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("bad MCP response: %w", err)
	}
	var text strings.Builder
	for _, blk := range res.Content {
		text.WriteString(blk.Text)
	}
	if res.IsError {
		return text.String(), fmt.Errorf("mcp tool %q error: %s", tool, text.String())
	}
	return text.String(), nil
}

// Close terminates the MCP session best-effort (DELETE with the session id, per the
// Streamable HTTP spec). Servers that don't track sessions simply ignore it.
func (c *Client) Close() error {
	c.closeOnce.Do(func() { c.circuit().close(); c.closeTransport(); c.http.CloseIdleConnections() })
	return nil
}

func (c *Client) closeTransport() error {
	if c.legacySSE {
		if c.streamCancel != nil {
			c.streamCancel()
		}
		if c.streamBody != nil {
			_ = c.streamBody.Close()
		}
		select {
		case <-c.streamDone:
		case <-time.After(time.Second):
		}
		return nil
	}
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid == "" {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cleanupCtx, http.MethodDelete, c.url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Mcp-Session-Id", sid)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if resp, err := c.http.Do(req); err == nil {
		resp.Body.Close()
	}
	return nil
}

// --- JSON-RPC over Streamable HTTP ---

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()
	resp, err := c.roundTrip(ctx, rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}, true)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("mcp: empty response for %s", method)
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	_, err := c.roundTrip(ctx, rpcRequest{JSONRPC: "2.0", Method: method, Params: params}, false)
	return err
}

// roundTrip POSTs one JSON-RPC frame. When expectResp is false (a notification) the
// server replies 202 Accepted with no body. Otherwise the reply is parsed from JSON
// or from an SSE stream, whichever the server chose.
func (c *Client) roundTrip(ctx context.Context, body rpcRequest, expectResp bool) (*rpcResponse, error) {
	if _, err := json.Marshal(body); err != nil {
		return nil, errors.New("mcp: invalid request parameters")
	}
	g := c.circuit()
	a, err := g.acquire(ctx)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	stop := context.AfterFunc(g.ctx, cancel)
	defer func() { stop(); cancel() }()
	response, err := c.transportRoundTrip(callCtx, body, expectResp)
	if err == nil && expectResp && (response == nil || response.JSONRPC != "2.0" || response.ID != body.ID || (response.Error == nil && len(response.Result) == 0)) {
		err = errors.New("mcp: invalid RPC response")
	}
	canceled := ctx.Err() != nil || g.ctx.Err() != nil
	g.finish(a, err, canceled)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if g.ctx.Err() != nil {
		return nil, ErrClosed
	}
	if err != nil {
		return nil, errors.New("mcp: transport request failed")
	}
	return response, nil
}

func (c *Client) transportRoundTrip(ctx context.Context, body rpcRequest, expectResp bool) (*rpcResponse, error) {
	if c.legacySSE {
		return c.legacyRoundTrip(ctx, body, expectResp)
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	version := c.protocol
	if version == "" {
		version = protocolVersion
	}
	req.Header.Set("MCP-Protocol-Version", version)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Capture the session id the server assigns on initialize.
	if sid == "" {
		if got := resp.Header.Get("Mcp-Session-Id"); got != "" {
			c.mu.Lock()
			c.sessionID = got
			c.mu.Unlock()
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mcp http %d", resp.StatusCode)
	}
	if !expectResp {
		return nil, nil // notification — no JSON-RPC body to parse
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return parseSSE(resp.Body, body.ID)
	}
	var out rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("mcp: decode json response: %w", err)
	}
	return &out, nil
}

// legacyRoundTrip posts to the endpoint announced by GET /sse. The HTTP POST
// only acknowledges receipt; the JSON-RPC response arrives on the SSE stream.
func (c *Client) legacyRoundTrip(ctx context.Context, body rpcRequest, expectResp bool) (*rpcResponse, error) {

	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var responseCh chan *rpcResponse
	if expectResp {
		responseCh = make(chan *rpcResponse, 1)
		c.pendingMu.Lock()
		c.pending[body.ID] = responseCh
		c.pendingMu.Unlock()
		defer func() {
			c.pendingMu.Lock()
			delete(c.pending, body.ID)
			c.pendingMu.Unlock()
		}()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.messageURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mcp sse http %d", resp.StatusCode)
	}
	if !expectResp {
		return nil, nil
	}
	select {
	case response := <-responseCh:
		return response, nil
	case <-c.streamDone:
		return nil, errors.New("mcp sse stream closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// parseSSE reads an SSE stream and returns the first data frame that is a JSON-RPC
// response matching wantID (server-to-client requests/notifications are skipped).
func parseSSE(r io.Reader, wantID int) (*rpcResponse, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var dataBuf strings.Builder
	flush := func() (*rpcResponse, bool) {
		if dataBuf.Len() == 0 {
			return nil, false
		}
		payload := dataBuf.String()
		dataBuf.Reset()
		var out rpcResponse
		if err := json.Unmarshal([]byte(payload), &out); err != nil {
			return nil, false
		}
		if out.ID != wantID {
			return nil, false
		}
		return &out, true
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" { // event boundary
			if resp, ok := flush(); ok {
				return resp, nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataBuf.WriteString(strings.TrimSpace(line[len("data:"):]))
		}
	}
	if resp, ok := flush(); ok {
		return resp, nil
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("mcp: no matching response in event stream")
}
