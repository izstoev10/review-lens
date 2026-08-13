// Package ci reads GitHub CI status for a pull request, so the auto-fix loop
// can wait for checks to go green (or catch them going red).
package ci

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/izstoev10/review-lens/internal/gh"
)

// Status is the overall conclusion of a PR's checks.
type Status int

const (
	Pending Status = iota // at least one check still running/queued
	Success               // all checks completed successfully (or none exist)
	Failure               // at least one check failed
)

func (s Status) String() string {
	switch s {
	case Success:
		return "success"
	case Failure:
		return "failure"
	default:
		return "pending"
	}
}

// classifyRollup reduces a set of check runs to a single Status. Pure function,
// unit-tested. A failure anywhere wins; otherwise any still-running check keeps
// it pending; an empty set is treated as success (nothing gates the PR).
func classifyRollup(runs []gh.CheckRun) (Status, []string) {
	var failing []string
	pending := false
	for _, r := range runs {
		concl := strings.ToUpper(r.Conclusion)
		state := strings.ToUpper(r.State)
		switch {
		case concl == "FAILURE" || concl == "TIMED_OUT" || concl == "CANCELLED" || concl == "STARTUP_FAILURE" || state == "FAILURE" || state == "ERROR":
			failing = append(failing, r.Name)
		case r.Status != "" && r.Status != "COMPLETED": // check run not done yet
			pending = true
		case r.Status == "" && state != "" && state != "SUCCESS": // legacy status not done
			pending = true
		}
	}
	switch {
	case len(failing) > 0:
		return Failure, failing
	case pending:
		return Pending, nil
	default:
		return Success, nil
	}
}

// Query returns the current CI status for a PR (empty prNumber = current branch).
func Query(c gh.Client, prNumber string) (Status, []string, error) {
	runs, err := c.CheckRollup(prNumber)
	if err != nil {
		return Pending, nil, err
	}
	status, failing := classifyRollup(runs)
	return status, failing, nil
}

// Poll queries CI repeatedly until it is conclusive (Success/Failure) or the
// timeout elapses. progress is called with each intermediate status line.
func Poll(c gh.Client, prNumber string, interval, timeout time.Duration, progress func(string)) (Status, []string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		status, failing, err := Query(c, prNumber)
		if err != nil {
			return Pending, nil, err
		}
		if status != Pending {
			return status, failing, nil
		}
		if progress != nil {
			progress("CI still running…")
		}
		select {
		case <-ctx.Done():
			return Pending, nil, fmt.Errorf("timed out waiting for CI after %s", timeout)
		case <-time.After(interval):
		}
	}
}
