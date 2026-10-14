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
helpers; `continuity_wire_test.go` is a local regression. No reasoning/signature,
provider selection, usage, token sizing or other runtime behavior was upgraded.

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

Maintenance boundary: review only these four production files against the
original hashes. Upgrade by restoring exact official source, reconsidering each
patch against the new version, and rerunning retained tests and ARTEX tests.
Remove the replacement once an official release carries equivalent fixes.
Do not treat this directory as a general independent Norma fork.
