package agent

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestProviderProxyValidationErrorsDoNotEchoInput(t *testing.T) {
	t.Parallel()
	userinfo := url.UserPassword("fixture-user", "fixture-pass").String()
	const details = "?fixture-query=fixture-value#fixture-fragment"
	tests := []struct {
		name  string
		proxy string
		want  string
	}{
		{
			name:  "control character",
			proxy: "http://" + userinfo + "@proxy.invalid:8080/\n" + details,
			want:  "llm: invalid proxy URL (use a valid http://, https:// or socks5:// URL)",
		},
		{
			name:  "invalid port",
			proxy: "http://" + userinfo + "@proxy.invalid:bad" + details,
			want:  "llm: invalid proxy URL (use a valid http://, https:// or socks5:// URL)",
		},
		{
			name:  "invalid host",
			proxy: "http://" + userinfo + "@[::1" + details,
			want:  "llm: invalid proxy URL (use a valid http://, https:// or socks5:// URL)",
		},
		{
			name:  "invalid escape",
			proxy: "http://" + userinfo + "@proxy.invalid/%zz" + details,
			want:  "llm: invalid proxy URL (use a valid http://, https:// or socks5:// URL)",
		},
		{
			name:  "missing scheme with authority",
			proxy: "//" + userinfo + "@proxy.invalid:8080/" + details,
			want:  "llm: proxy URL missing scheme (use http://, https:// or socks5://)",
		},
		{
			name:  "missing scheme with path",
			proxy: "proxy.invalid/" + details,
			want:  "llm: proxy URL missing scheme (use http://, https:// or socks5://)",
		},
		{
			name:  "unsupported scheme",
			proxy: "fixture-private-scheme://" + userinfo + "@proxy.invalid:8080/" + details,
			want:  "llm: unsupported proxy scheme (use http, https or socks5)",
		},
		{
			name:  "socks5h remains unsupported",
			proxy: "socks5h://" + userinfo + "@proxy.invalid:1080/" + details,
			want:  "llm: unsupported proxy scheme (use http, https or socks5)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := quotaAwareHTTPClient(tt.proxy, "")
			if client != nil || err == nil {
				t.Fatal("expected invalid proxy to return only an error")
			}
			if err.Error() != tt.want {
				t.Errorf("unexpected validation error: %q", err.Error())
			}
			if errors.Unwrap(err) != nil {
				t.Error("validation error must not wrap the input-bearing parse error")
			}
			for _, marker := range []string{"fixture-user", "fixture-pass", "proxy.invalid", "fixture-query", "fixture-value", "fixture-fragment", "fixture-private-scheme"} {
				if strings.Contains(err.Error(), marker) {
					t.Errorf("validation error includes submitted %q", marker)
				}
			}
			// The public provider builder must propagate the same safe error.
			_, buildErr := (Config{Proxy: tt.proxy}).NewProvider()
			if buildErr == nil || buildErr.Error() != tt.want {
				t.Error("provider builder did not preserve the safe validation error")
			}
		})
	}
}

func TestProviderProxyRoutingConfiguration(t *testing.T) {
	userinfo := url.UserPassword("fixture-user", "fixture-pass").String()
	tests := []struct {
		name  string
		proxy string
	}{
		{name: "direct"},
		{name: "whitespace direct", proxy: " \t "},
		{name: "http", proxy: "http://proxy.invalid:8080"},
		{name: "https credentials", proxy: "https://" + userinfo + "@proxy.invalid:8443"},
		{name: "socks5 credentials", proxy: "socks5://" + userinfo + "@proxy.invalid:1080"},
		{name: "trimmed http", proxy: " \thttp://proxy.invalid:8080 \t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := quotaAwareHTTPClient(tt.proxy, " x-session ")
			if err != nil {
				t.Fatal(err)
			}
			wrapper, ok := client.Transport.(quotaAwareTransport)
			if !ok {
				t.Fatal("missing quota-aware transport")
			}
			if wrapper.sessionHeaderKey != "x-session" {
				t.Errorf("session header key = %q", wrapper.sessionHeaderKey)
			}
			transport, ok := wrapper.base.(*http.Transport)
			if !ok {
				t.Fatal("missing HTTP transport")
			}
			want := strings.TrimSpace(tt.proxy)
			if want == "" {
				if transport.Proxy != nil {
					t.Error("direct configuration must not use an environment proxy")
				}
				return
			}
			if transport.Proxy == nil {
				t.Fatal("missing configured proxy")
			}
			// Resolving the configured proxy does not send an HTTP request.
			got, err := transport.Proxy(&http.Request{})
			if err != nil || got == nil || got.String() != want {
				t.Error("configured proxy URL was not preserved")
			}
		})
	}
}
