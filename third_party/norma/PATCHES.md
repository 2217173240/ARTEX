# Norma runtime dependency patch

This is the runtime dependency closure ARTEX uses from the official
`github.com/Autumn-27/norma v0.4.3`, commit
`320ae32989da97dd91469bb89164cbfe87d83d5d` (annotated tag
`09c5cab1bc085b2f5d9966e861c2f07abb2467f2`). No RC application/module was imported.
`UPSTREAM-SHA256.json` records the original bytes of every retained upstream file,
including the original bytes of files changed here. Runtime packages not in
ARTEX's dependency closure, command binaries, examples, README and unrelated
upstream tests are omitted. There are no runtime embed assets in this version.

The v0.4.3 tag contains no LICENSE or NOTICE. The included `LICENSE` is the
official project's Apache-2.0 license (Copyright 2026 Autumn-27), fetched from
commit `70d12d96de11030137b968e7637385fb09529109`, blob
`52e2373e0ab621b7ca99ef3e26bec7eb2e70897c`. That commit descends from v0.4.3
(compare ahead 12, behind 0, merge base equal to the v0.4.3 commit).
It is explicitly a later upstream license document, not a file shipped in that tag.

The bounded changes and regression tests derived from RuochenPan (RC-CHN)'s
ARTEX fork are covered by the source fork's AGPL-3.0 license, reproduced in
`PATCH-LICENSE`; ARTEX remains AGPL-3.0. Original source links:

- https://github.com/RC-CHN/ARTEX/commit/956abad73e9f88cd9f4dcc37036af4344bf2f3ba
  — adapted `noaadapter/project.go`, `session.go`, `exchange_order_test.go`.
  Defers synthetic summaries past surviving original-message/tool spans, and
  stores that same ordered view for model reference resolution.
- https://github.com/RC-CHN/ARTEX/commit/568ad83a2e5978ec2546434c832bc6076ae86bce
  — adapted `llm/openai.go`, `sanitize_test.go`. Validates results only in the
  immediately following continuous tool run, once per nonempty call ID, and
  encodes legitimate empty tool results as `content:""`.

Local extensions: the send guard preserves its input; tests verify source
immutability, real localhost Stream/Complete encoding, restore and model-ref
alignment. `continuity_helpers_test.go` extracts only required upstream test
helpers; `continuity_wire_test.go` is a local regression. No reasoning/signature, provider selection or token sizing behavior was upgraded.
The subsequent bounded interruption repair is documented below.

Additional local safety repair: `llm/openai.go` and `llm/anthropic.go` use
a single existing slice length for initial capacity instead of adding lengths.
This removes unchecked allocation-capacity arithmetic (CodeQL allocationoverflow)
while append preserves output order/content and Anthropic merge source immutability.
This repair is local ARTEX work, separate from the attributed RC-derived changes.

Retained upstream tests cover provider encoders (OpenAI, Responses, Anthropic),
thinking placeholders/overrides, boundary pairing, projection/reassembly,
Session initialization and state persistence. Noa's unrelated pacing, long-session
capacity and retry tests are not presented as passing: the full original v0.4.3
`llm`, `noa`, `noaadapter` test run has the following independently reproduced
failures on untouched commit `320ae32989da97dd91469bb89164cbfe87d83d5d`:

- `TestDoStreamHonorsConfiguredRetries`: nil HTTPClient panic.
- `TestAdaptiveGrowthTracksTheWindow`: equal growth floor/cap prevent adaptation.
- `TestSuppressionReleaseFitsInsideTheWindow`: suppression growth threshold
  exceeds the small-window pressure band.
- `TestLongSessionStaysBounded`: last-ten-turn token peak exceeds the bound.
- `TestLongSessionWithPartiallyCompliantModel`: view token peak exceeds window.
- `TestOverflowLearningIsWiredIn`: armed floor follows configured rather than
  provider-reported window.
- `TestTierTwoIsUnreachableOnSmallWindows`: its limitation assertion fails
  because tier two does fire.

These unrelated tests are omitted from this bounded replacement; no unrelated
runtime repair is claimed. A passing retained suite does not establish that the
complete upstream suite passes. The complete omitted-test list is in the manifest.

Root `go test ./...` does not enter nested modules. CI must explicitly run:

```sh
(cd third_party/norma && go test -race -count=1 ./...)
```

Maintenance boundary: review the production files documented here against the
original hashes. Upgrade by restoring exact official source, reconsidering each
patch against the new version, and rerunning retained tests and ARTEX tests.
Remove the replacement once an official release carries equivalent fixes.
Do not treat this directory as a general independent Norma fork.


## Interrupted streaming tool input (round 21)

Adapted from RuochenPan (RC-CHN), commit
[`05a861856c69ddbbf4b904680ea984ca2757519f`](https://github.com/RC-CHN/ARTEX/commit/05a861856c69ddbbf4b904680ea984ca2757519f),
2026-10-16, under the same AGPL-3.0 `PATCH-LICENSE` boundary. The original
v0.4.3 baseline and all original-file hashes above remain unchanged. No newer
Norma module, application lifecycle or UI patch was imported.

- `harness/query.go` / `event.go`: an error or cancellation does not complete
  the tool input currently being generated, even when its JSON is valid.
  Raw input is available only as terminal diagnostics and is absent from
  executable/replayable history. Previously dispatched calls and receipts are
  recorded together; reactive overflow recovery is disabled after dispatch
  to avoid automatically repeating possible effects. Usage and error reasons
  are retained. Legitimate successful empty arguments still become `{}`.
- Local adaptations in `harness/streaming.go` / `tools.go`: each executor owns
  a child tool context, canceled on stream failure or consumer exit; queued
  tools do not start after cancellation. Actual completed results are retained.
  Active calls without an observed result receive an explicit **outcome unknown**
  interruption receipt, while queued calls receive a **not invoked** receipt.
  `Event.ToolResultDisposition` distinguishes actual, interrupted and not-invoked
  receipts without parsing their text; ARTEX capture uses it to avoid describing
  a synthetic receipt or actual error as a successful tool return. Permissions
  and tool hooks also receive that child context. Late returns cannot overwrite
  terminal receipts. This does not prove that an external side effect stopped;
  a tool or callback that ignores context may continue.
- Additional local adaptations: preserve tool-injected `Extra` guidance, and
  remove only the last unfinished tool block by position, so an empty/repeated
  provider call ID cannot erase an earlier dispatched block.
- `harness/stream_interruption_test.go` uses only synthetic providers/tools.
  It covers empty/partial/valid-but-unclosed input, provider errors, cancellation,
  silent cancellation, late events, actual successful results, running/queued/
  unfinished calls in one turn, late-write protection, recorder and usage,
  waiting-approval cancellation, no post-dispatch retry or settlement,
  safe pre-dispatch compaction recovery,
  successful empty arguments, normal token budget completion, duplicate IDs,
  and the existing API projection's tool-pair continuity.

The v0.4.3 normalized stream API has no explicit per-tool block-end event.
This repair retains its existing successful boundary convention (the next
non-input event or clean iterator end). It does not add provider protocol
validation for a silent non-canceled EOF or change completed `max_tokens`
handling. The retained nested-module suite and ARTEX regressions must both run.

## Tool output and file/search memory (round 25)

Adapted from RuochenPan (RC-CHN), commit
[`d2b11ab3674f426a6f9044ce96be40c6736a1a0a`](https://github.com/RC-CHN/ARTEX/commit/d2b11ab3674f426a6f9044ce96be40c6736a1a0a),
2026-10-19, under AGPL-3.0 in `PATCH-LICENSE`. The original v0.4.3 baseline,
`UPSTREAM-SHA256.json`, and both license documents remain unchanged. The source
commit's LICENSE bytes match `PATCH-LICENSE` (SHA-256
`0d96a4ff68ad6d4b6f1f30f713b18d5184912ba8dd389f86aa7710db079abcb0`).

- `tool/capture_writer.go` / `bash.go`: synchronous Bash captures bounded head
  and tail while optionally streaming complete output to disk. Output formatting,
  combined stdout/stderr, exit and timeout reporting follow the existing Capture
  contract. Incomplete artifacts are removed after write/close failure, and the
  ordinary truncation notice is returned. Exclusive file creation avoids replacing
  an existing artifact. Managed background commands keep their existing file/tail
  behavior.
- `tool/helpers.go`: spill strings through `io.WriteString`, avoiding a complete
  byte-slice copy. `CaptureToolOutput` and `harness/tools.go` retain Read's bounded
  line-pagination allowance and continuation notice through the final runtime cap.
- `tool/files.go`: independently adapt Read to buffered line traversal, keeping
  at most one truncated line and one output page. Binary detection still inspects
  the first 8000 bytes. Offsets, line numbers, trailing empty lines and the 2000-byte
  per-line cap retain v0.4.3 semantics. A first line larger than a very small output
  budget is returned whole (within the per-line cap), so the continuation offset
  advances. Context cancellation is checked between input fragments. Read still
  uses its existing line offset API; a high offset scans earlier lines and callers
  must account for concurrent file changes. No version token or byte-offset API
  is introduced.
- `tool/search.go`: stream non-multiline counts/file matches without retaining
  complete files or match-index arrays; ordered multiline spans advance through
  file lines once; ordered context windows merge directly. Glob patterns compile
  once per walk. Content/multiline search retains whole-file storage, and a single
  long line remains uncapped for regular-expression correctness.
- `tool/ripgrep.go`: retain only the requested page while counting the exact
  remaining results. Unlimited pages and selected very long lines retain their
  existing memory requirements. Ripgrep uses the caller's context; cancellation
  returns to the caller without beginning a Go fallback scan. The Go backend
  checks cancellation during traversal, line counting and match processing.
- `tool/ls.go` / `tool/tool.go`: use existing directory order for directory-first
  listing and a builder for multi-block result flattening.

`tool/performance_test.go` benchmarks actual Read/Go-search calls on a fixed
8,400,000-byte text file and actual `cat` subprocess output. Baselines were run
before the production changes. `tool/performance_compatibility_test.go` compares
real file fixtures with the previous read/search rules and exercises real command
artifacts, stderr/exit status, timeout/cancellation, disk failure, and installed
ripgrep pagination/cancellation. These measurements are local ARTEX evidence,
not a reproduction of the source fork's benchmark results. No source `file_commit`,
CAS, output-receipt, runner, or application-lifecycle implementation is imported.
