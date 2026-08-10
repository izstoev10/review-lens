package setup

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/izstoev10/review-lens/internal/config"
	"github.com/izstoev10/review-lens/internal/discover"
	"github.com/izstoev10/review-lens/internal/gitx"
)

// Propose discovers gate proposals for the repo at root, from tracked files
// only. It is the single discovery seam shared by `init` and `configure`.
func Propose(root string, lookPath func(string) (string, error)) ([]discover.Proposal, []string, error) {
	tracked, err := gitx.TrackedFiles(root)
	if err != nil {
		return nil, nil, err
	}
	proposals, notes := discover.Discover(discover.Repo{Root: root, Tracked: tracked, LookPath: lookPath})
	return proposals, notes, nil
}

// Configure turns discovery proposals into the config's setup and check lists,
// leaving every other setting (agent, remote, review, PR) untouched. It is the
// one workflow behind both `init` and `configure`.
//
// Interactive callers review the proposed gate — drop, reorder, add custom
// commands — before it is accepted. Non-interactive callers accept only
// high-confidence proposals; with none, the config is left without checks
// (configured=false) and a pointer to the interactive configurator is printed,
// because writing speculative checks would build a gate that can only fail.
func Configure(
	cfg config.Config,
	proposals []discover.Proposal,
	notes []string,
	in io.Reader,
	out io.Writer,
	interactive bool,
) (result config.Config, configured bool, err error) {
	for _, n := range notes {
		fmt.Fprintln(out, "note: "+n)
	}

	if !interactive {
		var confident []discover.Proposal
		for _, p := range proposals {
			if p.HighConfidence {
				confident = append(confident, p)
			}
		}
		if len(confident) == 0 {
			cfg.Setup, cfg.Checks = nil, nil
			fmt.Fprintln(out, "No checks could be proposed with confidence; leaving the gate unconfigured.")
			fmt.Fprintln(out, "Run `review-lens configure` in a terminal to set up checks interactively.")
			return cfg, false, nil
		}
		cfg.Setup, cfg.Checks = split(confident)
		return cfg, true, nil
	}

	proposals, err = editProposals(proposals, in, out)
	if err != nil {
		return cfg, false, err
	}
	if len(proposals) == 0 {
		fmt.Fprintln(out, "No checks selected; leaving the gate unconfigured.")
		return cfg, false, nil
	}
	cfg.Setup, cfg.Checks = split(proposals)
	return cfg, true, nil
}

// split orders accepted proposals into the config's two phases, preserving the
// list order within each.
func split(ps []discover.Proposal) (setup, checks []config.Check) {
	for _, p := range ps {
		if p.Setup {
			setup = append(setup, p.Check)
		} else {
			checks = append(checks, p.Check)
		}
	}
	return setup, checks
}

// editProposals is the interactive review loop: the numbered gate is printed,
// and the user drops, moves, or adds entries until they accept with an empty
// line. Input ending without an answer accepts whatever is listed, matching
// SelectAgent's behaviour.
func editProposals(ps []discover.Proposal, in io.Reader, out io.Writer) ([]discover.Proposal, error) {
	sc := bufio.NewScanner(in)
	for {
		printProposals(ps, out)
		fmt.Fprint(out, "gate> ")
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				return nil, fmt.Errorf("reading gate edits: %w", err)
			}
			return ps, nil
		}
		line := strings.TrimSpace(sc.Text())
		fields := strings.Fields(line)
		switch {
		case line == "":
			return ps, nil
		case line == "q":
			return nil, fmt.Errorf("configuration aborted")
		case len(fields) == 2 && fields[0] == "d":
			if i, ok := index(fields[1], len(ps)); ok {
				ps = append(ps[:i], ps[i+1:]...)
				continue
			}
		case len(fields) == 3 && fields[0] == "m":
			if i, ok := index(fields[1], len(ps)); ok {
				if j, ok := index(fields[2], len(ps)); ok {
					p := ps[i]
					ps = append(ps[:i], ps[i+1:]...)
					ps = append(ps[:j], append([]discover.Proposal{p}, ps[j:]...)...)
					continue
				}
			}
		case fields[0] == "a" || fields[0] == "s":
			p, err := readCustom(sc, out, fields[0] == "s")
			if err != nil {
				return nil, err
			}
			if p != nil {
				ps = append(ps, *p)
			}
			continue
		}
		fmt.Fprintln(out, "Commands: enter accept · d N drop · m N M move · a add check · s add setup command · q abort")
	}
}

// index parses a 1-based list position.
func index(s string, n int) (int, bool) {
	i, err := strconv.Atoi(s)
	if err != nil || i < 1 || i > n {
		return 0, false
	}
	return i - 1, true
}

// readCustom prompts for one user-supplied command, so uncommon stacks are
// never blocked by the built-in adapters.
func readCustom(sc *bufio.Scanner, out io.Writer, isSetup bool) (*discover.Proposal, error) {
	ask := func(label string) (string, error) {
		fmt.Fprint(out, label)
		if !sc.Scan() {
			return "", sc.Err()
		}
		return strings.TrimSpace(sc.Text()), nil
	}
	name, err := ask("name: ")
	if err != nil || name == "" {
		return nil, err
	}
	dir, err := ask("directory (blank = repo root): ")
	if err != nil {
		return nil, err
	}
	cmd, err := ask("command: ")
	if err != nil || cmd == "" {
		return nil, err
	}
	return &discover.Proposal{
		Check: config.Check{Name: name, Cmd: strings.Fields(cmd), Dir: dir},
		Setup: isSetup,
	}, nil
}

func printProposals(ps []discover.Proposal, out io.Writer) {
	fmt.Fprintln(out, "\nProposed gate:")
	if len(ps) == 0 {
		fmt.Fprintln(out, "  (empty)")
	}
	for i, p := range ps {
		kind := "check"
		if p.Setup {
			kind = "setup"
		}
		dir := p.Check.Dir
		if dir == "" {
			dir = "."
		}
		src := ""
		if p.Source != "" {
			src = "  (from " + p.Source + ")"
		}
		fmt.Fprintf(out, "  %d) [%s] %-10s %-12s %s%s\n", i+1, kind, p.Check.Name, dir, strings.Join(p.Check.Cmd, " "), src)
	}
	fmt.Fprintln(out, "enter accept · d N drop · m N M move · a add check · s add setup command · q abort")
}
