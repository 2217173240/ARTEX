package mcphttp

import (
	"encoding/json"
	"testing"
)

func TestNegotiatedProtocol(t *testing.T) {
	for _, tc := range []struct {
		input  string
		legacy bool
		want   string
	}{
		{`{"protocolVersion":"2025-06-18"}`, false, "2025-06-18"},
		{`{"protocolVersion":"2025-03-26"}`, false, "2025-03-26"},
		{`{"protocolVersion":"2024-11-05"}`, true, "2024-11-05"},
		{`{"protocolVersion":"2025-06-18"}`, true, ""},
		{`{"protocolVersion":"2024-11-05"}`, false, ""},
		{`{"protocolVersion":"2025-11-25"}`, false, ""},
		{`{"protocolVersion":123}`, false, ""},
		{`{"protocolVersion":null}`, false, ""},
		{`{"protocolVersion":""}`, false, ""},
		{`{}`, false, ""},
		{`null`, false, ""},
		{`[]`, false, ""},
		{`{`, false, ""},
		{``, false, ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := negotiatedProtocol(json.RawMessage(tc.input), tc.legacy)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
