package processenv

import (
	"runtime"
	"sort"
	"strings"
)

var serverSecrets = [...]string{
	"ARTEX_PG_DSN",
	"ANTHROPIC_API_KEY",
	"OPENAI_API_KEY",
	"ARTEX_LLM_BASE_URL",
	"ARTEX_LLM_PROXY",
}

// ToolOverrides 在继承环境之上清空服务器秘密，再保留明确工具配置。
func ToolOverrides(explicit []string) []string {
	env := make([]string, 0, len(serverSecrets)+len(explicit))
	for _, name := range serverSecrets {
		env = append(env, name+"=")
	}
	return append(env, explicit...)
}

// Inherited 过滤隐式继承的服务器秘密，保留其它运行配置。
func Inherited(parent []string) []string {
	return inherited(parent, runtime.GOOS == "windows")
}

func inherited(parent []string, windows bool) []string {
	env := make([]string, 0, len(parent))
	for _, entry := range parent {
		name, _, _ := strings.Cut(entry, "=")
		if windows {
			name = strings.ToUpper(name)
		}
		secret := false
		for _, key := range serverSecrets {
			if name == key {
				secret = true
				break
			}
		}
		if !secret {
			env = append(env, entry)
		}
	}
	return env
}

// StdioOverrides 仅供进程环境使用；HTTP/SSE 配置仍按请求头处理。
func StdioOverrides(explicit map[string]string) map[string]string {
	return stdioOverrides(explicit, runtime.GOOS == "windows")
}

func stdioOverrides(explicit map[string]string, windows bool) map[string]string {
	env := make(map[string]string, len(serverSecrets)+len(explicit))
	for _, key := range serverSecrets {
		env[key] = ""
	}
	keys := make([]string, 0, len(explicit))
	for key := range explicit {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		name := key
		if windows {
			name = strings.ToUpper(name)
		}
		env[name] = explicit[key]
	}
	return env
}
