# review-lens

## Writing code

### Go function shape

Extract decisions you can test without I/O; keep reading, printing, and retry
loops in one thin impure function. Don't split to reduce line count. Apply this
while writing, not only when reviewing. See `docs/agents/go-style.md`.

## Agent skills

### Issue tracker

Issues are tracked in GitHub Issues (via the `gh` CLI); external PRs are not a triage surface. See `docs/agents/issue-tracker.md`.

### Triage labels

Canonical label vocabulary, unmodified (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context layout (`CONTEXT.md` + `docs/adr/` at the repo root). See `docs/agents/domain.md`.
