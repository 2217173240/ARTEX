package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTelegramPrivacyBriefRendering(t *testing.T) {
	it := Item{FindingID: 42, Name: "Example finding", VulnClass: "Example class", Summary: "Example summary", Assets: []string{"example.org"}, DetailURL: "https://example.org/finding/42", Severity: " HIGH ", FromStatus: "pending", ToStatus: "resolved"}
	for _, batch := range []bool{false, true} {
		m := Message{Items: []Item{it}, Batch: batch, HomeURL: "https://example.org", WindowMinutes: 10}
		text, kept, err := telegramConfiguredHTML(map[string]any{"privacy_brief": true}, m)
		if err != nil || kept != 1 || text != "<b>Privacy brief · 1</b>\nFindingID 42 · high" {
			t.Fatalf("batch=%v: text=%q kept=%d err=%v", batch, text, kept, err)
		}
		for _, cfg := range []map[string]any{nil, {"privacy_brief": false}} {
			got, count, err := telegramConfiguredHTML(cfg, m)
			want, wantCount := telegramHTML(m)
			if err != nil || got != want || count != wantCount {
				t.Fatalf("legacy render changed: %q, %d, %v", got, count, err)
			}
		}
	}
}

func TestTelegramPrivacyBriefSeverityAndTestMessage(t *testing.T) {
	for _, severity := range []string{"critical", " high ", "MEDIUM", "low", "unclassified", ""} {
		text, kept, err := telegramConfiguredHTML(map[string]any{"privacy_brief": true}, Message{Items: []Item{{FindingID: 0, Name: "Test message", Summary: "Example test content", Severity: severity}}})
		want := strings.ToLower(strings.TrimSpace(severity))
		if SeverityRank(want) == 0 {
			want = "unknown"
		}
		if err != nil || kept != 1 || text != "<b>Privacy brief · 1</b>\n"+want {
			t.Fatalf("severity %q: %q %d %v", severity, text, kept, err)
		}
	}
}

func TestTelegramPrivacyBriefConfigRoundTripAndInvalidSend(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		stored := map[string]any{"privacy_brief": enabled, "bot_token": "local-unused", "chat_id": "local-unused"}
		raw, err := json.Marshal(stored)
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		masked := MaskConfig(KindTelegram, cfg)
		merged, err := PrepareConfigUpdate(KindTelegram, cfg, masked)
		if err != nil || merged["privacy_brief"] != enabled {
			t.Fatalf("config roundtrip: %v", err)
		}
		if err := (telegramChannel{}).Validate(merged); err != nil {
			t.Fatal(err)
		}
	}
	for _, invalid := range []any{nil, "true", "false", 1, []any{}, map[string]any{}} {
		cfg := map[string]any{"privacy_brief": invalid}
		if err := (telegramChannel{}).Validate(cfg); err == nil {
			t.Fatalf("accepted %T", invalid)
		}
		text, kept, err := telegramConfiguredHTML(cfg, Message{})
		if err == nil || text != "" || kept != 0 {
			t.Fatalf("invalid configuration rendered text")
		}
		// 无凭据和端点；验证失败必须在任何 HTTP 操作前返回。
		if n, err := (telegramChannel{}).Send(context.Background(), cfg, Message{}); err == nil || n != 0 {
			t.Fatalf("invalid configuration reached send: %d %v", n, err)
		}
	}
}

func TestTelegramPrivacyBriefPacking(t *testing.T) {
	m := Message{Batch: true}
	for i := 1; i <= 500; i++ {
		m.Items = append(m.Items, Item{FindingID: int64(i), Severity: "critical"})
	}
	total := 0
	for len(m.Items) > 0 {
		text, kept, err := telegramConfiguredHTML(map[string]any{"privacy_brief": true}, m)
		if err != nil || kept < 1 || utf8.RuneCountInString(text) > telegramTextLimit {
			t.Fatalf("packing %d: %v", kept, err)
		}
		lines := strings.Split(text, "\n")
		if len(lines) != kept+1 || lines[0] != fmt.Sprintf("<b>Privacy brief · %d</b>", kept) {
			t.Fatalf("incorrect delivery count")
		}
		for i, it := range m.Items[:kept] {
			if lines[i+1] != fmt.Sprintf("FindingID %d · critical", it.FindingID) {
				t.Fatalf("missing item %d", it.FindingID)
			}
		}
		total += kept
		m.Items = m.Items[kept:]
	}
	if total != 500 {
		t.Fatalf("delivered %d", total)
	}
}

func TestTelegramPrivacyBriefUpdateRejectsInvalidBeforeMerge(t *testing.T) {
	stored := map[string]any{"privacy_brief": true, "bot_token": "local-unused", "chat_id": "local-unused"}
	for _, invalid := range []any{"", "  ", "__masked__:value", "true", "false", nil, 0, []any{}, map[string]any{}} {
		merged, err := PrepareConfigUpdate(KindTelegram, stored, map[string]any{"privacy_brief": invalid})
		if err == nil || merged != nil {
			t.Fatalf("accepted invalid update %T %v", invalid, invalid)
		}
		if stored["privacy_brief"] != true {
			t.Fatal("stored option changed")
		}
	}
	for _, incoming := range []map[string]any{nil, {}, {"chat_id": "local-unused"}, {"privacy_brief": true}, {"privacy_brief": false}} {
		merged, err := PrepareConfigUpdate(KindTelegram, stored, incoming)
		if err != nil {
			t.Fatal(err)
		}
		want := true
		if value, present := incoming["privacy_brief"]; present {
			want = value.(bool)
		}
		if merged["privacy_brief"] != want {
			t.Fatalf("merged privacy option %v, want %v", merged["privacy_brief"], want)
		}
		text, _, err := telegramConfiguredHTML(merged, Message{Items: []Item{{FindingID: 42, Severity: "low", Name: "Example finding"}}})
		if err != nil || strings.Contains(text, "Example finding") == want {
			t.Fatalf("rendered privacy option %v: %q %v", want, text, err)
		}
	}
}
