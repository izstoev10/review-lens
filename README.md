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
                          └─▶ push ▶ open PR
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
  "openPR": true
}
```

- **checks** run in order, fail-fast. A check passes when its command exits 0.
  These are the gate — a red check blocks the push.
- **agent** is optional. Its command is invoked inside the worktree with the
  prompt appended as the final argument (`claude -p "<prompt>"`). Set to `null`
  to only report failures instead of fixing/reviewing.
- **review** (advisory): after checks pass, the agent reviews this branch's diff
  against **baseBranch** and prints findings. It never blocks the push.
- Fix success is judged by re-running the checks, never by parsing agent output
  — which is why any agent CLI works.

### Auth / models

`review-lens` never talks to a model directly — it shells out to the CLI in
`agent.cmd`. Authentication lives in that CLI. For `claude` (Claude Code), log
in once with your Claude subscription (or set `ANTHROPIC_API_KEY`) and
review-lens reuses that session. Swap in `codex`, `opencode`, etc. the same way.

## Layout

```
main.go                 CLI entrypoint: init | run | help
internal/config         load/save .review-lens.json  (stdlib only)
internal/gitx           git wrappers: worktree lifecycle, diff, push
internal/checks         run configured commands, report pass/fail
internal/agent          build prompt + invoke the agent CLI
internal/pipeline       orchestrates the whole run
```

## Deliberately not built yet (good next steps)

1. **`git push review-lens <branch>` trigger** — a git remote helper so pushing
   *is* the gate — a natural next thing to build.
2. **TUI** with [bubbletea](https://github.com/charmbracelet/bubbletea) to watch
   a run live.
3. **Multi-agent fallback** — try `claude`, then `codex`, then `opencode`.
4. **Findings model** — distinguish auto-applied mechanical fixes from decisions
   that should be escalated to you.
