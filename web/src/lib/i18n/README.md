UI translations are based on the Chinese → English dictionary contributed by
hongvincent in ARTEX PR #193 (commit 1368a9422de0e083f31e543b31e231730f1ba675),
with additions for the declarative locale selector. The runtime implementation
is declarative React context; it never walks or rewrites DOM content.

Call `useI18n().t()` only for application-owned UI text. Keep raw user content,
target names, evidence, reports, logs, and tool output unchanged. Missing keys
retain the original text. Parameters use `{name}` placeholders.

The selector is available on authentication pages, the main header, and the task
header. Chinese remains the server and initial client render; local preferences
load after hydration. Switching locale updates React text without reloading or
remounting the application. Storage failures retain in-memory switching.

Run `node scripts/i18n-audit.mjs` from `web/` for a deterministic inventory of
static translation calls missing English entries and remaining Chinese JSX
text. Native language names remain in their own language. The login terms retain
their authoritative Chinese body with an English UI note. Runtime API errors,
model-generated reports, target and user names, commands, evidence, and prompt
values remain in their original language.
