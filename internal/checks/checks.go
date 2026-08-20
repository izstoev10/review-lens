// Package checks runs the configured validation commands inside a directory
// (normally the disposable worktree) and reports structured results.
package checks

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/izstoev10/review-lens/internal/config"
)

// Result is the outcome of running one check.
type Result struct {
	Name   string // check name from config
	Passed bool   // true if the command exited 0
	Output string // combined stdout+stderr, used as agent context on failure
	// ConfigProblem, when non-empty, says the failure is environment or
	// configuration — a missing executable, working directory, or npm script —
	// not a defect in the code. Such failures must be shown to the user, never
	// handed to the fixing agent as if the branch were broken.
	ConfigProblem string
}

// Run executes one check under base (the repo root or worktree), inside the
// check's own working directory when it declares one.
//
// Run is total over config.Check: a check with an empty argv is reported as a
// configuration problem, the same classification as a missing executable or
// working directory — never a panic, whichever entry point built the Check.
func Run(base string, c config.Check) Result {
	if len(c.Cmd) == 0 {
		return Result{Name: c.Name, ConfigProblem: "the check has no command — run `review-lens configure` to repair the gate"}
	}
	dir := filepath.Join(base, c.Dir)
	if _, err := os.Stat(dir); err != nil {
		return Result{Name: c.Name, ConfigProblem: fmt.Sprintf("working directory %q does not exist", c.Dir)}
	}
	// #nosec G204 — the command comes from the user's own config file, by design.
	cmd := exec.Command(c.Cmd[0], c.Cmd[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	r := Result{
		Name:   c.Name,
		Passed: err == nil,
		Output: string(out),
	}
	if err != nil {
		r.ConfigProblem = classify(err, r.Output)
	}
	return r
}

// classify recognises failures that no code change can fix. It is deliberately
// conservative: only unambiguous environment signatures qualify, because a
// false positive here would hide a real code failure from the agent.
func classify(err error, output string) string {
	if errors.Is(err, exec.ErrNotFound) {
		return "executable not found on PATH"
	}
	// npm's fixed wording when a configured script isn't in package.json.
	if strings.Contains(output, "Missing script:") {
		return "the npm script does not exist in package.json"
	}
	return ""
}

// RunAll runs checks in order and stops at the first failure (fail-fast), since
// the next step is to have the agent fix that failure before continuing.
// It returns every result gathered so far; the last one is the failure if
// allPassed is false.
func RunAll(base string, cs []config.Check) (results []Result, allPassed bool) {
	for _, c := range cs {
		r := Run(base, c)
		results = append(results, r)
		if !r.Passed {
			return results, false
		}
	}
	return results, true
}
