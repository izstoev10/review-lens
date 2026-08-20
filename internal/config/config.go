// Package config loads gate's settings from a JSON file.
//
// We deliberately use only the standard library (encoding/json) so the whole
// tool has zero external dependencies while you're learning. If you later want
// YAML or a nicer format, this is the one place you'd swap it out.
package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/izstoev10/review-lens/internal/guidance"
)

// Check is a single command gate runs against your code (a linter, tests, etc).
// A check "passes" when its process exits 0.
//
// Cmd must be a non-empty argv. The invariant's owner is checks.Run, which is
// total: it reports an empty Cmd as a configuration problem instead of
// executing (or panicking on) it. pipeline.preflight additionally rejects an
// empty Cmd up front so a misconfigured gate fails before any work runs.
type Check struct {
	Name string   `json:"name"` // human label, e.g. "test"
	Cmd  []string `json:"cmd"`  // argv, e.g. ["go", "test", "./..."]
	// Dir is the working directory the command runs in, relative to the repo
	// root. Empty means the root itself — every pre-Dir config keeps working.
	// This is what lets a monorepo's checks run inside their own project.
	Dir string `json:"dir,omitempty"`
}

// Agent describes how to invoke an AI CLI. The prompt is appended as the final
// argument, e.g. {"cmd": ["claude", "-p"]} -> claude -p "<prompt>".
//
// For the fix step the agent must be able to edit files non-interactively. With
// Claude Code that means a headless permission mode; the default below uses
// --permission-mode acceptEdits. Because every agent run happens inside a
// throwaway worktree (never your real tree), you can safely widen this to
// --dangerously-skip-permissions if a fix needs to run commands too.
type Agent struct {
	Cmd []string `json:"cmd"`
}

// Config is the whole tool configuration, normally read from .gate.json in the
// repo root.
type Config struct {
	// Remote is where a green branch gets pushed, e.g. "origin".
	Remote string `json:"remote"`
	// Setup commands run once before the check/fix loop — dependency installs
	// and bootstrap (npm ci, mix deps.get). They are environment, not code: a
	// setup failure is reported as a configuration problem and is never handed
	// to the fixing agent as if the branch were broken.
	Setup []Check `json:"setup,omitempty"`
	// Checks run in order. The first failing check stops the run and (if an
	// agent is configured) triggers a fix attempt.
	Checks []Check `json:"checks"`
	// Agent is optional. If nil, gate just reports failures instead of fixing.
	Agent *Agent `json:"agent,omitempty"`
	// MaxAgentAttempts bounds how many fix->recheck cycles gate will run.
	MaxAgentAttempts int `json:"maxAgentAttempts"`
	// MaxLoopIterations bounds the `loop` command's review->fix->CI cycles
	// before it stops for human review. Defaults to 3 when unset.
	MaxLoopIterations int `json:"maxLoopIterations"`
	// Review, when true, has the agent review the branch's diff and print
	// findings just before pushing. Auto-fix and no-op findings are advisory;
	// ask-user findings are decision points — the run refuses to push until
	// each is fixed, approved, or skipped by a human.
	Review bool `json:"review"`
	// BaseBranch is what the review diffs against, e.g. "main". The review
	// covers commits on the current branch since it diverged from BaseBranch.
	BaseBranch string `json:"baseBranch"`
	// ReviewGuidancePath points to a markdown file, resolved relative to the
	// repo root, whose contents customise the review criteria (what to flag, the
	// severity rubric, house style). Empty means the default location
	// (.review-lens.guidance.md); a missing file falls back to a built-in
	// default. The JSON findings format is fixed and not affected by this file.
	ReviewGuidancePath string `json:"reviewGuidancePath,omitempty"`
	// OpenPR, when true, runs `gh pr create` after a successful push.
	OpenPR bool `json:"openPR"`
	// JiraBaseURL is the Jira "browse" base, e.g.
	// "https://acme.atlassian.net/browse/". When set, opening a PR parses the
	// ticket key from the branch name (feat/oa-2576-… → OA-2576), prefixes the PR
	// title with "[OA-2576]", and adds a clickable "Jira:" link to the body. Empty
	// disables Jira linking (no key parsing), so non-Jira repos are unaffected.
	JiraBaseURL string `json:"jiraBaseURL,omitempty"`
}

// Default returns a language-agnostic starting config with placeholder checks.
// `init` and `configure` replace the checks via discovery (internal/discover);
// Default is the base for Load, so partial config files still work.
func Default() Config {
	return Config{
		Remote: "origin",
		Checks: []Check{
			// Placeholder — replace with your project's real checks.
			{Name: "example", Cmd: []string{"echo", "configure your checks in .review-lens.json"}},
		},
		// stream-json lets review-lens show Claude's activity live (files read,
		// commands run) instead of a silent wait. --verbose is required by
		// Claude when stream-json is used with -p. acceptEdits lets the fix step
		// edit files without an interactive prompt (safe: only in the worktree).
		// --include-partial-messages streams thinking/text token-by-token, so the
		// live feed updates continuously instead of only when a (possibly very
		// long) thinking block finishes.
		Agent:              ClaudeAgent(),
		MaxAgentAttempts:   2,
		MaxLoopIterations:  3,
		Review:             true,
		BaseBranch:         "main",
		OpenPR:             true,
		ReviewGuidancePath: guidance.DefaultPath,
	}
}

// ClaudeAgent returns the non-interactive Claude Code configuration used by
// init when Claude is selected.
func ClaudeAgent() *Agent {
	return &Agent{Cmd: []string{
		"claude", "-p",
		"--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--permission-mode", "acceptEdits",
	}}
}

// CodexAgent returns a non-interactive Codex configuration that can edit the
// disposable worktree without pausing for an approval prompt. --json makes
// Codex emit JSONL events, which drive the live activity feed and keep the
// final answer separate from transport diagnostics.
func CodexAgent() *Agent {
	return &Agent{Cmd: []string{
		"codex", "--ask-for-approval", "never",
		"exec", "--sandbox", "workspace-write", "--json",
	}}
}

// Placeholder reports whether a check is the starter example Default writes —
// a command that always passes without validating anything. A gate made only
// of placeholders must not count as green, so callers use this to fail closed.
func Placeholder(c Check) bool {
	return len(c.Cmd) > 0 && c.Cmd[0] == "echo"
}

// MeaningfulChecks reports whether the config contains at least one check that
// actually validates code (i.e. isn't a placeholder).
func MeaningfulChecks(cfg Config) bool {
	for _, c := range cfg.Checks {
		if !Placeholder(c) {
			return true
		}
	}
	return false
}

// Load reads config from path. If the file does not exist it returns Default()
// so the tool is usable before you've written a config.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("reading config %s: %w", path, err)
	}
	cfg := Default() // start from defaults so partial files still work
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing config %s: %w", path, err)
	}
	if cfg.Remote == "" {
		cfg.Remote = "origin"
	}
	return cfg, nil
}

// Save writes cfg to path as pretty-printed JSON. Used by `gate init`.
func Save(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
