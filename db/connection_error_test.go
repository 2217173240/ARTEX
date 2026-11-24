package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestOpenDoesNotExposeMalformedDSN(t *testing.T) {
	_, err := Open("host=localhost password=synthetic_secret port=invalid")
	if err == nil {
		t.Fatal("expected parse error")
	}
	if strings.Contains(fmt.Sprintf("%+v", err), "synthetic_secret") {
		t.Fatal("connection error exposed password")
	}
	var parseErr *pgconn.ParseConfigError
	if !errors.As(err, &parseErr) {
		t.Fatal("parse error identity lost")
	}
}

func TestConnectionErrorPreservesCancellation(t *testing.T) {
	err := &connectionError{operation: "ping postgres", cause: context.Canceled}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation identity lost")
	}
}

func TestConnectionErrorSafeCategories(t *testing.T) {
	for _, tc := range []struct {
		cause error
		want  string
	}{
		{context.Canceled, "connection canceled"},
		{context.DeadlineExceeded, "connection deadline exceeded"},
		{&pgconn.PgError{Code: "28P01", Message: "synthetic_secret"}, "authentication failed"},
		{&pgconn.PgError{Code: "3D000", Message: "synthetic_secret"}, "database does not exist"},
		{&pgconn.PgError{Code: "53300", Message: "synthetic_secret"}, "connection limit reached"},
		{errors.New("synthetic_secret"), "connection failed"},
	} {
		err := &connectionError{operation: "ping postgres", cause: tc.cause}
		if err.Error() != "ping postgres: "+tc.want {
			t.Fatalf("unexpected category: %s", err)
		}
		if !errors.Is(err, tc.cause) {
			t.Fatal("cause identity lost")
		}
	}
}
