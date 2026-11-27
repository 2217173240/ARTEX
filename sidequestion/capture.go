// Package sidequestion captures immutable main-agent checkpoints. It never owns
// an agent session or a tool executor.
package sidequestion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

type Parent struct {
	ConversationID int64 `json:"conversation_id,omitempty"`
	TaskID         int64 `json:"task_id,omitempty"`
	ExplorationID  int64 `json:"exploration_id,omitempty"`
	IntentID       int64 `json:"intent_id,omitempty"`
}

func (p Parent) Key() string {
	if p.ConversationID > 0 {
		return fmt.Sprintf("conv-%d", p.ConversationID)
	}
	if p.IntentID > 0 {
		return fmt.Sprintf("task-%d-exp-%d-worker-i%d", p.TaskID, p.ExplorationID, p.IntentID)
	}
	return fmt.Sprintf("task-%d-exp-%d-main", p.TaskID, p.ExplorationID)
}

// Model contains only configuration identity, never credentials or proxy URLs.
type Model struct {
	ProfileID    int64  `json:"profile_id"`
	Name         string `json:"name"`
	Format       string `json:"format"`
	Model        string `json:"model"`
	Identity     string `json:"identity"`
	Streaming    bool   `json:"streaming"`
	WindowTokens int    `json:"window_tokens"`
}

type Snapshot struct {
	Parent     Parent                `json:"parent"`
	RunID      int64                 `json:"run_id"`
	Version    int64                 `json:"version"`
	CapturedAt time.Time             `json:"captured_at"`
	Model      Model                 `json:"model"`
	Request    llm.CompletionRequest `json:"request"`
}

func CloneRequest(req llm.CompletionRequest) (llm.CompletionRequest, error) {
	out := llm.CompletionRequest{
		System:          slices.Clone(req.System),
		DynamicBoundary: req.DynamicBoundary,
		Messages:        cloneMessages(req.Messages),
		Tools:           cloneTools(req.Tools),
		MaxTokens:       req.MaxTokens,
		Stop:            slices.Clone(req.Stop),
		Thinking:        req.Thinking,
	}
	if req.Temperature != nil {
		temp := *req.Temperature
		out.Temperature = &temp
	}
	return out, nil
}

func cloneMessages(msgs []llm.Message) []llm.Message {
	if msgs == nil {
		return nil
	}
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		out[i] = llm.Message{
			Role:    m.Role,
			Content: cloneContentBlocks(m.Content),
		}
	}
	return out
}

func cloneContentBlocks(blocks []llm.ContentBlock) []llm.ContentBlock {
	if blocks == nil {
		return nil
	}
	out := make([]llm.ContentBlock, len(blocks))
	for i, b := range blocks {
		b.Input = json.RawMessage(bytes.Clone(b.Input))
		b.Content = cloneContentBlocks(b.Content)
		out[i] = b
	}
	return out
}

func cloneTools(tools []llm.ToolSchema) []llm.ToolSchema {
	if tools == nil {
		return nil
	}
	out := make([]llm.ToolSchema, len(tools))
	for i, t := range tools {
		t.InputSchema = cloneStringAnyMap(t.InputSchema)
		out[i] = t
	}
	return out
}

func cloneStringAnyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneAny(v)
	}
	return out
}

func cloneAny(v any) any {
	switch x := v.(type) {
	case nil, string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, json.Number:
		return x
	case map[string]any:
		return cloneStringAnyMap(x)
	case []any:
		if x == nil {
			return nil
		}
		out := make([]any, len(x))
		for i, elem := range x {
			out[i] = cloneAny(elem)
		}
		return out
	case []string:
		return slices.Clone(x)
	case map[string]string:
		return maps.Clone(x)
	case []byte:
		return bytes.Clone(x)
	case json.RawMessage:
		return json.RawMessage(bytes.Clone(x))
	default:
		return cloneReflectValue(reflect.ValueOf(v)).Interface()
	}
}

func cloneReflectValue(rv reflect.Value) reflect.Value {
	if !rv.IsValid() {
		return rv
	}
	switch rv.Kind() {
	case reflect.Interface:
		if rv.IsNil() {
			return rv
		}
		cloned := cloneReflectValue(rv.Elem())
		out := reflect.New(rv.Type()).Elem()
		out.Set(cloned)
		return out
	case reflect.Pointer:
		if rv.IsNil() {
			return rv
		}
		out := reflect.New(rv.Type().Elem())
		out.Elem().Set(cloneReflectValue(rv.Elem()))
		return out
	case reflect.Slice:
		if rv.IsNil() {
			return rv
		}
		n := rv.Len()
		out := reflect.MakeSlice(rv.Type(), n, n)
		for i := 0; i < n; i++ {
			out.Index(i).Set(cloneReflectValue(rv.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(rv.Type()).Elem()
		for i := 0; i < rv.Len(); i++ {
			out.Index(i).Set(cloneReflectValue(rv.Index(i)))
		}
		return out
	case reflect.Map:
		if rv.IsNil() {
			return rv
		}
		out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out.SetMapIndex(cloneReflectValue(iter.Key()), cloneReflectValue(iter.Value()))
		}
		return out
	case reflect.Struct:
		out := reflect.New(rv.Type()).Elem()
		out.Set(rv)
		for i := 0; i < rv.NumField(); i++ {
			if out.Field(i).CanSet() {
				out.Field(i).Set(cloneReflectValue(rv.Field(i)))
			}
		}
		return out
	default:
		return rv
	}
}

type Publisher func(Snapshot)
type publisherKey struct{}
type captureKey struct{}
type attemptKey struct{}

func WithPublisher(ctx context.Context, publish Publisher) context.Context {
	return context.WithValue(ctx, publisherKey{}, publish)
}

type Capture struct {
	mu      sync.Mutex
	parent  Parent
	runID   int64
	version int64
	publish Publisher
	last    *Snapshot
}

// Attach uses QueryDeps rather than a global provider hook so compaction and
// other auxiliary completions cannot replace the main conversation checkpoint.
func Attach(ctx context.Context, parent Parent, deps harness.QueryDeps, provider llm.Provider) (context.Context, harness.QueryDeps) {
	publish, _ := ctx.Value(publisherKey{}).(Publisher)
	if publish == nil {
		return ctx, deps
	}
	c := &Capture{parent: parent, runID: time.Now().UnixNano(), publish: publish}
	stream, complete := deps.CallModel, deps.CallModelSync
	if stream == nil {
		stream = provider.Stream
	}
	if complete == nil {
		complete = provider.Complete
	}
	deps.CallModel = func(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
		return stream(context.WithValue(ctx, attemptKey{}, c), req)
	}
	deps.CallModelSync = func(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		return complete(context.WithValue(ctx, attemptKey{}, c), req)
	}
	return context.WithValue(ctx, captureKey{}, c), deps
}

func (c *Capture) save(req llm.CompletionRequest, model Model) {
	copy, err := CloneRequest(req)
	if err != nil {
		return
	} // Observability must not break the main run.
	c.mu.Lock()
	c.version++
	s := Snapshot{Parent: c.parent, RunID: c.runID, Version: c.version, CapturedAt: time.Now().UTC(), Model: model, Request: copy}
	c.last = &s
	c.mu.Unlock()
	c.publish(s)
}

// Finish adds tool results that landed after the final model request. Norma's
// first API message is its host reminder, absent from Terminal.Messages.
func Finish(ctx context.Context, messages []llm.Message) {
	c, _ := ctx.Value(captureKey{}).(*Capture)
	if c == nil {
		return
	}
	c.mu.Lock()
	s := c.last
	c.mu.Unlock()
	if s == nil {
		return
	}
	req := s.Request
	var prefix []llm.Message
	if len(req.Messages) > 0 && strings.HasPrefix(req.Messages[0].Text(), "<system-reminder>") {
		prefix = append(prefix, req.Messages[0])
	}
	req.Messages = append(prefix, llm.MessagesForAPI(messages)...)
	c.save(req, s.Model)
}

type boundProvider struct {
	inner llm.Provider
	model Model
}

// Bind belongs immediately around a concrete provider, inside any routing pool.
func Bind(inner llm.Provider, model Model) llm.Provider { return &boundProvider{inner, model} }

func (p *boundProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		c, _ := ctx.Value(attemptKey{}).(*Capture)
		if c == nil {
			for ev, err := range p.inner.Stream(ctx, req) {
				if !yield(ev, err) {
					return
				}
			}
			return
		}
		c.save(req, p.model)
		acc := llm.NewAccumulator()
		failed := false
		complete := false
		for ev, err := range p.inner.Stream(ctx, req) {
			if err != nil {
				failed = true
			} else {
				acc.Add(ev)
				if ev.Type == llm.SEMessageStop {
					complete = true
				}
			}
			if !yield(ev, err) {
				return
			}
		}
		if !failed && complete && ctx.Err() == nil {
			msg := acc.Message()
			req.Messages = llm.MessagesForAPI(append(append([]llm.Message{}, req.Messages...), msg))
			c.save(req, p.model)
		}
	}
}

func (p *boundProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	c, _ := ctx.Value(attemptKey{}).(*Capture)
	if c != nil {
		c.save(req, p.model)
	}
	msg, stop, usage, err := p.inner.Complete(ctx, req)
	if c != nil && err == nil && ctx.Err() == nil {
		req.Messages = llm.MessagesForAPI(append(append([]llm.Message{}, req.Messages...), msg))
		c.save(req, p.model)
	}
	return msg, stop, usage, err
}
