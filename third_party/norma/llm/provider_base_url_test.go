package llm

import (
	"net/http"
	"testing"
)

func TestResolveBaseURLMatchesConstructedProvider(t *testing.T) {
	for _, format := range []Format{FormatAnthropic, FormatOpenAI, FormatOpenAIResponses} {
		t.Run(string(format), func(t *testing.T) {
			defaultURL := "https://api.openai.com/v1"
			if format == FormatAnthropic {
				defaultURL = "https://api.anthropic.com"
			}
			for _, tt := range []struct {
				name    string
				baseURL string
				envURL  string
				want    string
			}{
				{"default", "", "", defaultURL},
				{"environment", "", "https://environment.invalid/api", "https://environment.invalid/api"},
				{"environment trailing slash", "", "https://environment.invalid/api/", "https://environment.invalid/api/"},
				{"environment whitespace", "", " \thttps://environment.invalid/api/ \t", " \thttps://environment.invalid/api/ \t"},
				{"explicit", "https://explicit.invalid/api", "https://environment.invalid/api", "https://explicit.invalid/api"},
				{"explicit trailing slash", "https://explicit.invalid/api/", "https://environment.invalid/api", "https://explicit.invalid/api/"},
				{"explicit whitespace", " \thttps://explicit.invalid/api/ \t", "https://environment.invalid/api", " \thttps://explicit.invalid/api/ \t"},
				{"explicit whitespace only", " \t", "https://environment.invalid/api", " \t"},
			} {
				t.Run(tt.name, func(t *testing.T) {
					if format == FormatAnthropic {
						t.Setenv("ANTHROPIC_BASE_URL", tt.envURL)
						t.Setenv("OPENAI_BASE_URL", "https://unrelated-openai.invalid/api")
					} else {
						t.Setenv("OPENAI_BASE_URL", tt.envURL)
						t.Setenv("ANTHROPIC_BASE_URL", "https://unrelated-anthropic.invalid/api")
					}
					got := ResolveBaseURL(format, tt.baseURL)
					if got != tt.want {
						t.Fatalf("ResolveBaseURL = %q, want %q", got, tt.want)
					}
					client := &http.Client{}
					provider, err := NewProvider(Config{Format: format, BaseURL: tt.baseURL, HTTPClient: client})
					if err != nil {
						t.Fatal(err)
					}
					var cfg Config
					switch p := provider.(type) {
					case *anthropicProvider:
						cfg = p.cfg
					case *openaiProvider:
						cfg = p.cfg
					case *openaiResponsesProvider:
						cfg = p.cfg
					default:
						t.Fatalf("unexpected provider type %T", provider)
					}
					if cfg.BaseURL != got || cfg.HTTPClient != client {
						t.Fatalf("provider configuration differs: BaseURL %q, caller client preserved %v", cfg.BaseURL, cfg.HTTPClient == client)
					}
				})
			}
		})
	}
}

func TestResolveBaseURLUnknownFormat(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://anthropic-environment.invalid")
	t.Setenv("OPENAI_BASE_URL", "https://openai-environment.invalid")
	for _, baseURL := range []string{"", "https://explicit.invalid/api/", " \t"} {
		if got := ResolveBaseURL(Format("unknown"), baseURL); got != baseURL {
			t.Fatalf("ResolveBaseURL(unknown, %q) = %q", baseURL, got)
		}
	}
}
