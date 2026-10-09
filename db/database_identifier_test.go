package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestEnsureDatabaseQuotedIdentifier(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL fixture is not configured")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("fixture DSN is not a URL")
	}
	maintenance := *u
	maintenance.Path, maintenance.RawPath = "/postgres", ""
	admin, err := sql.Open("pgx", maintenance.String())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("artex_identifier_fixture_%d\"name", time.Now().UnixNano())
	target := *u
	target.Path, target.RawPath = "/"+name, ""
	if err := ensureDatabase(target.String()); err != nil {
		t.Fatalf("valid quoted database identifier failed: %v", err)
	}
	defer func() {
		if _, err := admin.Exec("DROP DATABASE " + pgx.Identifier{name}.Sanitize()); err != nil {
			t.Error(err)
		}
	}()
	var exists bool
	if err := admin.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", name).Scan(&exists); err != nil || !exists {
		t.Fatalf("database was not created with its exact name: exists=%v err=%v", exists, err)
	}
	if err := ensureDatabase(target.String()); err != nil {
		t.Fatalf("repeat ensure failed: %v", err)
	}
}
