package server

import (
	"errors"
	"net/url"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/norma/llm"
)

// 已存密钥仅复用于相同协议认证方式和完整基础路径。
func sameLLMKeyEndpoint(a, b agent.Config) bool {
	family := func(f llm.Format) llm.Format {
		if f == llm.FormatOpenAIResponses {
			return llm.FormatOpenAI
		}
		return f
	}
	if family(a.Format) != family(b.Format) {
		return false
	}
	canonical := func(c agent.Config) string {
		u, err := url.Parse(llm.ResolveBaseURL(c.Format, c.BaseURL))
		if err != nil {
			return ""
		}
		u.Scheme = strings.ToLower(u.Scheme)
		if u.Host == "" || strings.HasSuffix(u.Host, ":") || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" {
			return ""
		}
		host := strings.ToLower(u.Hostname())
		port := u.Port()
		if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
			port = ""
		}
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		if port != "" {
			host += ":" + port
		}
		u.Host = host
		return u.String()
	}
	aURL, bURL := canonical(a), canonical(b)
	return aURL != "" && aURL == bURL
}

func resolveLLMKey(target agent.Config, profile, global *agent.Config) (string, error) {
	if key := strings.TrimSpace(target.APIKey); key != "" {
		return key, nil
	}
	withheld := false
	for _, stored := range []*agent.Config{profile, global} {
		if stored == nil || strings.TrimSpace(stored.APIKey) == "" {
			continue
		}
		if sameLLMKeyEndpoint(target, *stored) {
			return strings.TrimSpace(stored.APIKey), nil
		}
		withheld = true
	}
	if withheld {
		return "", errors.New("当前目标端点与已存 API Key 的端点不一致，请为当前目标输入 API Key")
	}
	return "", errors.New("当前目标端点未提供 API Key")
}
