package agent

import (
	"context"
	"fmt"
	"iter"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"
)

const defaultCallTimeout = 600 * time.Second

// callTimeoutFromEnv bounds a logical provider call, including SDK retries and
// streaming. Zero disables it. Overflow and malformed values use the default.
func callTimeoutFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("ARTEX_LLM_CALL_TIMEOUT"))
	if raw == "" {
		return defaultCallTimeout
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64/int64(time.Second) {
		log.Printf("invalid ARTEX_LLM_CALL_TIMEOUT; using %s", defaultCallTimeout)
		return defaultCallTimeout
	}
	return time.Duration(n) * time.Second
}

// CallTimeoutError marks a deadline owned by the provider wrapper, rather than
// the caller. Pool failover recognizes the marker without depending on agent.
type CallTimeoutError struct {
	Provider, Model string
	Timeout         time.Duration
}

func (e *CallTimeoutError) Error() string {
	return fmt.Sprintf("llm call_timeout: %s/%s exceeded %s", e.Provider, e.Model, e.Timeout)
}
func (e *CallTimeoutError) Unwrap() error        { return context.DeadlineExceeded }
func (e *CallTimeoutError) LLMCallTimeout() bool { return true }

type callTimeoutProvider struct {
	inner           llm.Provider
	timeout         time.Duration
	provider, model string
}

func wrapCallTimeout(inner llm.Provider, c Config) llm.Provider {
	d := callTimeoutFromEnv()
	if inner == nil || d == 0 {
		return inner
	}
	return &callTimeoutProvider{inner: inner, timeout: d, provider: c.Provider(), model: c.Model}
}
func (p *callTimeoutProvider) deadlineError(parent, child context.Context, err error) error {
	if parent.Err() != nil {
		if err == nil {
			return parent.Err()
		}
		return err
	}
	if child.Err() == context.DeadlineExceeded {
		return &CallTimeoutError{Provider: p.provider, Model: p.model, Timeout: p.timeout}
	}
	return err
}

// Cancellation is cooperative: no detached goroutines are created to abandon
// a provider that ignores its context.
func (p *callTimeoutProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		child, cancel := context.WithTimeout(ctx, p.timeout)
		defer cancel()
		for ev, err := range p.inner.Stream(child, req) {
			if final := p.deadlineError(ctx, child, err); final != nil {
				if _, owned := final.(*CallTimeoutError); !owned {
					yield(ev, final)
				} else {
					yield(llm.StreamEvent{}, final)
				}
				return
			}
			if !yield(ev, nil) {
				return
			}
		}
		if err := p.deadlineError(ctx, child, nil); err != nil {
			yield(llm.StreamEvent{}, err)
		}
	}
}
func (p *callTimeoutProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	child, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	msg, stop, usage, err := p.inner.Complete(child, req)
	if final := p.deadlineError(ctx, child, err); final != nil {
		if _, owned := final.(*CallTimeoutError); !owned {
			return msg, stop, usage, final
		}
		return llm.Message{}, "", llm.Usage{}, final
	}
	return msg, stop, usage, nil
}
