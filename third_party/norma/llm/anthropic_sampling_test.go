package llm

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestAnthropicSamplingBody(t *testing.T) {
	models := []struct {
		name string
		omit bool
	}{
		{"claude-opus-4-7", true},
		{"claude-opus-4-8", true},
		{"claude-opus-4-6", false},
		{"claude-sonnet-4-6", false},
		{"unknown-model", false},
		{"claude-opus-4-9", false},
		{"claude-opus-4-8-custom", false},
	}
	thinkingModes := []struct {
		name, profile, request, want string
	}{
		{"unset", "", "", ""},
		{"profile_disabled", "disabled", "", "disabled"},
		{"request_disabled", "enabled", "disabled", "disabled"},
		{"request_enabled", "disabled", "enabled", "enabled"},
	}
	for _, model := range models {
		for _, mode := range thinkingModes {
			for _, stream := range []bool{false, true} {
				for _, temperature := range []float64{0, 0.7} {
					t.Run(fmt.Sprintf("%s/%s/stream=%t/temperature=%g", model.name, mode.name, stream, temperature), func(t *testing.T) {
						cfg := Config{Model: model.name, ThinkingType: mode.profile, ReasoningEffort: "high"}
						p := anthropicProvider{cfg: cfg}
						req := CompletionRequest{Temperature: &temperature, Thinking: mode.request}
						before := req
						wantTemperature := temperature
						raw, err := p.buildBody(req, stream)
						if err != nil {
							t.Fatal(err)
						}
						var body map[string]json.RawMessage
						if err := json.Unmarshal(raw, &body); err != nil {
							t.Fatal(err)
						}
						value, present := body["temperature"]
						if present == model.omit {
							t.Errorf("temperature present=%t, want %t: %s", present, !model.omit, raw)
						}
						if present {
							var got float64
							if err := json.Unmarshal(value, &got); err != nil || got != temperature {
								t.Errorf("temperature=%s, want %g (error=%v)", value, temperature, err)
							}
						}
						var got struct {
							Model        string                 `json:"model"`
							Stream       bool                   `json:"stream"`
							Thinking     *anthropicThinking     `json:"thinking"`
							OutputConfig *anthropicOutputConfig `json:"output_config"`
						}
						if err := json.Unmarshal(raw, &got); err != nil {
							t.Fatal(err)
						}
						if got.Model != model.name || got.Stream != stream {
							t.Errorf("model/stream changed: %s", raw)
						}
						if mode.want == "" {
							if _, present := body["thinking"]; present {
								t.Errorf("unexpected thinking: %s", raw)
							}
						} else if got.Thinking == nil || got.Thinking.Type != mode.want {
							t.Errorf("thinking=%+v, want %s", got.Thinking, mode.want)
						}
						if got.OutputConfig == nil || got.OutputConfig.Effort != "high" {
							t.Errorf("reasoning effort changed: %s", raw)
						}
						if !reflect.DeepEqual(req, before) || req.Temperature != before.Temperature || *req.Temperature != wantTemperature {
							t.Error("buildBody changed the original request")
						}
						if !reflect.DeepEqual(p.cfg, cfg) {
							t.Error("buildBody changed the provider configuration")
						}
					})
				}
			}
		}
	}
}
