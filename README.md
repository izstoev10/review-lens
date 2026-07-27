# review-lens

A pre-push validation gate: run your checks and an AI review of the diff inside
a disposable git worktree, and only push when everything's green.

## The idea

Before code leaves your machine, run your checks — and optionally let an AI
agent fix failures — inside a **disposable git worktree**, so your real working
directory is never touched. Push only when everything is green.

```
you commit your changes, then run `review-lens run`:

current branch
  └─▶ disposable worktree (isolated copy at HEAD)
        └─▶ run checks in order (build, test, lint, …)
              ├─ red ──▶ ask agent to fix ▶ re-run  (up to N attempts)
              └─ green ─▶ AI reviews the diff vs main ▶ print findings
                          └─▶ push ▶ open PR ▶ stamp gate signature
```

Checks are the **gate** (red blocks the push). The AI review is **advisory** —
it prints findings for you but never blocks. Your real working directory is
never touched; everything runs in the throwaway worktree.

## Install

```sh
go install github.com/izstoev10/review-lens@latest   # once it's on GitHub
# or, locally:
go build -o review-lens . && mv review-lens ~/bin/    # anywhere on your PATH
```

## Use

```sh
cd your-repo
review-lens init      # writes .review-lens.json
# edit .review-lens.json to set your checks / agent

review-lens run       # full gate: checks -> fix -> review -> push -> PR
review-lens pr        # review the current branch's OPEN PR, read-only
review-lens pr 1234   # review a specific PR by number
```

`pr` is the safe, read-only path: it pulls the PR diff via `gh pr diff`, has the
agent review it, and shows findings. Nothing is committed, pushed, or edited —
ideal when the branch is already pushed and the PR exists.

In a real terminal, `pr` opens an interactive TUI:

1. **Live view** — a spinner + elapsed timer and a feed of what the agent is
   doing (files read, commands run) while it works.
2. **Findings viewer** — navigate with `j/k` (and `g/G`). Select findings with
   `space` (`A` all, `N` none).
3. **Fix** — press `f` to have the agent fix the selected findings. It edits
   your working tree directly (review with `git diff`, then commit); nothing is
   committed or pushed for you.

Piped or non-interactive, it prints the plain colour report instead. The live
view requires an agent that emits Claude's `--output-format stream-json` (the
default agent config does); other agents fall back to the plain path.

## Config (`.review-lens.json`)

```json
{
  "remote": "origin",
  "checks": [
    { "name": "build", "cmd": ["go", "build", "./..."] },
    { "name": "test",  "cmd": ["go", "test", "./..."] }
  ],
  "agent": { "cmd": ["claude", "-p"] },
  "maxAgentAttempts": 2,
  "review": true,
  "baseBranch": "main",
  "openPR": true,
  "jiraBaseURL": "https://acme.atlassian.net/browse/"
}
```

- **checks** run in order, fail-fast. A check passes when its command exits 0.
  These are the gate — a red check blocks the push.
- **agent** is optional. Its command is invoked inside the worktree with the
  prompt appended as the final argument (`claude -p "<prompt>"`). Set to `null`
  to only report failures instead of fixing/reviewing.
- **review** (advisory): after checks pass, the agent reviews this branch's diff
  against **baseBranch** and prints findings. It never blocks the push.
- **reviewGuidancePath** points to the editable review-criteria file (see below).
  Omit it to use the default location.
- **openPR** opens a PR with `gh` after a green push (see [PR body](#pr-body)).
- **jiraBaseURL** (optional) — your Jira "browse" base. Set it to link PRs to
  tickets (see [PR body](#pr-body)); omit it in non-Jira repos.
- Fix success is judged by re-running the checks, never by parsing agent output
  — which is why any agent CLI works.

### PR body

When `review-lens run` opens a PR, it builds the body rather than dumping raw
commits:

- **Repo template** — if the repo has a PR template
  (`.github/pull_request_template.md`, checked case-insensitively), review-lens
  uses it as the body. Otherwise it falls back to `gh`'s commit-derived summary.
- **Jira linking** — with **jiraBaseURL** set, review-lens parses the ticket key
  from the branch name (`feat/oa-2576-add-limiter` → `OA-2576`), prefixes the PR
  title with `[OA-2576]`, and adds a clickable `Jira:` link to the top of the
  body. Unset → no ticket parsing.
- The [gate signature](#require-review-lens-gate) is always appended.

Body-building happens on **creation**. Re-running `run` on an existing PR only
back-fills the gate signature — it never overwrites a body or title you've since
edited.

### Review guidance (`.review-lens.guidance.md`)

*What* the agent flags — the criteria, the error/warning/info severity rubric,
and house-style expectations — lives in an editable markdown file, not compiled
into the binary. `review-lens init` writes a starter `.review-lens.guidance.md`;
edit it to tune reviews and the change takes effect on the next run, no rebuild
needed. Delete the file and review-lens falls back to a built-in default. Point
`reviewGuidancePath` elsewhere to use a different file.

Only the criteria are editable — the structured JSON findings format is fixed,
so tuning the guidance can never break how findings are parsed or rendered.

The guidance file can also be a **skill** in the [Matt Pocock skill
convention](https://github.com/mattpocock/skills) — a `SKILL.md` with YAML
frontmatter — and review-lens strips the frontmatter before using the body.
This repo's own review standards live at `skills/code-review/SKILL.md`, and its
`.review-lens.json` points `reviewGuidancePath` there, so review-lens reviews
itself with the same skill you can read and edit.

### Auth / models

`review-lens` never talks to a model directly — it shells out to the CLI in
`agent.cmd`. Authentication lives in that CLI. For `claude` (Claude Code), log
in once with your Claude subscription (or set `ANTHROPIC_API_KEY`) and
review-lens reuses that session. Swap in `codex`, `opencode`, etc. the same way.

## Require review-lens gate

When `review-lens run` opens (or updates) a PR, it stamps a deterministic
**gate signature** — an HTML comment, invisible in the rendered PR — into the
PR body:

```
<!-- review-lens-gate:v1 -->
```

A GitHub Actions workflow (`.github/workflows/require-review-lens.yml`) then
fails any PR to `main` whose body lacks that signature, so every human PR is
provably routed through the gate. Automation is exempt: GitHub Apps
(`user.type == "Bot"`, e.g. Dependabot) pass automatically, and you can list
extra automation logins in the workflow's `EXEMPT_AUTHORS`.

**Make it required.** The workflow alone only reports; to actually block merges,
add it as a required status check:

> GitHub → repo **Settings → Branches → Branch protection rules** → add/edit the
> rule for `main` → enable **Require status checks to pass before merging** →
> search for and select **`require review-lens signature`**.

Missing the signature on a PR you opened by hand? Either re-run `review-lens run`
(it back-fills the signature on the existing PR) or paste the marker above into
the PR body — the check re-runs on edit.

## Layout

```
main.go                 CLI entrypoint: init | run | pr | loop | help
internal/config         load/save .review-lens.json  (stdlib only)
internal/gitx           git wrappers: worktree lifecycle, diff, push
internal/checks         run configured commands, report pass/fail
internal/guidance       load editable review criteria (fallback to default)
internal/agent          build prompt + invoke the agent CLI
internal/findings       parse + render structured review findings
internal/ci             read GitHub CI status via gh (for the auto-fix loop)
internal/signature      the PR gate signature (stamp + marker)
internal/tui            interactive review/run terminal UI
internal/pipeline       orchestrates run / pr / loop
```

## Deliberately not built yet (good next steps)

1. **`git push review-lens <branch>` trigger** — a git remote helper so pushing
   *is* the gate — a natural next thing to build.
2. **TUI** with [bubbletea](https://github.com/charmbracelet/bubbletea) to watch
   a run live.
3. **Multi-agent fallback** — try `claude`, then `codex`, then `opencode`.
4. **Findings model** — distinguish auto-applied mechanical fixes from decisions
   that should be escalated to you.
