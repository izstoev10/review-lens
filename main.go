// Command review-lens is a personal pre-push validation gate.
//
// It runs your checks (and optionally an AI agent to fix failures) inside a
// disposable git worktree, then pushes only when everything is green.
//
// Usage:
//
//	review-lens init   # write a starter .review-lens.json in the current repo
//	review-lens run    # gate the current branch
//	review-lens help
//
// This is an MVP. Deliberately missing (and good next steps): the
// `git push review-lens` remote-proxy trigger, a bubbletea TUI, and
// multi-agent fallback. See README.md.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/lipgloss"

	"github.com/izstoev10/review-lens/internal/config"
	"github.com/izstoev10/review-lens/internal/gitx"
	"github.com/izstoev10/review-lens/internal/guidance"
	"github.com/izstoev10/review-lens/internal/pipeline"
)

const configName = ".review-lens.json"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit()
	case "run":
		err = cmdRun()
	case "pr":
		err = cmdPR()
	case "loop":
		err = cmdLoop()
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "review-lens: error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`review-lens — pre-push validation gate

Commands:
  init      Write a starter .review-lens.json into the current repo
  run       Gate the current branch (checks -> agent fix -> review -> push -> PR)
  pr [num]  Review an already-open PR's diff, read-only (current branch if no num)
  loop [num]  Auto-fix loop: review -> fix -> push -> poll CI -> re-review, until green
  help      Show this help`)
}

// configPath returns the path to .gate.json at the repo root of the cwd.
func configPath() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root, err := gitx.RepoRoot(cwd)
	if err != nil {
		return "", fmt.Errorf("not inside a git repo: %w", err)
	}
	return filepath.Join(root, configName), nil
}

func cmdInit() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	// Detect the project type so the starter checks match the repo. The tool
	// itself is language-agnostic — this just picks convenient defaults.
	root := filepath.Dir(path)
	cfg := config.Detect(func(name string) bool {
		_, err := os.Stat(filepath.Join(root, name))
		return err == nil
	})
	if err := config.Save(path, cfg); err != nil {
		return err
	}

	// Scaffold the editable review guidance alongside the config, so the review
	// criteria are easy to tune. It's optional: if this file is later removed,
	// review-lens falls back to the same built-in default it contains now.
	guidancePath := cfg.ReviewGuidancePath
	if guidancePath == "" {
		guidancePath = guidance.DefaultPath
	}
	guidanceFull := filepath.Join(root, guidancePath)
	if _, err := os.Stat(guidanceFull); err != nil {
		if err := os.WriteFile(guidanceFull, []byte(guidance.Default+"\n"), 0o644); err != nil {
			return err
		}
	}

	printInitSummary(root, configName, guidancePath)
	return nil
}

// printInitSummary renders the branded `init` result: a wordmark panel, a
// confirmation, an aligned repo/config/review summary, and next steps. Colour is
// via lipgloss, which auto-degrades to plain text when output isn't a terminal
// (or NO_COLOR is set), so piped output stays clean.
func printInitSummary(root, cfgPath, reviewPath string) {
	var (
		accent = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
		dim    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
		ok     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
		bold   = lipgloss.NewStyle().Bold(true)
		border = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 2)
	)

	wordmark := accent.Render("review") + dim.Render("·") + accent.Render("lens")
	tagline := dim.Render("AI review + pre-push gate")
	banner := border.Render(wordmark + "\n" + tagline)

	label := func(s string) string { return dim.Render(fmt.Sprintf("%-8s", s)) }
	row := func(k, v string) string { return "  " + label(k) + " " + v }

	lines := []string{
		"",
		banner,
		"",
		"  " + ok.Render("✓") + " " + bold.Render("initialized"),
		"",
		row("repo", root),
		row("config", cfgPath),
		row("review", reviewPath),
		"",
		"  " + dim.Render("Review a PR") + "    " + bold.Render("review-lens ") + accent.Render("pr"),
		"  " + dim.Render("Gate a branch") + "  " + bold.Render("review-lens ") + accent.Render("run"),
		"",
	}
	for _, l := range lines {
		fmt.Println(l)
	}
}

func cmdRun() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return pipeline.Run(cwd, cfg, os.Stdout)
}

func cmdPR() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	// Optional PR number: `review-lens pr 1234`.
	var number string
	if len(os.Args) > 2 {
		number = os.Args[2]
	}
	return pipeline.ReviewPR(cwd, number, cfg, os.Stdout, isInteractive())
}

func cmdLoop() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	// Optional PR number: `review-lens loop 1234` (current branch's PR if omitted).
	var number string
	if len(os.Args) > 2 {
		number = os.Args[2]
	}
	return pipeline.AutoFixLoop(cwd, number, cfg, os.Stdout)
}

// isInteractive reports whether stdout is a real terminal, so the TUI is only
// launched when it can actually render (not when output is piped or redirected).
func isInteractive() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
