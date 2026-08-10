package setup

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/izstoev10/review-lens/internal/config"
	"github.com/izstoev10/review-lens/internal/discover"
)

// osMkdirWrite writes content to full, creating parent directories.
func osMkdirWrite(full, content string) error {
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

// fixtureRepo builds a disposable git repo: files are written and committed,
// ignored entries are written but listed in .gitignore — present on disk yet
// invisible to `git ls-files`, exactly like local-only tooling.
func fixtureRepo(t *testing.T, files map[string]string, ignored map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")

	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := osMkdirWrite(full, content); err != nil {
			t.Fatal(err)
		}
	}
	var ignores []string
	for rel, content := range ignored {
		write(rel, content)
		ignores = append(ignores, "/"+rel)
	}
	if len(ignores) > 0 {
		write(".gitignore", strings.Join(ignores, "\n")+"\n")
	}
	for rel, content := range files {
		write(rel, content)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "fixture")
	return dir
}

// configureRepo runs the exact workflow init and configure share: discover
// from tracked files, then confirm. Tests inspect the resulting config only.
func configureRepo(t *testing.T, root string, cfg config.Config, input string, interactive bool, installed ...string) (config.Config, bool, string) {
	t.Helper()
	proposals, notes, err := Propose(root, pathLookup(installed...))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	got, configured, err := Configure(cfg, proposals, notes, strings.NewReader(input), &out, interactive)
	if err != nil {
		t.Fatal(err)
	}
	return got, configured, out.String()
}

// The originating failure: an ignored root package.json (a local prototype
// launcher) must not decide the stack when the tracked application is an
// Elixir project under server/.
func TestIgnoredRootManifestLosesToTrackedProject(t *testing.T) {
	root := fixtureRepo(t,
		map[string]string{"server/mix.exs": "defmodule P.MixProject do\nend\n"},
		map[string]string{"package.json": `{"scripts":{"lint":"x","test":"x"}}`},
	)

	cfg, configured, _ := configureRepo(t, root, config.Default(), "", false, "mix", "npm")

	if !configured {
		t.Fatal("a tracked mix.exs with mix installed should configure with confidence")
	}
	all := append(append([]config.Check{}, cfg.Setup...), cfg.Checks...)
	for _, c := range all {
		if c.Cmd[0] == "npm" {
			t.Errorf("ignored package.json produced check %v — untracked tooling must not win", c.Cmd)
		}
		if c.Dir != "server" {
			t.Errorf("check %q has dir %q, want %q", c.Name, c.Dir, "server")
		}
	}
	joined := flatten(cfg.Checks)
	for _, want := range []string{"mix format --check-formatted", "mix compile --warnings-as-errors", "mix test"} {
		if !strings.Contains(joined, want) {
			t.Errorf("checks = %q, want them to include %q", joined, want)
		}
	}
	if flatten(cfg.Setup) != "mix deps.get" {
		t.Errorf("setup = %q, want mix deps.get", flatten(cfg.Setup))
	}
}

// Only npm scripts that exist in the manifest may be proposed — a generated
// `npm test` against a script-less manifest fails with "Missing script".
func TestNodeProposesOnlyExistingScripts(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"package.json":      `{"scripts":{"lint":"eslint ."}}`,
		"package-lock.json": `{}`,
	}, nil)

	cfg, _, _ := configureRepo(t, root, config.Default(), "", false, "npm")

	if got := flatten(cfg.Checks); got != "npm run lint" {
		t.Errorf("checks = %q, want only the existing lint script", got)
	}
	if got := flatten(cfg.Setup); got != "npm ci" {
		t.Errorf("setup = %q, want npm ci (lockfile present)", got)
	}
}

func TestMultipleProjectsKeepDirsAndOrder(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"api/go.mod":       "module api\n",
		"web/package.json": `{"scripts":{"test":"jest"}}`,
	}, nil)

	cfg, _, _ := configureRepo(t, root, config.Default(), "", false, "go", "npm")

	type want struct{ dir, cmd string }
	wants := []want{
		{"api", "go build ./..."},
		{"api", "go test ./..."},
		{"web", "npm test"},
	}
	if len(cfg.Checks) != len(wants) {
		t.Fatalf("got %d checks (%q), want %d", len(cfg.Checks), flatten(cfg.Checks), len(wants))
	}
	for i, w := range wants {
		if got := strings.Join(cfg.Checks[i].Cmd, " "); got != w.cmd || cfg.Checks[i].Dir != w.dir {
			t.Errorf("check %d = %q in %q, want %q in %q", i, got, cfg.Checks[i].Dir, w.cmd, w.dir)
		}
	}
}

// When CI already invokes a repository-owned gate, that command is the
// preferred proposal: one command for developers, CI and review-lens.
func TestCIRepoOwnedGateIsProposed(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		"Makefile": "ci:\n\tgo test ./...\n",
		".github/workflows/ci.yml": strings.Join([]string{
			"jobs:",
			"  test:",
			"    steps:",
			"      - uses: actions/checkout@v4",
			"      - run: make ci",
		}, "\n"),
	}, nil)

	cfg, configured, out := configureRepo(t, root, config.Default(), "", false, "make")

	if !configured {
		t.Fatal("a CI-invoked make target should configure with confidence")
	}
	if got := flatten(cfg.Checks); got != "make ci" {
		t.Errorf("checks = %q, want the repo-owned gate only", got)
	}
	if !strings.Contains(out, "uses") {
		t.Errorf("output %q should note the workflow's unsupported features", out)
	}
}

// A workflow built from actions, services and templating offers nothing that
// can honestly run locally; it must produce notes, not guessed commands.
func TestComplexCIIsNotTranslated(t *testing.T) {
	root := fixtureRepo(t, map[string]string{
		".github/workflows/ci.yml": strings.Join([]string{
			"jobs:",
			"  test:",
			"    services:",
			"      postgres:",
			"        image: postgres:16",
			"    steps:",
			"      - uses: actions/checkout@v4",
			"      - run: ./deploy --key ${{ secrets.KEY }}",
		}, "\n"),
	}, nil)

	existing := config.Default()
	cfg, configured, out := configureRepo(t, root, existing, "", false)

	if configured {
		t.Errorf("nothing here is reproducible, yet checks were configured: %q", flatten(cfg.Checks))
	}
	if flatten(cfg.Checks) != flatten(existing.Checks) {
		t.Errorf("checks = %q, want the input untouched", flatten(cfg.Checks))
	}
	for _, feat := range []string{"services", "uses"} {
		if !strings.Contains(out, feat) {
			t.Errorf("output %q should identify the unsupported %q feature", out, feat)
		}
	}
	if !strings.Contains(out, "review-lens configure") {
		t.Errorf("output %q should point at the interactive configurator", out)
	}
}

// The interactive loop lets the developer shape the gate: drop a proposal,
// move another, add a custom command for a stack discovery doesn't know.
func TestInteractiveEditDropMoveAdd(t *testing.T) {
	root := fixtureRepo(t, map[string]string{"go.mod": "module m\n"}, nil)

	input := strings.Join([]string{
		"d 1",            // drop go build
		"a",              // add a custom check…
		"integration",    //   name
		"e2e",            //   directory
		"just check-all", //   command
		"m 2 1",          // move it ahead of go test
		"",               // accept
	}, "\n") + "\n"
	cfg, configured, _ := configureRepo(t, root, config.Default(), input, true, "go")

	if !configured {
		t.Fatal("an accepted interactive session should configure the gate")
	}
	want := []string{"just check-all", "go test ./..."}
	if len(cfg.Checks) != 2 || strings.Join(cfg.Checks[0].Cmd, " ") != want[0] || strings.Join(cfg.Checks[1].Cmd, " ") != want[1] {
		t.Fatalf("checks = %q, want %q", flatten(cfg.Checks), strings.Join(want, ", "))
	}
	if cfg.Checks[0].Dir != "e2e" || cfg.Checks[0].Name != "integration" {
		t.Errorf("custom check = %+v, want name integration in dir e2e", cfg.Checks[0])
	}
}

// Non-interactive configuration must never write speculative checks: with no
// high-confidence discovery it leaves the gate unconfigured and says how to
// finish the job, rather than blocking or guessing.
func TestNonInteractiveWithoutConfidenceLeavesUnconfigured(t *testing.T) {
	// A Python project, but neither ruff nor pytest is installed.
	root := fixtureRepo(t, map[string]string{"pyproject.toml": "[project]\nname='p'\n"}, nil)

	cfg, configured, out := configureRepo(t, root, config.Default(), "", false)

	if configured {
		t.Errorf("expected an unconfigured gate, got checks %q setup %q", flatten(cfg.Checks), flatten(cfg.Setup))
	}
	if !strings.Contains(out, "review-lens configure") {
		t.Errorf("output %q should explain how to configure interactively", out)
	}
}

// A blind (non-interactive) re-run that discovers nothing with confidence must
// leave an existing gate intact — wiping it would destroy a working
// configuration the moment the toolchain is missing from PATH.
func TestNonInteractiveWithoutConfidencePreservesExistingGate(t *testing.T) {
	// A Python project whose tools aren't installed in this environment.
	root := fixtureRepo(t, map[string]string{"pyproject.toml": "[project]\nname='p'\n"}, nil)

	existing := config.Default()
	existing.Setup = []config.Check{{Name: "deps", Cmd: []string{"uv", "sync"}}}
	existing.Checks = []config.Check{{Name: "test", Cmd: []string{"uv", "run", "pytest"}}}

	cfg, configured, _ := configureRepo(t, root, existing, "", false)

	if configured {
		t.Fatal("nothing was discovered with confidence, yet the run claims it configured the gate")
	}
	if flatten(cfg.Checks) != flatten(existing.Checks) || flatten(cfg.Setup) != flatten(existing.Setup) {
		t.Errorf("existing gate was modified: checks %q setup %q", flatten(cfg.Checks), flatten(cfg.Setup))
	}
}

// Reconfiguration owns only the gate. Agent, remote, review and PR settings
// belong to the user and survive untouched.
func TestReconfigurePreservesOtherSettings(t *testing.T) {
	root := fixtureRepo(t, map[string]string{"go.mod": "module m\n"}, nil)

	existing := config.Default()
	existing.Agent = &config.Agent{Cmd: []string{"my-agent", "--flag"}}
	existing.Remote = "upstream"
	existing.Review = false
	existing.OpenPR = false
	existing.JiraBaseURL = "https://acme.atlassian.net/browse/"
	existing.Checks = []config.Check{{Name: "old", Cmd: []string{"false"}}}

	cfg, _, _ := configureRepo(t, root, existing, "", false, "go")

	if cfg.Remote != "upstream" || cfg.Review || cfg.OpenPR || cfg.JiraBaseURL != existing.JiraBaseURL {
		t.Errorf("non-check settings changed: %+v", cfg)
	}
	if cfg.Agent == nil || cfg.Agent.Cmd[0] != "my-agent" {
		t.Errorf("agent setting changed: %+v", cfg.Agent)
	}
	if strings.Contains(flatten(cfg.Checks), "false") {
		t.Errorf("stale checks survived reconfiguration: %q", flatten(cfg.Checks))
	}
}

func flatten(cs []config.Check) string {
	var parts []string
	for _, c := range cs {
		parts = append(parts, strings.Join(c.Cmd, " "))
	}
	return strings.Join(parts, ", ")
}

// The edit grammar is where the splice arithmetic lives; every command and
// every rejected input is a table row, no scripted session needed.
func TestApplyEdit(t *testing.T) {
	three := func() []discover.Proposal {
		return []discover.Proposal{
			{Check: config.Check{Name: "a"}},
			{Check: config.Check{Name: "b"}},
			{Check: config.Check{Name: "c"}},
		}
	}
	names := func(ps []discover.Proposal) string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Check.Name)
		}
		return strings.Join(out, "")
	}

	tests := []struct {
		line       string
		want       string
		wantAction editAction
	}{
		{"", "abc", editAccept},
		{"   ", "abc", editAccept},
		{"q", "abc", editAbort},
		{"a", "abc", editAdd},
		{"s", "abc", editAddSetup},
		{"d 1", "bc", editApplied},
		{"d 3", "ab", editApplied},
		{"d 4", "abc", editInvalid}, // out of range
		{"d 0", "abc", editInvalid},
		{"d x", "abc", editInvalid},
		{"m 3 1", "cab", editApplied},
		{"m 1 3", "bca", editApplied},
		{"m 2 2", "abc", editApplied}, // no-op move is legal
		{"m 1 4", "abc", editInvalid},
		{"nonsense", "abc", editInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got, action := applyEdit(three(), tt.line)
			if action != tt.wantAction {
				t.Errorf("action = %v, want %v", action, tt.wantAction)
			}
			if names(got) != tt.want {
				t.Errorf("list = %q, want %q", names(got), tt.want)
			}
		})
	}
}
