---
name: code-review
description: How review-lens reviews a diff — verify before reporting, weight correctness and data safety first, stay high-signal, and classify each finding's action (auto-fix / ask-user / no-op) fail-closed.
---

# Code review

Standards for reviewing a diff. They are **codebase-agnostic** — universal
engineering best practice for any language or repo, not the rules of one project.

## How to review (the loop)

1. **Read the diff.** Review only the changed lines and what they directly
   affect — do not audit untouched code or propose rewrites of code the diff
   didn't touch.
2. **Verify before you report.** You are running inside the repository, so
   confirm every finding against the real code before reporting it:
   - the symbol, type, or signature is actually what you assume;
   - the failing path is genuinely reachable;
   - the case isn't already handled by nearby code you haven't looked at.
   **Drop any finding you cannot substantiate.** A low false-positive rate
   matters more than catching one extra maybe.
3. **Report only what survives.** Prefer few, high-signal findings. If nothing
   meaningful is wrong, return none — an empty review is a correct, good result.

## What to weight (in priority order)

1. **Correctness** — off-by-one, wrong operator/comparison, inverted condition,
   wrong variable, mishandled zero/empty/nil, incorrect boolean logic.
2. **Data safety & security** — untrusted input reaching a query/command/path/
   HTML sink; secrets or sensitive data in code or logs; a missing or broadened
   authorization check; access that isn't scoped to the right subject.
3. **Error & edge handling** — errors swallowed or logged-and-continued when they
   should stop; missing rollback; partial writes left on failure; unhandled
   empty/boundary input.
4. **Resource & concurrency safety** — a file/connection/timer/context not closed
   or cancelled on every path; shared state written without synchronisation; a
   data race; a lock held across I/O; a leaked goroutine.
5. **Contracts & compatibility** — a signature, field, status code, default, or
   migration change that breaks existing callers or in-flight data.
6. **Tests** — new branching logic or edge cases with no test, or a test that
   asserts nothing meaningful. Don't demand tests for trivial code.

Style is **not** on this list — defer it to linters. Never report a pure-style
nit unless it hides a real defect above.

## Always-on best practices

Regardless of what the diff is "about", these are defects wherever they appear:

- **No secret or sensitive-data leakage** — into logs, error messages, or
  responses.
- **Authentication & authorization** — every privileged action checks identity,
  every data access is scoped to the right subject; no borrowed or over-broad
  grants.
- **Backward compatibility** — public APIs, response shapes, and data migrations
  don't break existing consumers.

## Maintainability smells (lowest priority — heuristic)

These are the weakest signals here. Raise one only when the diff *introduces* it
and it clearly hurts; never let them crowd out the priorities above, and prefer
saying nothing. Three rules bind them:

- **Repo and tooling override.** A documented repo standard wins; skip anything a
  linter or formatter already enforces.
- **Heuristic, never a violation.** Label it as possible ("possible Data
  Clumps"), cap severity at `warning` (usually `info`), and set action `no-op` or
  `ask-user` — never `auto-fix`, because the fix is a design choice.
- **Diff-local only.** Judge only what the change itself shows; do not infer a
  module's history or reasons-for-change from a single diff.

Match against the changed code (*what it is* → *how to fix*):

- **Mysterious Name** — a name that doesn't reveal what it does or holds. →
  rename it; if no honest name comes, the design is murky.
- **Duplicated Code** — the same logic shape in more than one hunk or file of the
  change. → extract the shared shape, call it from both.
- **Primitive Obsession** — a primitive or string standing in for a domain
  concept that deserves its own type. → give the concept its own small type.
- **Data Clumps** — the same few fields or params keep travelling together. →
  bundle them into one type, pass that.
- **Message Chains** — long `a.b().c().d()` navigation the caller shouldn't
  depend on. → hide the walk behind one method on the first object.
- **Middle Man** — a class or function that mostly just delegates onward. → cut
  it, call the real target directly.

## How to rate severity

- **error** — a defect that will produce wrong behaviour, a crash, data loss, or
  a security hole on a reachable path. If you can describe inputs that break it,
  it's an error.
- **warning** — a real risk or judgment call: works today but is fragile, relies
  on an unstated assumption, or will bite under load or edge input. Also a public
  API/contract change worth a second opinion.
- **info** — a minor, declinable improvement.

When torn between two levels, choose the **lower** one. Never inflate severity.

## How to classify the action (fail closed)

Every finding gets an `action`. It decides what the auto-fix loop applies without
a human, so **when in doubt, choose `ask-user`.**

- **auto-fix** — a single, obviously-correct, mechanical fix that cannot change
  intended behaviour: add the missing nil check, close the resource, handle the
  returned error, correct the off-by-one, add the missing `break`. Use this
  **only** when all of these hold:
  - there is exactly one right answer;
  - it does not change intended behaviour;
  - it does not touch a public contract;
  - it does not touch authorization, authentication, or sensitive-data handling.
- **ask-user** — anything else: a fix with trade-offs or latitude, a behaviour or
  contract change, a security/authz-adjacent fix, or anything you're unsure about.
  This is the default.
- **no-op** — informational only; nothing should change.

## House style for findings

- Do **not** restate what the code does. Do **not** praise it. Do **not** write a
  summary paragraph.
- Every finding names the **concrete failure mode** and why it matters — not
  "this could be improved" but "if `userID` is empty, `len(userID)` is 0 and this
  panics."
- Reference the specific file and line.
- Keep each `detail` to 1–3 sentences.

## Calibration

Good — specific, verified, reproducible, correctly classified:

> **error / auto-fix** — `pay.go:42` — `total / len(items)` panics with a
> divide-by-zero when `items` is empty, which is reachable from an empty request
> body. Guard the empty case before dividing.

Good — a real risk that is *not* a safe auto-fix, so it escalates:

> **warning / ask-user** — `api.go:22` — this handler reads records by
> `user_id` from the request without checking the caller owns that id, so any
> authenticated user can read another's data. The fix depends on your authz
> model, so it needs a human.

Weak — vague, no failure mode; do **not** produce this:

> **warning** — `pay.go:42` — This division could potentially be problematic and
> might want to be reviewed for edge cases.

Over-reach — auditing unchanged code, or a style preference dressed as a risk;
**avoid**:

> **warning** — `pay.go:10` — Consider extracting this into a helper for
> readability. *(Not in the diff, and a preference, not a defect.)*
