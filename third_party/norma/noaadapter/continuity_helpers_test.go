package noaadapter

import (
	"encoding/json"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/noa"
	"github.com/Autumn-27/norma/tool"
	"testing"
)

func simSession(t *testing.T, window int) *Session {
	t.Helper()
	opts := baseOptions()
	cfg := noa.DefaultConfig(window)
	sess, err := EnableWithSession(&opts, Options{
		ArchiveBaseDir: t.TempDir(), SessionID: "sim", Config: &cfg,
	})
	if err != nil {
		t.Fatalf("EnableWithSession: %v", err)
	}
	return sess
}
func refOfCore(t *testing.T, sess *Session, msgs []llm.Message, pick func(noa.CoreMessage) bool) string {
	t.Helper()
	cores, _ := Project(msgs)
	st := sess.State()
	for _, c := range cores {
		if !pick(c) {
			continue
		}
		if ref, ok := st.MessageRefs.ByRaw[c.ID]; ok && ref != noa.BlockedRef {
			return ref
		}
		t.Fatalf("the core matching the predicate (%s/%s) has no usable ref", c.ContentType, c.ToolCallID)
	}
	t.Fatal("no core matched the predicate")
	return ""
}
func compressRange(t *testing.T, sess *Session, callID, startRef, endRef string) string {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"content": []map[string]any{{
		"startId": startRef, "endId": endRef, "topic": "work batch",
		"summary": "Summary of " + startRef + "–" + endRef + ": read and reviewed several " +
			"modules under /src. Key files: auth/jwt.go:42, auth/middleware.go:18. " +
			"Decision: keep JWT with refresh tokens. Open: clock-skew leeway hard-coded at 60s.",
	}}})
	res, err := runCompress(sess, args, &tool.ToolContext{ToolUseID: callID})
	if err != nil {
		t.Fatalf("Compress errored: %v", err)
	}
	return res.Content[0].Text
}
