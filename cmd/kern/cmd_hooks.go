package main

// kern hooks install — write a git pre-push hook that runs `kern lint` so
// workflow compile errors surface before CI. Advisory only: a missing kern
// binary warns and lets the push through.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const hookMarker = "# kern pre-push hook — installed by 'kern hooks install'"

const prePushHook = `#!/bin/sh
` + hookMarker + `
# Lints .github/workflows offline; remove this file to disable.
if command -v kern >/dev/null 2>&1; then
  kern lint || exit 1
else
  echo "kern: hook skipped — 'kern' not on PATH" >&2
fi
exit 0
`

func (c *cli) cmdHooks(args []string) error {
	if len(args) == 0 || args[0] != "install" {
		return fmt.Errorf("usage: kern hooks install [--dry-run] [--force]")
	}
	// Presence-only flags — parseFlags would demand a value for each.
	var dryRun, force bool
	var pos []string
	for _, a := range args[1:] {
		switch a {
		case "--dry-run":
			dryRun = true
		case "--force":
			force = true
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) > 0 {
		return fmt.Errorf("usage: kern hooks install [--dry-run] [--force]")
	}
	hookPath, err := gitHookPath("pre-push")
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Print(prePushHook)
		return nil
	}
	if _, err := os.Stat(hookPath); err == nil && !force {
		return fmt.Errorf("%s already exists — pass --force to overwrite", hookPath)
	}
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(hookPath, []byte(prePushHook), 0o755); err != nil {
		return err
	}
	fmt.Printf("installed %s — runs 'kern lint' before every push\n", hookPath)
	fmt.Println("remove the file to disable")
	return nil
}

// gitHookPath resolves hooks/<name> through git so linked worktrees land in
// the common hooks dir.
func gitHookPath(name string) (string, error) {
	out, err := exec.Command("git", "rev-parse", "--git-path", "hooks/"+name).Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git repository (or git missing): %w", err)
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		p = abs
	}
	return p, nil
}
