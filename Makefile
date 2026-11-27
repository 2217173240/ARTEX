.DEFAULT_GOAL := help
.PHONY: help offline-package test-offline test-web test-norma typecheck test-go
PYTHON ?= python3
VERSION ?=
TARGETARCH ?= amd64
POSTGRES_IMAGE ?=
OUTPUT ?= dist/offline

help:
	@echo 'make offline-package VERSION=vMAJOR.MINOR.PATCH TARGETARCH=amd64 POSTGRES_IMAGE=postgres:16.MINOR-alpine@sha256:INDEX_DIGEST'
	@echo 'make test-offline (requires a bundle path in OFFLINE_BUNDLE and a disposable Linux Docker engine)'
	@echo 'make test-web     (runs frontend unit tests and i18n audit in web/)'
	@echo 'make typecheck    (runs TypeScript typecheck in web/)'
	@echo 'make test-norma   (runs Go race tests in third_party/norma)'
	@echo 'make test-go      (runs Go race tests; uses scripts/test-pg-local.sh when ARTEX_PG_DSN or ARTEX_TEST_PG_ADMIN_DSN is set)'

offline-package:
	$(PYTHON) scripts/build-offline.py --version '$(VERSION)' --arch '$(TARGETARCH)' --postgres '$(POSTGRES_IMAGE)' --output '$(OUTPUT)'

test-offline:
	$(PYTHON) scripts/test_offline_lifecycle.py '$(OFFLINE_BUNDLE)'

test-web:
	cd web && node --test src/lib/*.test.mjs src/lib/i18n/i18n.test.mjs && node scripts/i18n-audit.mjs

typecheck:
	cd web && npx --no-install tsc --noEmit

test-norma:
	cd third_party/norma && go test -race -count=1 ./...

test-go:
	@if [ -n "$$ARTEX_PG_DSN" ] || [ -n "$$ARTEX_TEST_PG_ADMIN_DSN" ]; then \
		./scripts/test-pg-local.sh ./...; \
	else \
		echo "[test-go] NOTICE: ARTEX_PG_DSN and ARTEX_TEST_PG_ADMIN_DSN are not set; running non-PG Go tests only (PostgreSQL tests will skip). Set one or run ./scripts/test-pg-local.sh to run PG tests."; \
		go test -race -count=1 -skip '^TestLiveContextReview$$' ./...; \
	fi
