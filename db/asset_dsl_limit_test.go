package db

import (
	"strings"
	"testing"
)

func TestDSLInputBounds(t *testing.T) {
	for _, query := range []string{
		"", "  ", "domain==example.com AND (port>=443 OR technology=nginx)",
		strings.Repeat("a", dslMaxLen),
		strings.Repeat(" ", dslMaxLen),
		strings.Repeat("(", dslMaxDepth) + "domain==example.com" + strings.Repeat(")", dslMaxDepth),
		`title=="` + strings.Repeat("(", dslMaxDepth+1) + strings.Repeat(")", dslMaxDepth+1) + `"`,
		`"` + strings.Repeat("(", dslMaxDepth+1) + `"`,
		strings.Repeat("(domain==example.com) OR ", 33) + "port=443",
	} {
		if err := ValidateDSL(query); err != nil {
			t.Fatalf("legal query (%d bytes): %v", len(query), err)
		}
	}
	for _, query := range []string{
		strings.Repeat("a", dslMaxLen+1),
		strings.Repeat(" ", dslMaxLen+1),
		strings.Repeat("界", dslMaxLen/3+1),
		strings.Repeat("(", dslMaxDepth+1) + "domain==example.com" + strings.Repeat(")", dslMaxDepth+1),
	} {
		if _, err := ParseDSL(query); err == nil {
			t.Fatalf("accepted query beyond input bound (%d bytes)", len(query))
		}
	}
}
