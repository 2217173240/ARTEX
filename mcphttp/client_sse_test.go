package mcphttp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLegacySSEClientDiscoverAndCall(t *testing.T) {
	var mu sync.Mutex
	var stream http.ResponseWriter
	var flusher http.Flusher
	sessions := make(map[string]bool)
	messages := make(chan []byte, 8)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sse":
			id := "test-session"
			mu.Lock()
			sessions[id] = true
			stream, flusher = w, w.(http.Flusher)
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			fmt.Fprintf(w, "event: endpoint\ndata: %s/message?sessionId=%s\n\n", "http://"+r.Host, id)
			flusher.Flush()
			for {
				select {
				case payload := <-messages:
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
					flusher.Flush()
				case <-r.Context().Done():
					return
				}
			}
		case "/message":
			mu.Lock()
			knownSession := sessions["test-session"]
			mu.Unlock()
			if r.URL.Query().Get("sessionId") != "test-session" || !knownSession {
				http.Error(w, "unknown session", http.StatusBadRequest)
				return
			}
			var req rpcRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			if req.ID == 0 {
				return
			}
			var result any
			switch req.Method {
			case "initialize":
				result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "test", "version": "1"}}
			case "tools/list":
				result = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "echo input", "inputSchema": map[string]any{"type": "object"}}}}
			case "tools/call":
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}
			default:
				result = map[string]any{}
			}
			resultJSON, err := json.Marshal(result)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			payload, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(resultJSON)})
			mu.Lock()
			streamReady := stream != nil
			mu.Unlock()
			if streamReady {
				messages <- payload
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := NewSSE(ctx, "test", srv.URL+"/sse", map[string]string{"Authorization ": "Bearer test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tools, err := c.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || !strings.Contains(tools[0].Name(), "echo") {
		t.Fatalf("unexpected tools: %#v", tools)
	}
	got, err := c.Call(ctx, "echo", map[string]any{"value": "x"})
	if err != nil || got != "ok" {
		t.Fatalf("Call() = %q, %v", got, err)
	}
}

func TestReadLimitedLine(t *testing.T) {
	short := bufio.NewReaderSize(strings.NewReader("hello\nnext\n"), 4)
	got, err := readLimitedLine(short, 16)
	if err != nil || got != "hello\n" {
		t.Fatalf("short = %q, %v", got, err)
	}
	got, err = readLimitedLine(short, 16)
	if err != nil || got != "next\n" {
		t.Fatalf("second = %q, %v", got, err)
	}

	exact := strings.Repeat("a", 7) + "\n"
	got, err = readLimitedLine(bufio.NewReaderSize(strings.NewReader(exact), 3), 8)
	if err != nil || got != exact {
		t.Fatalf("exact = %q, %v", got, err)
	}

	over := strings.Repeat("b", 8) + "\n"
	if _, err = readLimitedLine(bufio.NewReaderSize(strings.NewReader(over), 3), 8); err == nil || !strings.Contains(err.Error(), "exceeds 8 bytes") {
		t.Fatalf("over: %v", err)
	}

	got, err = readLimitedLine(bufio.NewReader(strings.NewReader("tail")), 16)
	if got != "tail" || !errors.Is(err, io.EOF) {
		t.Fatalf("eof = %q, %v", got, err)
	}
}

func TestReadSSEEndpointRejectsLongLine(t *testing.T) {
	line := "data: " + strings.Repeat("a", mcpMaxRPCBytes) + "\n"
	if _, err := readSSEEndpoint(bufio.NewReader(strings.NewReader(line)), "https://mcp.example.com/sse"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("long line: %v", err)
	}
}
