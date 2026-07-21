// Package checks runs the configured validation commands inside a directory
// (normally the disposable worktree) and reports structured results.
package checks

import (
	"os/exec"

	"github.com/izstoev10/review-lens/internal/config"
)

// Result is the outcome of running one check.
type Result struct {
	Name   string // check name from config
	Passed bool   // true if the command exited 0
	Output string // combined stdout+stderr, used as agent context on failure
}

// Run executes one check in dir.
func Run(dir string, c config.Check) Result {
	// #nosec G204 — the command comes from the user's own config file, by design.
	cmd := exec.Command(c.Cmd[0], c.Cmd[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return Result{
		Name:   c.Name,
		Passed: err == nil,
		Output: string(out),
	}
}

// RunAll runs checks in order and stops at the first failure (fail-fast), since
// the next step is to have the agent fix that failure before continuing.
// It returns every result gathered so far; the last one is the failure if
// allPassed is false.
func RunAll(dir string, cs []config.Check) (results []Result, allPassed bool) {
	for _, c := range cs {
		r := Run(dir, c)
		results = append(results, r)
		if !r.Passed {
			return results, false
		}
	}
	return results, true
}
