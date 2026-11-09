package main

import (
	"strings"
	"testing"
)

func TestCompletionScripts(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		var script string
		switch shell {
		case "bash":
			script = bashCompletion()
		case "zsh":
			script = zshCompletion()
		case "fish":
			script = fishCompletion()
		}
		if !strings.Contains(script, "kern") {
			t.Fatalf("%s script does not mention kern", shell)
		}
		for _, cmd := range topLevelCommands {
			if !strings.Contains(script, cmd) {
				t.Fatalf("%s script missing top-level command %q", shell, cmd)
			}
		}
	}
}

func TestSubCommandsCoverDispatchGroups(t *testing.T) {
	// Every dispatch group that accepts a subcommand must appear in the table,
	// otherwise completion silently offers nothing for it.
	for _, group := range []string{"runs", "projects", "token", "config", "secrets", "vars",
		"artifacts", "cache", "environments", "deployments", "settings", "runners", "hooks"} {
		if _, ok := subCommands[group]; !ok {
			t.Fatalf("subCommands missing group %q", group)
		}
	}
}
