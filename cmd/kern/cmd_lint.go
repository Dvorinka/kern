package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dvorinka/kern/internal/workflows"
)

// lintFile is the per-workflow result.
type lintFile struct {
	Path     string                 `json:"path"`
	Name     string                 `json:"name"`
	Events   []string               `json:"events"`
	Valid    bool                   `json:"valid"`
	Jobs     int                    `json:"jobs"`
	Steps    int                    `json:"steps"`
	RunsOn   []string               `json:"runs_on,omitempty"`
	Uses     []string               `json:"uses,omitempty"`
	Errors   []workflows.Diagnostic `json:"errors,omitempty"`
	Warnings []workflows.Diagnostic `json:"warnings,omitempty"`
	Infos    []workflows.Diagnostic `json:"infos,omitempty"`
}

// cmdLint compiles every workflow under <dir>/.github/workflows offline and
// reports whether KERN could run it as written. No server required; remote
// reusable workflows resolve through the gh CLI when it is installed.
func (c *cli) cmdLint(args []string) error {
	_, pos := parseFlags(args)
	if len(pos) == 0 {
		pos = []string{"."}
	}
	defaultImage := envOr("KERN_DEFAULT_IMAGE", "ubuntu:24.04")

	var files []lintFile
	for _, dir := range pos {
		for _, f := range findWorkflows(dir) {
			files = append(files, lintOne(dir, f, defaultImage))
		}
	}
	if len(files) == 0 {
		return fmt.Errorf("no workflows found under .github/workflows in %s", strings.Join(pos, ", "))
	}

	if c.json {
		printJSON(files)
	} else {
		for _, f := range files {
			printLintFile(f)
		}
	}
	for _, f := range files {
		if !f.Valid || len(f.Errors) > 0 {
			return fmt.Errorf("%d workflow(s) have errors", countBroken(files))
		}
	}
	return nil
}

func findWorkflows(dir string) []string {
	if st, err := os.Stat(dir); err == nil && !st.IsDir() {
		return []string{dir}
	}
	var out []string
	for _, pat := range []string{"*.yml", "*.yaml"} {
		m, _ := filepath.Glob(filepath.Join(dir, ".github", "workflows", pat))
		out = append(out, m...)
	}
	sort.Strings(out)
	return out
}

func lintOne(dir, path, defaultImage string) lintFile {
	lf := lintFile{Path: path}
	content, err := os.ReadFile(path)
	if err != nil {
		lf.Errors = []workflows.Diagnostic{{Level: "error", Code: "read", Message: err.Error()}}
		return lf
	}
	cc := workflows.Compiler{
		DefaultImage: defaultImage,
		LabelImages:  map[string]string{"ubuntu-latest": defaultImage, "ubuntu-24.04": defaultImage},
		Resolve:      makeResolver(dir),
	}
	res, err := workflows.Compile(string(content), cc)
	if err != nil {
		lf.Errors = []workflows.Diagnostic{{Level: "error", Code: "compile", Message: err.Error()}}
		return lf
	}
	lf.Name, lf.Events, lf.Valid = res.Name, res.Events, res.Valid
	if res.Plan != nil {
		lf.Jobs = len(res.Plan.Jobs)
		seenUses, seenLabels := map[string]bool{}, map[string]bool{}
		for _, j := range res.Plan.Jobs {
			lf.Steps += len(j.Steps)
			for _, l := range j.RunsOn {
				if !seenLabels[l] {
					seenLabels[l] = true
					lf.RunsOn = append(lf.RunsOn, l)
				}
			}
			for _, s := range j.Steps {
				if s.Uses != "" && !seenUses[s.Uses] {
					seenUses[s.Uses] = true
					lf.Uses = append(lf.Uses, s.Uses)
				}
			}
		}
	}
	for _, d := range res.Diagnostics {
		switch d.Level {
		case "error":
			lf.Errors = append(lf.Errors, d)
		case "warn", "warning":
			lf.Warnings = append(lf.Warnings, d)
		default:
			lf.Infos = append(lf.Infos, d)
		}
	}
	return lf
}

// makeResolver resolves ./local reusable workflows from disk and remote
// owner/repo/path@ref forms through gh (matching production's gh contents call).
func makeResolver(repoRoot string) func(string) (string, error) {
	return func(uses string) (string, error) {
		if strings.HasPrefix(uses, "./") {
			b, err := os.ReadFile(filepath.Join(repoRoot, strings.TrimPrefix(uses, "./")))
			if err != nil {
				return "", fmt.Errorf("resolve %s: %w", uses, err)
			}
			return string(b), nil
		}
		spec, atRef, _ := strings.Cut(uses, "@")
		parts := strings.SplitN(spec, "/", 3)
		if len(parts) != 3 {
			return "", fmt.Errorf("unresolvable reusable ref %q", uses)
		}
		apiPath := fmt.Sprintf("repos/%s/%s/contents/%s?ref=%s", parts[0], parts[1], parts[2], atRef)
		out, err := exec.Command("gh", "api", apiPath, "--jq", ".content").Output()
		if err != nil {
			return "", fmt.Errorf("resolve %s via gh api: %w", uses, err)
		}
		var decBuf strings.Builder
		cmd := exec.Command("base64", "-d")
		cmd.Stdin = strings.NewReader(strings.ReplaceAll(string(out), "\n", ""))
		cmd.Stdout = &decBuf
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("decode %s: %w", uses, err)
		}
		return decBuf.String(), nil
	}
}

func printLintFile(f lintFile) {
	mark := "ok  "
	if !f.Valid || len(f.Errors) > 0 {
		mark = "FAIL"
	} else if len(f.Warnings) > 0 {
		mark = "warn"
	}
	fmt.Printf("%s  %s  (%s) — %d jobs, %d steps, on: %s\n",
		mark, f.Path, f.Name, f.Jobs, f.Steps, strings.Join(f.Events, ","))
	if len(f.RunsOn) > 0 {
		fmt.Printf("      runs-on: %s\n", strings.Join(f.RunsOn, " | "))
	}
	for _, d := range f.Errors {
		fmt.Printf("      error   %-22s %s — %s\n", d.Code, d.Path, d.Message)
	}
	for _, d := range f.Warnings {
		fmt.Printf("      warn    %-22s %s — %s\n", d.Code, d.Path, d.Message)
	}
	for _, d := range f.Infos {
		fmt.Printf("      info    %-22s %s — %s\n", d.Code, d.Path, d.Message)
	}
}

func countBroken(files []lintFile) int {
	n := 0
	for _, f := range files {
		if !f.Valid || len(f.Errors) > 0 {
			n++
		}
	}
	return n
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
