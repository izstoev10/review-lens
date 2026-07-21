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

	"github.com/izstoev10/review-lens/internal/config"
	"github.com/izstoev10/review-lens/internal/gitx"
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
	fmt.Printf("wrote %s — edit it to set your checks and agent.\n", path)
	return nil
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
	return pipeline.ReviewPR(cwd, number, cfg, os.Stdout)
}
