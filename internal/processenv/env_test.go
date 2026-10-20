package processenv

import (
	"reflect"
	"testing"
)

func TestInheritedPreservesConfiguration(t *testing.T) {
	parent := []string{"OPENAI_API_KEY=server", "openai_api_key=lower", "PATH=/bin", "HOME=home", "ARTEX_LLM_CALL_TIMEOUT=37", "SSL_CERT_DIR=certs", "CUSTOM_KEY=tool"}
	want := parent[1:]
	if got := inherited(parent, false); !reflect.DeepEqual(got, want) {
		t.Fatalf("POSIX environment mismatch: %v", got)
	}
	if got := inherited(parent, true); !reflect.DeepEqual(got, parent[2:]) {
		t.Fatalf("Windows environment mismatch: %v", got)
	}
	if parent[0] != "OPENAI_API_KEY=server" {
		t.Fatal("parent slice changed")
	}
}

func TestStdioOverridesCaseAliases(t *testing.T) {
	explicit := map[string]string{"OPENAI_API_KEY": "upper", "openai_api_key": "lower", "PATH": "bin"}
	for range 100 {
		got := stdioOverrides(explicit, true)
		if len(got) != 6 || got["OPENAI_API_KEY"] != "lower" || got["PATH"] != "bin" {
			t.Fatalf("Windows alias precedence mismatch: %v", got)
		}
	}
	got := stdioOverrides(explicit, false)
	if got["OPENAI_API_KEY"] != "upper" || got["openai_api_key"] != "lower" {
		t.Fatalf("POSIX case-sensitive overlay mismatch: %v", got)
	}
	if len(explicit) != 3 {
		t.Fatal("caller map changed")
	}
}
