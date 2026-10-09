package agent

import (
	"context"
	"errors"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
)

func TestProviderCallTimeoutStalledHTTP(t *testing.T) {
	t.Setenv("ARTEX_LLM_CALL_TIMEOUT", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	p, err := ConfigFrom("anthropic", "test", srv.URL, "fake", "").NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	var got error
	for _, err := range p.Stream(ctx, llm.CompletionRequest{}) {
		if err != nil {
			got = err
		}
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("call lacked independent timeout: %v (%v)", elapsed, got)
	}
	if got == nil || ctx.Err() != nil {
		t.Fatalf("expected child timeout with live parent: %v", got)
	}
}

// A cooperative provider lets tests exercise cancellation without background workers.
type timeoutFake struct {
	stream func(context.Context, func(llm.StreamEvent, error) bool)
}

func (f timeoutFake) Stream(ctx context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(y func(llm.StreamEvent, error) bool) { f.stream(ctx, y) }
}
func (f timeoutFake) Complete(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	<-ctx.Done()
	return llm.Message{}, "", llm.Usage{}, ctx.Err()
}

func TestCallTimeoutCooperativePaths(t *testing.T) {
	for _, silent := range []bool{false, true} {
		f := timeoutFake{stream: func(ctx context.Context, y func(llm.StreamEvent, error) bool) {
			<-ctx.Done()
			if !silent {
				y(llm.StreamEvent{}, ctx.Err())
			}
		}}
		p := &callTimeoutProvider{inner: f, timeout: 10 * time.Millisecond}
		var got error
		for _, err := range p.Stream(context.Background(), llm.CompletionRequest{}) {
			got = err
		}
		var marker *CallTimeoutError
		if !errors.As(got, &marker) || !errors.Is(got, context.DeadlineExceeded) {
			t.Fatalf("silent=%v: %v", silent, got)
		}
		_, _, _, got = p.Complete(context.Background(), llm.CompletionRequest{})
		if !errors.As(got, &marker) {
			t.Fatalf("Complete: %v", got)
		}
	}
	for _, deadline := range []bool{false, true} {
		var ctx context.Context
		var cancel context.CancelFunc
		if deadline {
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		} else {
			ctx, cancel = context.WithCancel(context.Background())
			cancel()
		}
		f := timeoutFake{stream: func(ctx context.Context, y func(llm.StreamEvent, error) bool) {
			<-ctx.Done()
			y(llm.StreamEvent{}, ctx.Err())
		}}
		p := &callTimeoutProvider{inner: f, timeout: time.Second}
		var got error
		for _, err := range p.Stream(ctx, llm.CompletionRequest{}) {
			got = err
		}
		cancel()
		var marker *CallTimeoutError
		if errors.As(got, &marker) || !errors.Is(got, ctx.Err()) {
			t.Fatalf("parent cancellation: %v", got)
		}
	}
}

func TestCallTimeoutConsumerStopAndSuccess(t *testing.T) {
	var child context.Context
	f := timeoutFake{stream: func(ctx context.Context, y func(llm.StreamEvent, error) bool) {
		child = ctx
		if !y(llm.StreamEvent{Type: llm.SETextDelta, Text: "hello", Usage: llm.Usage{InputTokens: 3, OutputTokens: 2}}, nil) {
			return
		}
		y(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}}
	p := &callTimeoutProvider{inner: f, timeout: time.Second}
	for range p.Stream(context.Background(), llm.CompletionRequest{}) {
		break
	}
	if child.Err() != context.Canceled {
		t.Fatal("consumer stop failed to cancel child")
	}
	var text string
	var count int
	var usage llm.Usage
	for ev, err := range p.Stream(context.Background(), llm.CompletionRequest{}) {
		if err != nil {
			t.Fatal(err)
		}
		text += ev.Text
		if ev.Type == llm.SETextDelta {
			usage = ev.Usage
		}
		count++
	}
	if text != "hello" || count != 2 || usage.InputTokens != 3 || usage.OutputTokens != 2 {
		t.Fatalf("normal stream changed: %q %d", text, count)
	}
}
func TestCallTimeoutEnvironment(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{{"", defaultCallTimeout}, {"0", 0}, {" 2 ", 2 * time.Second}, {"-1", defaultCallTimeout}, {"garbage", defaultCallTimeout}, {"9223372037", defaultCallTimeout}, {"99999999999999999999999", defaultCallTimeout}} {
		t.Setenv("ARTEX_LLM_CALL_TIMEOUT", tc.raw)
		if got := callTimeoutFromEnv(); got != tc.want {
			t.Fatalf("%q: %v", tc.raw, got)
		}
	}
	t.Setenv("ARTEX_LLM_CALL_TIMEOUT", "0")
	f := timeoutFake{}
	if _, wrapped := wrapCallTimeout(f, Config{}).(*callTimeoutProvider); wrapped {
		t.Fatal("zero should disable")
	}
	if wrapCallTimeout(nil, Config{}) != nil {
		t.Fatal("nil should remain nil")
	}
}
