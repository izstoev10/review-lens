package pipeline

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/izstoev10/review-lens/internal/config"
)

func gate(checks ...config.Check) config.Config {
	cfg := config.Default()
	cfg.Checks = checks
	return cfg
}

// An empty or placeholder gate must never look green: run refuses before any
// check, agent, or push is reached.
func TestPreflightRefusesAMeaninglessGate(t *testing.T) {
	dir := t.TempDir()
	for name, cfg := range map[string]config.Config{
		"no checks at all": gate(),
		"placeholder only": config.Default(), // the echo starter check
	} {
		t.Run(name, func(t *testing.T) {
			err := preflight(dir, cfg)
			if err == nil {
				t.Fatal("preflight passed a gate that validates nothing")
			}
			if !strings.Contains(err.Error(), "review-lens configure") {
				t.Errorf("err = %v, want a pointer to the configurator", err)
			}
		})
	}
}

// A broken gate names its broken piece — check, directory, or executable — so
// the user can repair configuration instead of decoding a downstream failure.
func TestPreflightNamesTheInvalidPiece(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		cfg  config.Config
		want []string
	}{
		{
			name: "missing working directory",
			cfg:  gate(config.Check{Name: "test", Cmd: []string{"true"}, Dir: "missing-dir"}),
			want: []string{`"test"`, "missing-dir", "review-lens configure"},
		},
		{
			name: "missing executable",
			cfg:  gate(config.Check{Name: "lint", Cmd: []string{"review-lens-no-such-binary"}}),
			want: []string{`"lint"`, "review-lens-no-such-binary"},
		},
		{
			name: "missing repo script",
			cfg:  gate(config.Check{Name: "ci", Cmd: []string{"./scripts/check"}}),
			want: []string{`"ci"`, "scripts/check"},
		},
		{
			name: "setup command is preflighted too",
			cfg: func() config.Config {
				cfg := gate(config.Check{Name: "test", Cmd: []string{"true"}})
				cfg.Setup = []config.Check{{Name: "deps", Cmd: []string{"review-lens-no-such-binary"}}}
				return cfg
			}(),
			want: []string{`"deps"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := preflight(dir, tt.cfg)
			if err == nil {
				t.Fatal("preflight passed an unrunnable gate")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to contain %q", err, want)
				}
			}
		})
	}
}

func TestPreflightAcceptsARunnableGate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := gate(config.Check{Name: "test", Cmd: []string{"true"}, Dir: "app"})
	cfg.Setup = []config.Check{{Name: "deps", Cmd: []string{"true"}, Dir: "app"}}
	if err := preflight(dir, cfg); err != nil {
		t.Errorf("preflight rejected a runnable gate: %v", err)
	}
}

// A setup failure is the environment's fault. It is reported with the output
// attached and the run stops — the fixing agent must never be asked to edit
// code over it.
func TestSetupFailureIsAnEnvironmentError(t *testing.T) {
	cfg := config.Default()
	cfg.Setup = []config.Check{{Name: "deps", Cmd: []string{"sh", "-c", "echo lockfile out of date; exit 1"}}}

	var log bytes.Buffer
	err := runSetup(t.TempDir(), cfg, &log)

	if err == nil {
		t.Fatal("a failing setup command should fail the run")
	}
	for _, want := range []string{"deps", "environment", "lockfile out of date"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %q", err, want)
		}
	}
}

// A check that cannot run (here: npm's missing-script signature) is invalid
// configuration. The loop must surface that and stop — not hand it to the
// agent as a code defect, which burns fix attempts on an unfixable failure.
func TestCheckAndFixRoutesConfigProblemsToTheUser(t *testing.T) {
	cfg := gate(config.Check{
		Name: "lint",
		Cmd:  []string{"sh", "-c", `echo 'npm error Missing script: "lint"'; exit 1`},
	})
	// A real agent invocation would fail loudly with "agent fix failed".
	cfg.Agent = &config.Agent{Cmd: []string{"review-lens-no-such-binary"}}

	var log bytes.Buffer
	_, err := checkAndFix(t.TempDir(), cfg, &log)

	if err == nil {
		t.Fatal("an unrunnable check should fail the run")
	}
	if strings.Contains(err.Error(), "agent") {
		t.Fatalf("the agent was invoked for a configuration problem: %v", err)
	}
	if !strings.Contains(err.Error(), "review-lens configure") {
		t.Errorf("err = %v, want a pointer to the configurator", err)
	}
}

// The counterpart to the config-problem test: a genuine code failure must
// reach the agent seam. The fake agent proves its invocation by dropping a
// marker file.
func TestCheckAndFixSendsGenuineFailuresToTheAgent(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "agent-was-here")

	cfg := gate(config.Check{Name: "test", Cmd: []string{"sh", "-c", "echo assertion failed; exit 1"}})
	cfg.MaxAgentAttempts = 1
	cfg.Agent = &config.Agent{Cmd: []string{"sh", "-c", "touch " + marker}}

	var log bytes.Buffer
	fixed, err := checkAndFix(dir, cfg, &log)

	if err == nil {
		t.Fatal("the check never passes, so the loop must eventually fail")
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Error("a genuine check failure never reached the agent seam")
	}
	// The caller writes the commit message from these names (#39).
	if len(fixed) != 1 || fixed[0] != "test" {
		t.Errorf("fixed = %v, want the failed check's name exactly once", fixed)
	}
	if !strings.Contains(err.Error(), "still failing") {
		t.Errorf("err = %v, want the attempts-exhausted failure, not a config error", err)
	}
}
