// Package discover proposes a validation gate from what a repository actually
// contains. It looks only at *tracked* files — the same view a disposable
// worktree gets — so ignored local tooling (a stray root package.json) can
// never decide the validation stack. Each ecosystem lives behind one adapter,
// so new stacks are added here without touching the run pipeline.
package discover

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/izstoev10/review-lens/internal/config"
)

// Proposal is one suggested command for the gate, with enough provenance for a
// human to judge it.
type Proposal struct {
	Check config.Check
	// Setup marks dependency/bootstrap commands (npm ci, mix deps.get). They
	// run before the check loop and their failures are environment problems,
	// never agent work.
	Setup bool
	// HighConfidence marks proposals safe to accept without a human: the
	// manifest names the command and the executable is installed. Only these
	// survive a non-interactive configuration.
	HighConfidence bool
	// Source is the tracked file that justified the proposal, e.g.
	// "server/mix.exs" or ".github/workflows/ci.yml".
	Source string
}

// Repo is the discovery input: the repo root plus injected dependencies, so
// tests can fabricate any layout and toolchain without touching PATH.
type Repo struct {
	Root     string
	Tracked  []string                     // repo-relative tracked paths
	LookPath func(string) (string, error) // normally exec.LookPath
}

// tracked reports whether a repo-relative path is in the tracked list.
func (r Repo) tracked(path string) bool {
	for _, t := range r.Tracked {
		if t == path {
			return true
		}
	}
	return false
}

// read returns a tracked file's contents, or "" — discovery treats an
// unreadable manifest the same as an absent one.
func (r Repo) read(path string) string {
	if !r.tracked(path) {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(r.Root, path))
	if err != nil {
		return ""
	}
	return string(data)
}

// installed reports whether an executable can be resolved.
func (r Repo) installed(bin string) bool {
	_, err := r.LookPath(bin)
	return err == nil
}

// An adapter proposes the gate for one ecosystem. marker is the manifest file
// that identifies a project root; propose is called once per directory that
// contains a tracked marker.
type adapter struct {
	marker  string
	propose func(r Repo, dir string) []Proposal
}

// adapters is the ecosystem registry. Order sets proposal order when several
// stacks share one directory.
var adapters = []adapter{
	{"go.mod", proposeGo},
	{"package.json", proposeNode},
	{"mix.exs", proposeElixir},
	{"Cargo.toml", proposeRust},
	{"pyproject.toml", proposePython},
	{"Makefile", proposeMake},
}

// Discover walks the tracked manifests and returns the proposed gate, plus
// human-readable notes (e.g. CI features that cannot be reproduced locally).
//
// A repository-owned gate that CI itself invokes (make ci, scripts/check) is
// the preferred shape — when found, it leads the list so developers, CI and
// review-lens all run the same command.
func Discover(r Repo) (proposals []Proposal, notes []string) {
	ciProposals, ciNotes := inspectCI(r)
	proposals = append(proposals, ciProposals...)
	notes = append(notes, ciNotes...)

	for _, dir := range projectDirs(r) {
		for _, a := range adapters {
			marker := filepath.Join(dir, a.marker)
			if r.tracked(marker) {
				proposals = append(proposals, a.propose(r, dir)...)
			}
		}
	}
	return dedupe(proposals), notes
}

// projectDirs lists every directory that holds a tracked ecosystem marker, in
// stable path order (repo root first, then nested projects).
func projectDirs(r Repo) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, t := range r.Tracked {
		base := filepath.Base(t)
		for _, a := range adapters {
			if base == a.marker {
				dir := filepath.Dir(t)
				if dir == "." {
					dir = ""
				}
				if !seen[dir] {
					seen[dir] = true
					dirs = append(dirs, dir)
				}
			}
		}
	}
	sort.Strings(dirs)
	return dirs
}

// dedupe collapses proposals whose directory and argv repeat an earlier one —
// two CI workflows invoking the same make target should propose it once. The
// first occurrence keeps its position, but a later high-confidence duplicate
// upgrades it: a plain CI step must never shadow the same command proposed
// with confidence by an adapter, or a non-interactive run would reject a gate
// it should have accepted.
func dedupe(ps []Proposal) []Proposal {
	at := map[string]int{}
	var out []Proposal
	for _, p := range ps {
		key := p.Check.Dir + "\x00" + strings.Join(p.Check.Cmd, "\x00")
		if i, dup := at[key]; dup {
			if p.HighConfidence && !out[i].HighConfidence {
				out[i] = p // the confident proposal wins, in the earlier slot
			}
			continue
		}
		at[key] = len(out)
		out = append(out, p)
	}
	return out
}

// --- ecosystem adapters ----------------------------------------------------

func proposeGo(r Repo, dir string) []Proposal {
	src := filepath.Join(dir, "go.mod")
	ok := r.installed("go")
	return []Proposal{
		{Check: config.Check{Name: "build", Cmd: []string{"go", "build", "./..."}, Dir: dir}, HighConfidence: ok, Source: src},
		{Check: config.Check{Name: "test", Cmd: []string{"go", "test", "./..."}, Dir: dir}, HighConfidence: ok, Source: src},
	}
}

// proposeNode reads the manifest and proposes only scripts that exist, so a
// generated check can never die with npm's "Missing script".
func proposeNode(r Repo, dir string) []Proposal {
	src := filepath.Join(dir, "package.json")
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal([]byte(r.read(src)), &manifest); err != nil {
		return nil
	}
	ok := r.installed("npm")

	var ps []Proposal
	// npm ci needs the lockfile; without one, install is the honest bootstrap.
	install := "install"
	if r.tracked(filepath.Join(dir, "package-lock.json")) {
		install = "ci"
	}
	ps = append(ps, Proposal{
		Check: config.Check{Name: "deps", Cmd: []string{"npm", install}, Dir: dir},
		Setup: true, HighConfidence: ok, Source: src,
	})
	if _, has := manifest.Scripts["lint"]; has {
		ps = append(ps, Proposal{Check: config.Check{Name: "lint", Cmd: []string{"npm", "run", "lint"}, Dir: dir}, HighConfidence: ok, Source: src})
	}
	if _, has := manifest.Scripts["test"]; has {
		ps = append(ps, Proposal{Check: config.Check{Name: "test", Cmd: []string{"npm", "test"}, Dir: dir}, HighConfidence: ok, Source: src})
	}
	if len(ps) == 1 { // only the bootstrap — nothing to validate with
		return nil
	}
	return ps
}

// proposeElixir uses check-only Mix modes: formatting is verified, never
// rewritten, so a gate can't silently mutate the branch it validates.
func proposeElixir(r Repo, dir string) []Proposal {
	src := filepath.Join(dir, "mix.exs")
	ok := r.installed("mix")
	return []Proposal{
		{Check: config.Check{Name: "deps", Cmd: []string{"mix", "deps.get"}, Dir: dir}, Setup: true, HighConfidence: ok, Source: src},
		{Check: config.Check{Name: "format", Cmd: []string{"mix", "format", "--check-formatted"}, Dir: dir}, HighConfidence: ok, Source: src},
		{Check: config.Check{Name: "compile", Cmd: []string{"mix", "compile", "--warnings-as-errors"}, Dir: dir}, HighConfidence: ok, Source: src},
		{Check: config.Check{Name: "test", Cmd: []string{"mix", "test"}, Dir: dir}, HighConfidence: ok, Source: src},
	}
}

func proposeRust(r Repo, dir string) []Proposal {
	src := filepath.Join(dir, "Cargo.toml")
	ok := r.installed("cargo")
	return []Proposal{
		{Check: config.Check{Name: "build", Cmd: []string{"cargo", "build"}, Dir: dir}, HighConfidence: ok, Source: src},
		{Check: config.Check{Name: "test", Cmd: []string{"cargo", "test"}, Dir: dir}, HighConfidence: ok, Source: src},
	}
}

// proposeMake offers the project's own gate target when the Makefile declares
// one, preferring the canonical names. This keeps Makefile-only repos (no CI
// workflow to reveal the gate) discoverable.
func proposeMake(r Repo, dir string) []Proposal {
	src := filepath.Join(dir, "Makefile")
	for _, target := range []string{"ci", "check", "test"} {
		if makefileTarget(r, src, target) {
			return []Proposal{{
				Check:          config.Check{Name: "make " + target, Cmd: []string{"make", target}, Dir: dir},
				HighConfidence: r.installed("make"),
				Source:         src,
			}}
		}
	}
	return nil
}

// proposePython's tools aren't implied by the manifest the way go/cargo are,
// so each check is tied to its own executable being installed.
func proposePython(r Repo, dir string) []Proposal {
	src := filepath.Join(dir, "pyproject.toml")
	var ps []Proposal
	if r.installed("ruff") {
		ps = append(ps, Proposal{Check: config.Check{Name: "lint", Cmd: []string{"ruff", "check", "."}, Dir: dir}, HighConfidence: true, Source: src})
	}
	if r.installed("pytest") {
		ps = append(ps, Proposal{Check: config.Check{Name: "test", Cmd: []string{"pytest"}, Dir: dir}, HighConfidence: true, Source: src})
	}
	return ps
}
