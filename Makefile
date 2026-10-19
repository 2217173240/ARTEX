.DEFAULT_GOAL := help
.PHONY: help offline-package test-offline
PYTHON ?= python3
VERSION ?=
TARGETARCH ?= amd64
POSTGRES_IMAGE ?=
OUTPUT ?= dist/offline

help:
	@echo 'make offline-package VERSION=vMAJOR.MINOR.PATCH TARGETARCH=amd64 POSTGRES_IMAGE=postgres:16.MINOR-alpine@sha256:INDEX_DIGEST'
	@echo 'make test-offline (requires a bundle path in OFFLINE_BUNDLE and a disposable Linux Docker engine)'

offline-package:
	$(PYTHON) scripts/build-offline.py --version '$(VERSION)' --arch '$(TARGETARCH)' --postgres '$(POSTGRES_IMAGE)' --output '$(OUTPUT)'

test-offline:
	$(PYTHON) scripts/test_offline_lifecycle.py '$(OFFLINE_BUNDLE)'
