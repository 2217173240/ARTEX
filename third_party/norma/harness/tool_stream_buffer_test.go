package harness

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"
)

func TestToolStreamBufferIndexedCompletion(t *testing.T) {
	zero, one := 0, 1
	events := []llm.StreamEvent{
		{Type: llm.SEToolUseSignature, ToolIndex: &zero, Text: "zero-signature"},
		{Type: llm.SEToolInputJSON, ToolIndex: &zero, Text: `{"zero":`},
		{Type: llm.SEToolUseStart, ToolIndex: &one, ToolID: "first", ToolName: "one"},
		{Type: llm.SEToolInputJSON, ToolIndex: &one, Text: `{"one":1}`},
		{Type: llm.SEToolUseStart, ToolIndex: &zero, ToolID: "second", ToolName: "zero"},
		{Type: llm.SETextDelta, Text: "interleaved"},
		{Type: llm.SEToolInputJSON, ToolIndex: &zero, Text: `0}`},
		{Type: llm.SEMessageStop},
		{Type: llm.SEToolUseSignature, ToolIndex: &one, Text: "late-one-signature"},
	}
	b := newToolStreamBuffer()
	for _, event := range events {
		if got := b.add(event); got != nil {
			t.Fatalf("indexed call dispatched before successful EOF: %+v", got)
		}
	}
	m, ready, diagnostic := b.finish(true)
	want := []llm.ContentBlock{
		{Type: llm.BlockToolUse, ID: "first", Name: "one", Input: []byte(`{"one":1}`), ThoughtSignature: "late-one-signature"},
		{Type: llm.BlockToolUse, ID: "second", Name: "zero", Input: []byte(`{"zero":0}`), ThoughtSignature: "zero-signature"},
	}
	if diagnostic != nil || !reflect.DeepEqual(ready, want) || !reflect.DeepEqual(m.ToolUses(), want) || m.Text() != "interleaved" {
		t.Fatalf("message=%+v ready=%+v diagnostic=%+v", m, ready, diagnostic)
	}
}

func TestToolStreamBufferInterruptedIndexedCalls(t *testing.T) {
	zero, one := 0, 1
	for _, input := range []string{"", `{"partial`, `{"complete":true}`} {
		t.Run(input, func(t *testing.T) {
			b := newToolStreamBuffer()
			events := []llm.StreamEvent{
				{Type: llm.SEToolUseStart, ToolID: "same", ToolName: "legacy"},
				{Type: llm.SEToolInputJSON, Text: `{}`},
				{Type: llm.SEToolUseStart, ToolIndex: &zero, ToolID: "same", ToolName: "zero"},
				{Type: llm.SEToolInputJSON, ToolIndex: &zero, Text: `{"zero":0}`},
				{Type: llm.SEToolUseStart, ToolIndex: &one, ToolID: "same", ToolName: "one"},
				{Type: llm.SEToolInputJSON, ToolIndex: &one, Text: input},
				{Type: llm.SEToolUseSignature, ToolIndex: &one, Text: "diagnostic-signature"},
				{Type: llm.SETextDelta, Text: "retained"},
			}
			var dispatched []llm.ContentBlock
			for _, event := range events {
				if ready := b.add(event); ready != nil {
					dispatched = append(dispatched, *ready)
				}
			}
			m, ready, diagnostic := b.finish(false)
			if len(dispatched) != 1 || dispatched[0].Name != "legacy" || len(ready) != 0 || !reflect.DeepEqual(m.ToolUses(), dispatched) || m.Text() != "retained" {
				t.Fatalf("unfinished calls escaped: message=%+v dispatched=%+v ready=%+v", m, dispatched, ready)
			}
			if diagnostic == nil || diagnostic.Name != "one" || string(diagnostic.Input) != input || diagnostic.ThoughtSignature != "diagnostic-signature" {
				t.Fatalf("diagnostic=%+v", diagnostic)
			}
		})
	}
}

func TestToolStreamBufferLegacySignatureBoundary(t *testing.T) {
	b := newToolStreamBuffer()
	for _, event := range []llm.StreamEvent{
		{Type: llm.SEToolUseStart, ToolID: "legacy", ToolName: "probe"},
		{Type: llm.SEToolInputJSON, Text: `{"value":`},
		{Type: llm.SEToolUseSignature, Text: "signature"},
		{Type: llm.SEThinkingSignature, Text: "thinking-signature"},
		{Type: llm.SEToolInputJSON, Text: `1}`},
	} {
		if ready := b.add(event); ready != nil {
			t.Fatalf("signature/input closed call: %+v", ready)
		}
	}
	ready := b.add(llm.StreamEvent{Type: llm.SETextDelta, Text: "after"})
	if ready == nil || string(ready.Input) != `{"value":1}` || ready.ThoughtSignature != "signature" {
		t.Fatalf("legacy boundary block=%+v", ready)
	}
	m, final, diagnostic := b.finish(false)
	if len(final) != 0 || diagnostic != nil || !reflect.DeepEqual(m.ToolUses(), []llm.ContentBlock{*ready}) {
		t.Fatalf("completed legacy call lost: %+v %+v %+v", m, final, diagnostic)
	}
}

func TestToolStreamBufferCompletionControlsInputRelease(t *testing.T) {
	zero := 0
	for _, index := range []*int{nil, &zero} {
		for _, input := range []string{"", `{"unfinished`, `{"ready":true}`} {
			for _, success := range []bool{false, true} {
				b := newToolStreamBuffer()
				for _, event := range []llm.StreamEvent{
					{Type: llm.SEToolUseStart, ToolIndex: index, ToolID: "pending", ToolName: "probe"},
					{Type: llm.SEToolInputJSON, ToolIndex: index, Text: input},
					{Type: llm.SEToolUseSignature, ToolIndex: index, Text: "signature"},
				} {
					if block := b.add(event); block != nil {
						t.Fatalf("input validity completed a call: %+v", block)
					}
				}
				m, ready, diagnostic := b.finish(success)
				if !success {
					if len(m.ToolUses()) != 0 || len(ready) != 0 || diagnostic == nil || string(diagnostic.Input) != input || diagnostic.ThoughtSignature != "signature" {
						t.Fatalf("interrupted state: %+v %+v %+v", m, ready, diagnostic)
					}
					continue
				}
				wantInput := input
				if input != `{"ready":true}` {
					wantInput = `{}`
				}
				if diagnostic != nil || len(ready) != 1 || !reflect.DeepEqual(m.ToolUses(), ready) || string(ready[0].Input) != wantInput || ready[0].ThoughtSignature != "signature" {
					t.Fatalf("successful state: %+v %+v %+v", m, ready, diagnostic)
				}
			}
		}
	}
}

func TestToolStreamBufferPreservesMalformedDispatchInput(t *testing.T) {
	zero := 0
	for _, index := range []*int{nil, &zero} {
		for _, input := range []string{`{"x":}`, `oops`, ` {"x":} `} {
			b := newToolStreamBuffer()
			for _, event := range []llm.StreamEvent{
				{Type: llm.SEToolUseStart, ToolIndex: index, ToolID: "invalid", ToolName: "probe"},
				{Type: llm.SEToolInputJSON, ToolIndex: index, Text: input},
				{Type: llm.SEToolUseSignature, ToolIndex: index, Text: "signature"},
			} {
				if ready := b.add(event); ready != nil {
					t.Fatalf("input prematurely dispatched: %+v", ready)
				}
			}
			var dispatched []llm.ContentBlock
			if index == nil {
				block := b.add(llm.StreamEvent{Type: llm.SETextDelta, Text: "boundary"})
				if block == nil {
					t.Fatal("legacy boundary did not release input")
				}
				dispatched = append(dispatched, *block)
			}
			m, ready, diagnostic := b.finish(true)
			dispatched = append(dispatched, ready...)
			if diagnostic != nil || len(dispatched) != 1 || string(dispatched[0].Input) != strings.TrimSpace(input) || dispatched[0].ThoughtSignature != "signature" {
				t.Fatalf("dispatch input changed: %+v diagnostic=%+v", dispatched, diagnostic)
			}
			// Use the same validation as execOne, with a schema that permits {}.
			// Malformed input must reach validation and produce its error result.
			schema := map[string]any{"type": "object"}
			if err := tool.ValidateInput(schema, []byte(`{}`)); err != nil {
				t.Fatalf("schema rejects empty object: %v", err)
			}
			if err := tool.ValidateInput(schema, dispatched[0].Input); err == nil {
				t.Fatalf("malformed dispatch bypassed validation: %s", dispatched[0].Input)
			}
			if uses := m.ToolUses(); len(uses) != 1 || string(uses[0].Input) != `{}` {
				t.Fatalf("replay normalization changed: %+v", uses)
			}
		}
	}
}
