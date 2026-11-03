package llm

import (
	"encoding/json"
	"strings"
)

// Accumulator reassembles a complete assistant Message from a stream of
// normalized StreamEvents. The harness feeds every event to Add; once the
// stream ends, Message returns the finished assistant turn (text, thinking, and
// tool_use blocks in arrival order).
type Accumulator struct {
	blocks            []ContentBlock
	toolInputs        []*strings.Builder
	toolIndexes       map[int]int
	pendingInputs     map[int]*strings.Builder
	pendingSignatures map[int]string
	textBuf           *strings.Builder
	thinkBuf          *strings.Builder
	thinkSig          string // signature for the current thinking block
	StopReason        string
	Usage             Usage
}

// NewAccumulator returns an empty Accumulator.
func NewAccumulator() *Accumulator { return &Accumulator{} }

// Add folds one event into the accumulator.
func (a *Accumulator) Add(ev StreamEvent) {
	switch ev.Type {
	case SEMessageStart:
		a.Usage.Add(ev.Usage)
	case SETextDelta:
		if a.textBuf == nil {
			a.flushThinking()
			a.textBuf = &strings.Builder{}
		}
		a.textBuf.WriteString(ev.Text)
	case SEThinkingDelta:
		if a.thinkBuf == nil {
			a.flushText()
			a.thinkBuf = &strings.Builder{}
		}
		a.thinkBuf.WriteString(ev.Text)
	case SEThinkingSignature:
		a.thinkSig = ev.Text
	case SEToolUseStart:
		a.flushText()
		a.flushThinking()
		a.blocks = append(a.blocks, ContentBlock{Type: BlockToolUse, ID: ev.ToolID, Name: ev.ToolName})
		a.toolInputs = append(a.toolInputs, &strings.Builder{})
		if ev.ToolIndex != nil {
			if a.toolIndexes == nil {
				a.toolIndexes = make(map[int]int)
			}
			a.toolIndexes[*ev.ToolIndex] = len(a.blocks) - 1
			if input := a.pendingInputs[*ev.ToolIndex]; input != nil {
				a.toolInputs[len(a.toolInputs)-1].WriteString(input.String())
				delete(a.pendingInputs, *ev.ToolIndex)
			}
			a.blocks[len(a.blocks)-1].ThoughtSignature = a.pendingSignatures[*ev.ToolIndex]
			delete(a.pendingSignatures, *ev.ToolIndex)
		}
	case SEToolInputJSON:
		if ev.ToolIndex != nil {
			if block, ok := a.toolIndexes[*ev.ToolIndex]; ok {
				a.toolInput(block).WriteString(ev.Text)
			} else {
				if a.pendingInputs == nil {
					a.pendingInputs = make(map[int]*strings.Builder)
				}
				if a.pendingInputs[*ev.ToolIndex] == nil {
					a.pendingInputs[*ev.ToolIndex] = &strings.Builder{}
				}
				a.pendingInputs[*ev.ToolIndex].WriteString(ev.Text)
			}
		} else if n := len(a.toolInputs); n > 0 {
			a.toolInputs[n-1].WriteString(ev.Text)
		}
	case SEToolUseSignature:
		if ev.ToolIndex != nil {
			if block, ok := a.toolIndexes[*ev.ToolIndex]; ok {
				a.blocks[block].ThoughtSignature = ev.Text
			} else {
				if a.pendingSignatures == nil {
					a.pendingSignatures = make(map[int]string)
				}
				a.pendingSignatures[*ev.ToolIndex] = ev.Text
			}
		} else {
			for i := len(a.blocks) - 1; i >= 0; i-- {
				if a.blocks[i].Type == BlockToolUse {
					a.blocks[i].ThoughtSignature = ev.Text
					break
				}
			}
		}
	case SEMessageDelta:
		if ev.StopReason != "" {
			a.StopReason = ev.StopReason
		}
		a.Usage.Add(ev.Usage)
	case SEMessageStop:
		a.flushText()
		a.flushThinking()
	}
}

func (a *Accumulator) toolInput(block int) *strings.Builder {
	tool := 0
	for i := 0; i < block; i++ {
		if a.blocks[i].Type == BlockToolUse {
			tool++
		}
	}
	return a.toolInputs[tool]
}

func (a *Accumulator) flushText() {
	if a.textBuf != nil {
		if s := a.textBuf.String(); s != "" {
			a.blocks = append(a.blocks, TextBlock(s))
		}
		a.textBuf = nil
	}
}

func (a *Accumulator) flushThinking() {
	if a.thinkBuf != nil {
		if s := a.thinkBuf.String(); s != "" {
			a.blocks = append(a.blocks, ContentBlock{
				Type:      BlockThinking,
				Thinking:  s,
				Signature: a.thinkSig,
			})
		}
		a.thinkBuf = nil
		a.thinkSig = ""
	}
}

// Message finalizes and returns the assembled assistant message. Partial tool
// inputs are validated; malformed JSON falls back to "{}".
func (a *Accumulator) Message() Message {
	a.flushText()
	a.flushThinking()
	ti := 0
	out := make([]ContentBlock, len(a.blocks))
	for i, b := range a.blocks {
		if b.Type == BlockToolUse {
			raw := strings.TrimSpace(a.toolInputs[ti].String())
			ti++
			if raw == "" || !json.Valid([]byte(raw)) {
				raw = "{}"
			}
			b.Input = json.RawMessage(raw)
		}
		out[i] = b
	}
	return Message{Role: RoleAssistant, Content: out}
}
