package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"testing"
	"unicode"

	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/llm"
)

func TestConnectionLogPrivacy(t *testing.T) {
	const secret = "synthetic-profile-credential"
	const body = "{\"api_key\":\"" + secret + "\",\"authorization\":\"Bearer " + secret + "\"}\n[forged] accepted\r\x00\x1b\u0085\u2028\u2029"
	c := Config{
		Model:   "model\n[forged] accepted\r\x00\x1b\u0085\u2028\u2029\"\\",
		BaseURL: "https://user:" + secret + "@example.test:8443/private/" + secret + "?token=" + secret + "#" + secret,
		APIKey:  secret,
		Proxy:   "http://user:" + secret + "@proxy.test",
	}
	for _, count := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			_, capt := llmrec.NewMetadataCapture(context.Background())
			for i := 0; i < count; i++ {
				r := capt.TeeResponse(401+i*28, io.NopCloser(strings.NewReader(body)))
				if _, err := io.Copy(io.Discard, r); err != nil {
					t.Fatal(err)
				}
				if err := r.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			previousWriter, previousFlags, previousPrefix := log.Writer(), log.Flags(), log.Prefix()
			log.SetOutput(&out)
			log.SetFlags(0)
			log.SetPrefix("")
			defer func() {
				log.SetOutput(previousWriter)
				log.SetFlags(previousFlags)
				log.SetPrefix(previousPrefix)
			}()
			logTestConnection(c, capt)
			got := out.String()
			for _, forbidden := range []string{secret, "private", "?token", "proxy.test", "[forged]", "api_key", "authorization"} {
				if strings.Contains(got, forbidden) {
					t.Fatalf("log contains forbidden value %q: %q", forbidden, got)
				}
			}
			lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
			wantLines := max(count, 1)
			if len(lines) != wantLines {
				t.Fatalf("got %d physical lines, want %d: %q", len(lines), wantLines, got)
			}
			for i, line := range lines {
				if !strings.HasPrefix(line, "[llm-test] ") || strings.Count(line, "[llm-test]") != 1 {
					t.Fatalf("invalid log record: %q", line)
				}
				if !strings.Contains(line, `"https://example.test:8443"`) {
					t.Fatalf("missing safe origin: %q", line)
				}
				for _, r := range line {
					if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
						t.Fatalf("unescaped separator/control %U in %q", r, line)
					}
				}
				if count > 0 {
					want := fmt.Sprintf("尝试 %d/%d HTTP %d 已捕获响应字节数: %d", i+1, count, 401+i*28, len(body))
					if !strings.Contains(line, want) {
						t.Fatalf("missing diagnostics %q: %q", want, line)
					}
				} else if !strings.Contains(line, "未发出任何 HTTP 请求") {
					t.Fatalf("missing no-request diagnostic: %q", line)
				}
			}
		})
	}
}

func TestConnectionLogOrigin(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"https://user:synthetic-secret@example.test:8443/a?token=synthetic-secret#fragment", "https://example.test:8443"},
		{"http://[::1]:8080/a", "http://[::1]:8080"},
		{"https://example.test/a\u2028[forged]?secret=value", "https://example.test"},
		{"https://example.test/\nsecret", "(invalid origin)"},
		{"https://host\u2028.test/", "(invalid origin)"},
		{"https://host\x1b.test/", "(invalid origin)"},
		{"https://host%5bforged%5d/", "(invalid origin)"},
		{"https://user:secret@/", "(invalid origin)"},
		{"https://host:badport/secret", "(invalid origin)"},
		{"file:///secret", "(invalid origin)"},
		{"//example.test/secret", "(invalid origin)"},
		{"", "(invalid origin)"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got := testConnectionOrigin(tc.input); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConnectionLogDefaultOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, env, value, want string
		format                 llm.Format
	}{
		{"openai-default", "OPENAI_BASE_URL", "", "https://api.openai.com", llm.FormatOpenAI},
		{"anthropic-default", "ANTHROPIC_BASE_URL", "", "https://api.anthropic.com", llm.FormatAnthropic},
		{"responses-env", "OPENAI_BASE_URL", "https://user:synthetic-secret@gateway.test/private?token=synthetic-secret#secret", "https://gateway.test", llm.FormatOpenAIResponses},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			var out bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&out)
			defer log.SetOutput(previous)
			logTestConnection(Config{Format: tc.format}, nil)
			if got := out.String(); !strings.Contains(got, `"`+tc.want+`"`) || strings.Contains(got, "synthetic-secret") || strings.Contains(got, "invalid origin") {
				t.Fatalf("unexpected resolved origin log: %q", got)
			}
		})
	}
}
