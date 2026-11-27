#!/usr/bin/env bash
# Run Go race tests with all CI PostgreSQL test DSN env vars configured.
#
# Usage:
#   ARTEX_TEST_PG_ADMIN_DSN="postgres://artex_ci:fixture_only@127.0.0.1:5432/postgres?sslmode=disable" \
#     ./scripts/test-pg-local.sh [go test args...]
#   ARTEX_PG_DSN="postgres://artex_ci:fixture_only@127.0.0.1:5432/artex_test?sslmode=disable" \
#     ./scripts/test-pg-local.sh [go test args...]
#   ./scripts/test-pg-local.sh --dsn "postgres://..." [go test args...]
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/test-pg-local.sh [--dsn <postgres-dsn>] [go test args...]

Exports all 7 CI PostgreSQL DSN environment variables:
  ARTEX_PG_DSN
  ARTEX_INTERCEPT_TEST_DSN
  ARTEX_AUTH_TEST_DSN
  ARTEX_RATE_TEST_PG_DSN
  ARTEX_LEDGER_TEST_DSN
  ARTEX_COMPANY_TEST_DSN
  ARTEX_CALENDAR_TEST_ADMIN_DSN

and runs:
  go test -race -count=1 -skip '^TestLiveContextReview$' [go test args...] (default: ./...)

DSN resolution (in order):
  1. --dsn <dsn> / --dsn=<dsn>       Uses the supplied database DSN directly.
  2. ARTEX_TEST_PG_ADMIN_DSN         Creates a temporary isolated database via psql,
                                     runs tests against it, and drops it on exit.
  3. ARTEX_PG_DSN                    Uses the supplied database DSN directly.

Example with a disposable local PostgreSQL container:
  docker run --name artex-local-pg --rm -d -p 127.0.0.1:55432:5432 \
    -e POSTGRES_USER=artex_ci -e POSTGRES_PASSWORD=fixture_only -e POSTGRES_DB=postgres postgres:16
  ARTEX_TEST_PG_ADMIN_DSN="postgres://artex_ci:fixture_only@127.0.0.1:55432/postgres?sslmode=disable" \
    ./scripts/test-pg-local.sh ./...
EOF
}

explicit_dsn=""
go_args=()

while [ "$#" -gt 0 ]; do
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    --dsn)
      if [ "$#" -lt 2 ] || [ -z "$2" ]; then
        echo "error: --dsn requires a non-empty PostgreSQL DSN argument" >&2
        usage >&2
        exit 2
      fi
      explicit_dsn="$2"
      shift 2
      ;;
    --dsn=*)
      explicit_dsn="${1#--dsn=}"
      if [ -z "$explicit_dsn" ]; then
        echo "error: --dsn= requires a non-empty PostgreSQL DSN value" >&2
        usage >&2
        exit 2
      fi
      shift
      ;;
    --)
      shift
      while [ "$#" -gt 0 ]; do
        go_args+=("$1")
        shift
      done
      ;;
    *)
      go_args+=("$1")
      shift
      ;;
  esac
done

if [ "${#go_args[@]}" -eq 0 ]; then
  go_args=(./...)
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

created_db=""
admin_dsn=""
target_dsn=""

cleanup() {
  if [ -n "$created_db" ] && [ -n "$admin_dsn" ]; then
    psql "$admin_dsn" -v ON_ERROR_STOP=1 -c "DROP DATABASE IF EXISTS \"$created_db\" WITH (FORCE);" >/dev/null 2>&1 || true
  fi
}

if [ -n "$explicit_dsn" ]; then
  target_dsn="$explicit_dsn"
  admin_dsn="${ARTEX_CALENDAR_TEST_ADMIN_DSN:-$target_dsn}"
elif [ -n "${ARTEX_TEST_PG_ADMIN_DSN:-}" ]; then
  command -v psql >/dev/null 2>&1 || {
    echo "error: psql is required when ARTEX_TEST_PG_ADMIN_DSN is set (or pass ARTEX_PG_DSN / --dsn for an existing database)" >&2
    exit 1
  }
  command -v python3 >/dev/null 2>&1 || {
    echo "error: python3 is required to build the isolated database DSN from ARTEX_TEST_PG_ADMIN_DSN" >&2
    exit 1
  }
  admin_dsn="$ARTEX_TEST_PG_ADMIN_DSN"
  created_db="artex_local_test_$(date +%Y%m%d_%H%M%S)_$$"
  target_dsn="$(python3 -c 'import sys, urllib.parse as u; p = u.urlsplit(sys.argv[1]); print(u.urlunsplit((p.scheme, p.netloc, "/" + sys.argv[2], p.query, p.fragment)))' "$admin_dsn" "$created_db")"
  trap cleanup EXIT INT TERM
  echo "[test-pg-local] Creating isolated PostgreSQL database: $created_db" >&2
  psql "$admin_dsn" -v ON_ERROR_STOP=1 -c "CREATE DATABASE \"$created_db\";" >/dev/null
elif [ -n "${ARTEX_PG_DSN:-}" ]; then
  target_dsn="$ARTEX_PG_DSN"
  admin_dsn="${ARTEX_CALENDAR_TEST_ADMIN_DSN:-$target_dsn}"
else
  echo "error: no PostgreSQL test DSN configured; refusing to silently skip PG integration tests." >&2
  echo "Set ARTEX_TEST_PG_ADMIN_DSN (recommended, creates & drops a clean database), ARTEX_PG_DSN, or pass --dsn <postgres-dsn>." >&2
  echo >&2
  usage >&2
  exit 2
fi

export ARTEX_PG_DSN="$target_dsn"
export ARTEX_INTERCEPT_TEST_DSN="$target_dsn"
export ARTEX_AUTH_TEST_DSN="$target_dsn"
export ARTEX_RATE_TEST_PG_DSN="$target_dsn"
export ARTEX_LEDGER_TEST_DSN="$target_dsn"
export ARTEX_COMPANY_TEST_DSN="$target_dsn"
export ARTEX_CALENDAR_TEST_ADMIN_DSN="$admin_dsn"

go test -race -count=1 -skip '^TestLiveContextReview$' "${go_args[@]}"
