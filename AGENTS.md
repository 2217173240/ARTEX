# ARTEX maintenance

Keep changes focused on the current architecture, preserve user data and existing work, and verify the actual outcome. Use isolated databases and owned local services for tests. Keep raw monitoring responses, logs, one-off scripts and reports outside the source tree; retain useful dependency and build caches. Record adopted source commits, authors, licenses and adaptation boundaries in `docs/fork-integrations.md`.

## CI performance

Follow [the CI contract](docs/ci.md) during every maintenance round. Preserve necessary tests, security checks and platform lifecycle validation while reducing elapsed time and runner occupancy.

- Fetch the latest 50 workflow runs with `gh`, including earlier attempts. Inspect every job's available timestamps and steps; separate preparation, execution and cleanup. Report queue uncertainty and mixed build/test steps honestly.
- Cancel superseded runs for the same PR with `cancel-in-progress`. Keep each main push independent so every code commit receives complete validation.
- Documentation-only changes use the existing classifier and lightweight required `quality` gate. Keep CodeQL's documentation filters aligned. Do not filter the required workflow out of existence or broaden Markdown exclusions to security/configuration inputs.
- Prefer `setup-node`/`setup-go` dependency caches. Keep a 10 GB cache budget, scope keys to consumed dependencies, and avoid redundant or irrelevant caches. Use `Swatinem/rust-cache` if Rust is introduced; this repository currently has no Rust build.
- Run identical local builds twice when changing build/cache behavior. Investigate repeated downloads, invalidated dependency layers and missing inputs. Cache compilation; continue executing tests with `-count=1`.
- Download pinned, checksum-verified precompiled tools. Avoid `go install`/`cargo install` compilation during CI setup.
- Run independent jobs in parallel. Keep `needs` only for consumed outputs or required gates; rebalance long test shards using measurements while retaining complete package coverage.
- Keep platform-independent formatting, lint and audits on Ubuntu. Budget at most five simultaneous macOS jobs across all workflows; retain native macOS lifecycle checks.
- Reuse existing artifacts and completed checks where their input and validation contract match. A frontend-only job must not restore unrelated backend/toolchain caches.
- Push changes through an internal PR, review the exact HEAD, wait for the required checks, merge normally and verify main. Compare matched workflow/event/platform/cache samples; label unmatched observations and missing measurements. Refine only when evidence identifies remaining waste.

## Capability readiness

Follow [the capability contract](docs/capabilities.md): gate startup on core dependencies, and determine optional capability availability independently.

- Check each operation's actual requirements at its boundary. Missing optional capabilities affect only dependent operations and return a clear, actionable unavailable outcome.
- Support partial availability, all optional capabilities missing, capability removal, and subsequent recovery. Register only usable tools; a later operation may retry setup after recovery within a bounded budget.
- Preserve parameter validation, authorization, approval/confirmation, transaction guarantees and required operation context. An unreadable authorization catalog or corrupt requested session is a required safety/context failure for that operation.
- Do not invent success, silently replace required context, retry unconditionally, or turn optional absence into global startup failure.
- Bound setup work and preserve client/process ownership. A setup deadline must not cancel a healthy long-lived client. Close failed clients and retain per-call authorization checks after setup.
