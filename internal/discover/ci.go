package discover

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/izstoev10/review-lens/internal/config"
)

// CI workflows are evidence of the intended gate, but not a safe source of
// truth: they may lean on actions, services, matrices, secrets and runner
// state that don't exist locally. inspectCI therefore extracts only what is
// honestly reproducible — a repository-owned gate command the workflow invokes
// (make ci, scripts/check), plus plain single-line shell steps as low-
// confidence candidates — and names the unsupported features instead of
// guessing at them.

var (
	runLine = regexp.MustCompile(`(?m)^\s*(?:-\s+)?run:\s*(.+?)\s*$`)
	// Features a workflow can use that have no faithful local equivalent.
	unsupported = []string{"uses:", "services:", "container:", "strategy:"}
)

func inspectCI(r Repo) (proposals []Proposal, notes []string) {
	for _, path := range r.Tracked {
		dir := filepath.Dir(path)
		if dir != ".github/workflows" || (!strings.HasSuffix(path, ".yml") && !strings.HasSuffix(path, ".yaml")) {
			continue
		}
		content := r.read(path)

		if feats := unsupportedFeatures(content); len(feats) > 0 {
			notes = append(notes, path+" uses "+strings.Join(feats, ", ")+
				" — review-lens will not translate those into local commands")
		}

		for _, m := range runLine.FindAllStringSubmatch(content, -1) {
			cmd := strings.TrimSpace(m[1])
			if !plainStep(cmd) {
				continue // block scalars, templating and shell syntax aren't reproducible as one argv
			}
			if p, ok := repoOwnedGate(r, cmd, path); ok {
				proposals = append(proposals, p)
				continue
			}
			// A plain step is only ever a suggestion for the human to keep or drop.
			proposals = append(proposals, Proposal{
				Check:  proposalCheck("ci-step", cmd),
				Source: path,
			})
		}
	}
	return proposals, notes
}

func unsupportedFeatures(content string) []string {
	var feats []string
	for _, f := range unsupported {
		if regexp.MustCompile(`(?m)^\s*(?:-\s+)?` + f).MatchString(content) {
			feats = append(feats, strings.TrimSuffix(f, ":"))
		}
	}
	return feats
}

// repoOwnedGate recognises a workflow step that runs a command the repository
// itself owns — the canonical local gate. Those are high confidence: the same
// command works for developers, CI, and review-lens, so the gates can't drift.
func repoOwnedGate(r Repo, cmd, source string) (Proposal, bool) {
	argv := strings.Fields(cmd)
	if len(argv) == 0 {
		return Proposal{}, false
	}

	// make <target>, where the root Makefile defines <target>.
	if argv[0] == "make" && len(argv) == 2 && makefileTarget(r, argv[1]) {
		return Proposal{
			Check:          proposalCheck("make "+argv[1], cmd),
			HighConfidence: r.installed("make"),
			Source:         source,
		}, true
	}

	// A tracked script: ./scripts/check, scripts/check, bash scripts/check.
	script := argv[0]
	if script == "bash" || script == "sh" {
		if len(argv) < 2 {
			return Proposal{}, false
		}
		script = argv[1]
	}
	if r.tracked(strings.TrimPrefix(script, "./")) {
		return Proposal{
			Check:          proposalCheck(filepath.Base(script), cmd),
			HighConfidence: true,
			Source:         source,
		}, true
	}
	return Proposal{}, false
}

// makefileTarget reports whether the root Makefile declares target.
func makefileTarget(r Repo, target string) bool {
	content := r.read("Makefile")
	if content == "" {
		return false
	}
	return regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(target) + `\s*:`).MatchString(content)
}

// plainStep reports whether a run line is one simple command: no block
// scalars, no `${{ }}` templating, no shell operators that a single argv can't
// express.
func plainStep(cmd string) bool {
	if cmd == "" || cmd == "|" || cmd == ">" || strings.Contains(cmd, "${{") {
		return false
	}
	return !strings.ContainsAny(cmd, "|&;<>$`(")
}

// proposalCheck builds a root-relative check from a plain command line. Fields
// splitting is safe because plainStep rejected anything needing real shell
// syntax.
func proposalCheck(name, cmd string) config.Check {
	return config.Check{Name: name, Cmd: strings.Fields(cmd)}
}
