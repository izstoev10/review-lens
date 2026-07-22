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
type Check struct {
	Name string   `json:"name"` // human label, e.g. "test"
	Cmd  []string `json:"cmd"`  // argv, e.g. ["go", "test", "./..."]
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
	// findings just before pushing. Findings are advisory — they do not block
	// the push (the checks are the gate).
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
}

// Default returns a language-agnostic starting config with placeholder checks.
// `gate init` normally calls Detect instead, which fills in checks based on what
// it finds in the repo; Default is the fallback and the base for Load.
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
		Agent: &Agent{Cmd: []string{
			"claude", "-p",
			"--output-format", "stream-json", "--verbose", "--include-partial-messages",
			"--permission-mode", "acceptEdits",
		}},
		MaxAgentAttempts:   2,
		MaxLoopIterations:  3,
		Review:             true,
		BaseBranch:         "main",
		OpenPR:             true,
		ReviewGuidancePath: guidance.DefaultPath,
	}
}

// Detect inspects the repo root for well-known project markers and returns a
// config with matching starter checks. The tool works with any language — this
// just saves you writing the common cases by hand. Falls back to Default().
//
// exists is injected so this is trivially testable, but callers normally pass
// a function backed by os.Stat.
func Detect(exists func(name string) bool) Config {
	cfg := Default()
	switch {
	case exists("go.mod"):
		cfg.Checks = []Check{
			{Name: "build", Cmd: []string{"go", "build", "./..."}},
			{Name: "test", Cmd: []string{"go", "test", "./..."}},
		}
	case exists("package.json"):
		cfg.Checks = []Check{
			{Name: "lint", Cmd: []string{"npm", "run", "lint"}},
			{Name: "test", Cmd: []string{"npm", "test"}},
		}
	case exists("Cargo.toml"):
		cfg.Checks = []Check{
			{Name: "build", Cmd: []string{"cargo", "build"}},
			{Name: "test", Cmd: []string{"cargo", "test"}},
		}
	case exists("pyproject.toml"), exists("requirements.txt"), exists("setup.py"):
		cfg.Checks = []Check{
			{Name: "lint", Cmd: []string{"ruff", "check", "."}},
			{Name: "test", Cmd: []string{"pytest"}},
		}
	case exists("Makefile"):
		cfg.Checks = []Check{
			{Name: "test", Cmd: []string{"make", "test"}},
		}
	}
	return cfg
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
