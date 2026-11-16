package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/transcript"
)

func TestChatRestoreFailurePreservesTranscript(t *testing.T) {
	for _, kind := range []string{"corrupt", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			store := transcript.NewStore(dir)
			path := store.MainPath("restore-case")
			original := []byte("{broken\n")
			if kind == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			// 缺少 provider 时仍应在恢复阶段返回，避免进入模型执行。
			actor := NewChatAgent(nil, "", dir, store, 0)
			text, err := actor.Chat(context.Background(), "chat", "restore-case", "continue", 1, 0, false, nil)
			if err == nil || !strings.Contains(err.Error(), `restore Norma session "restore-case"`) {
				t.Fatalf("text=%q err=%v", text, err)
			}
			if text != "" {
				t.Fatalf("unexpected text %q", text)
			}
			if kind == "directory" {
				entries, err := os.ReadDir(path)
				if err != nil || len(entries) != 0 {
					t.Fatalf("directory changed: %v %v", entries, err)
				}
			} else {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != string(original) {
					t.Fatalf("transcript changed: %q %v", got, err)
				}
			}
		})
	}
}

func TestRestoreMissingTranscript(t *testing.T) {
	store := transcript.NewStore(t.TempDir())
	s := agentcore.NewSession(agentcore.Options{Transcript: store, SessionID: "fresh"})
	defer s.Close()
	if err := s.Resume("fresh"); err != nil {
		t.Fatal(err)
	}
	if len(s.Messages()) != 0 {
		t.Fatal("fresh session has history")
	}
}
