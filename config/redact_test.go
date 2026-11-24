package config

import "testing"

func TestRedactOmitsConnectionInput(t *testing.T) {
	for _, dsn := range []string{
		"host=localhost password='one\\' two' password=three",
		"postgres://u:synthetic@localhost/d",
		"postgres://localhost/d?password=one&password=two",
		"postgres://u:synthetic@localhost/%zz",
		"host=localhost password=one\\ two",
		"postgres://localhost/d?options=synthetic",
	} {
		if got := Redact(dsn); got != "[REDACTED DSN]" {
			t.Fatalf("unexpected redaction: %q", got)
		}
	}
	if Redact("") != "" {
		t.Fatal("empty input changed")
	}
}
