# Go Function Shape

How to shape Go functions **while writing them** in this repo. This is authoring
guidance, not a review checklist — the goal is code that arrives verifiable, not
code that gets caught later.

## Why this rule exists

A pure, deterministic function states its whole contract in its signature. That
makes it cheap to verify: a table test needs no `bytes.Buffer`, no fake PATH, no
scanner, and no reading of call sites to discover what state is touched. When
most of the logic sits behind signatures like that, confidence in a change comes
from tests that run in microseconds instead of from careful reasoning about a
prompt loop.

Impure code doesn't disappear — it gets pushed to the edges, where it stays thin
enough to read in one pass.

## The rule

> Extract a **decision** you can name and test without I/O. Do not extract to
> reduce line count.

A decision is a mapping or a policy: text → choice, options → the installed
subset, choice → concrete value. Those become pure functions taking plain
arguments and returning plain results.

Everything left over — reading, printing, retry loops — stays in one clearly
impure function that does nothing but sequence those decisions.

## What does not qualify

Do **not** create a function that only forwards to another, or that wraps a
single expression used once. That is a Middle Man: it costs a reader (human or
agent) a hop to recover one thought, and buys nothing testable. Six one-line
functions chained together are worse than one twenty-line function, because
reconstructing the flow now takes six lookups.

Signals a split is real rather than cosmetic:

- The extracted function takes no `io.Reader` / `io.Writer` / `func` dependency.
- You can write a meaningful failing test for it in under five lines.
- It has more than one caller, **or** it isolates a rule that would otherwise be
  restated in several places.

If none hold, leave the code inline.

## Worked example

`internal/setup/agent.go` is the reference. `SelectAgent` sequences the work;
the decisions are separate and pure:

- `installedAgents(lookPath)` — policy: which supported agents are on PATH, in
  preference order. Only this function touches `lookPath`, so detection-order
  questions get tested against a slice with no I/O.
- `parseAgentChoice(answer, noneChoice) (int, bool)` — mapping: text → 1-based
  choice. Pure, so the whole input space (blank, in range, out of range,
  non-numeric, padded) is one table test.
- `agentForChoice(installed, choice)` — mapping: choice → agent, and the single
  owner of the off-by-one and the None sentinel.

`promptForAgent` keeps what is genuinely impure and genuinely subtle: the retry
loop, and the difference between a read error and input ending.

Note what was **not** extracted: `len(installed) + 1` has two call sites, but a
`noneChoiceFor` helper would be a Middle Man. Instead `printAgentMenu` returns
the value, because the function that assigns the numbers is the right authority
on what the last one is.

## Return a bool, not an error, for expected answers

A user typing `9` at a menu is an expected outcome, not a failure. Model it as
`(value, ok bool)` and let the caller print the retry message. Reserve `error`
for conditions that should propagate — a failed read, a malformed config file.
Manufacturing an `error` to carry UI copy invites a caller to `return nil, err`
and kill the retry loop.

## Test the seam, not the loop

Once decisions are extracted, cover them directly with table tests. Keep a small
number of end-to-end tests through the impure entry point to prove the wiring —
not one per input case. The existing `TestSelectAgent*` tests are that wiring
layer; `TestParseAgentChoice` is where input coverage lives.

## Prefer a type once helpers share state

When three or more helpers take the same value and depend on a shared invariant
(such as a 1-based index convention), promote it to a type with methods. One
declaration then makes the invariant discoverable, instead of it being inferred
by cross-referencing free functions. Below that threshold, free functions are
simpler — revisit `internal/setup/agent.go` if a third supported agent lands.

## Relationship to `/code-review`

`skills/code-review/SKILL.md` is codebase-agnostic and defers to documented repo
standards. This document is that standard for Go function shape: it applies at
authoring time, and the review skill honors it rather than re-litigating it.
