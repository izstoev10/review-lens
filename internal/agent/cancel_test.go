package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/izstoev10/review-lens/internal/config"
)

// streamingAgent builds a fake agent whose argv contains "stream-json", so
// CanStream picks the streaming code path — the one the TUI uses.
func streamingAgent(script string) *config.Agent {
	return &config.Agent{Cmd: []string{"sh", "-c", script, "stream-json"}}
}

// Cancelling must not merely abandon the agent: StreamFix has to return only
// once the process is gone. The `run` pipeline re-runs checks and commits the
// tree the instant the TUI returns, so an agent still writing files after
// cancellation would race those commits.
func TestStreamFixReturnsOnlyAfterTheAgentDies(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "late-write.txt")

	// Write a file well after cancellation would have happened. If the process
	// outlives StreamFix, the marker shows up and we've raced.
	a := streamingAgent("sleep 5; echo late > " + marker)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := StreamFix(ctx, dir, a, "prompt", nil)
	elapsed := time.Since(start)
	cancel()

	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("err = %v, want ErrCanceled", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("StreamFix took %s — it should return as soon as the agent is killed", elapsed)
	}

	// Give the (supposedly dead) process the time it would have needed to finish.
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the agent wrote a file after StreamFix returned — cancellation did not reap the process")
	}
}

// A cancel and a genuine agent failure must be distinguishable, so the UI can
// say "you stopped it" rather than reporting a crash the user caused.
func TestFailureIsNotReportedAsCancel(t *testing.T) {
	_, err := StreamFix(context.Background(), t.TempDir(), streamingAgent("exit 3"), "prompt", nil)
	if err == nil {
		t.Fatal("expected an error from a failing agent")
	}
	if errors.Is(err, ErrCanceled) {
		t.Errorf("a failing agent was misreported as canceled: %v", err)
	}
}
