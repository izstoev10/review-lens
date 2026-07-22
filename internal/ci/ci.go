// Package ci reads GitHub CI status for a pull request via the gh CLI, so the
// auto-fix loop can wait for checks to go green (or catch them going red).
package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
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

// checkRun is one entry of GitHub's statusCheckRollup.
type checkRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`     // QUEUED | IN_PROGRESS | COMPLETED (checks) or "" (statuses)
	Conclusion string `json:"conclusion"` // SUCCESS | FAILURE | ... (checks)
	State      string `json:"state"`      // SUCCESS | FAILURE | PENDING (legacy commit statuses)
}

// classifyRollup reduces a set of check runs to a single Status. Pure function,
// unit-tested. A failure anywhere wins; otherwise any still-running check keeps
// it pending; an empty set is treated as success (nothing gates the PR).
func classifyRollup(runs []checkRun) (Status, []string) {
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
func Query(dir, prNumber string) (Status, []string, error) {
	args := []string{"pr", "view"}
	if prNumber != "" {
		args = append(args, prNumber)
	}
	args = append(args, "--json", "statusCheckRollup")
	cmd := exec.Command("gh", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return Pending, nil, fmt.Errorf("gh pr view failed: %w", err)
	}
	var payload struct {
		StatusCheckRollup []checkRun `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return Pending, nil, fmt.Errorf("parsing statusCheckRollup: %w", err)
	}
	status, failing := classifyRollup(payload.StatusCheckRollup)
	return status, failing, nil
}

// Poll queries CI repeatedly until it is conclusive (Success/Failure) or the
// timeout elapses. progress is called with each intermediate status line.
func Poll(dir, prNumber string, interval, timeout time.Duration, progress func(string)) (Status, []string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		status, failing, err := Query(dir, prNumber)
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
