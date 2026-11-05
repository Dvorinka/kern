// Package actions classifies `uses:` references and parses action.yml files.
// The compiler uses Classify for diagnostics; the runner uses ParseRemote and
// ParseMeta to resolve and run actions inside the job container.
package actions

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Kind is the action runtime a `uses:` reference needs.
type Kind int

const (
	KindInvalid Kind = iota
	KindNative       // implemented by the runner itself (checkout, artifacts, cache)
	KindLocal        // ./path — action.yml inside the checked-out workspace
	KindRemote       // owner/repo[/path]@ref — fetched by the runner at step time
	KindDocker       // docker://image — container action (not implemented)
)

// nativeActions are the `uses:` references KERN implements without reading an
// action.yml. Versions are ignored: the runner pins behaviour, not the tag.
var nativeActions = map[string]bool{
	"actions/checkout":          true,
	"actions/upload-artifact":   true,
	"actions/download-artifact": true,
	"actions/cache":             true,
	"actions/cache/restore":     true,
	"actions/cache/save":        true,
}

// remoteRe matches owner/repo[/sub/path]; the ref is split separately.
var remoteRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(/[A-Za-z0-9_./-]+)?$`)

// Classify determines which runtime a `uses:` value needs. It never looks at
// the filesystem or network — remote actions are classified by shape only.
func Classify(uses string) Kind {
	u := strings.TrimSpace(uses)
	if u == "" {
		return KindInvalid
	}
	if strings.HasPrefix(strings.ToLower(u), "docker://") {
		return KindDocker
	}
	name := u
	if i := strings.LastIndexByte(u, '@'); i >= 0 {
		name = u[:i]
	}
	if nativeActions[strings.ToLower(name)] {
		return KindNative
	}
	if strings.HasPrefix(u, "./") {
		return KindLocal
	}
	return KindRemote
}

// ValidLocal reports whether a `./path` reference stays inside the workspace.
// Local actions cannot carry a ref (GitHub rejects `./x@v1`).
func ValidLocal(uses string) error {
	if strings.Contains(uses, "@") {
		return fmt.Errorf("local action %q cannot specify a ref", uses)
	}
	clean := path.Clean(strings.TrimPrefix(uses, "./"))
	if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return fmt.Errorf("local action %q escapes the workspace", uses)
	}
	return nil
}

// RemoteRef is a parsed owner/repo[/path]@ref action reference.
type RemoteRef struct {
	Owner string
	Repo  string
	Path  string // optional subdirectory containing action.yml
	Ref   string
}

// ParseRemote splits owner/repo[/path]@ref. The ref (branch, tag or SHA) is
// required — GitHub rejects refless remote actions.
func ParseRemote(uses string) (RemoteRef, error) {
	at := strings.LastIndexByte(uses, '@')
	if at < 0 {
		return RemoteRef{}, fmt.Errorf("action %q must pin a ref: owner/repo[/path]@ref", uses)
	}
	where, ref := uses[:at], uses[at+1:]
	if ref == "" {
		return RemoteRef{}, fmt.Errorf("action %q has an empty ref", uses)
	}
	if !remoteRe.MatchString(where) {
		return RemoteRef{}, fmt.Errorf("action %q is not owner/repo[/path]@ref", uses)
	}
	parts := strings.SplitN(where, "/", 3)
	r := RemoteRef{Owner: parts[0], Repo: parts[1], Ref: ref}
	if len(parts) == 3 {
		r.Path = strings.Trim(parts[2], "/")
	}
	return r, nil
}

// Meta is the subset of action.yml KERN understands.
type Meta struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Inputs      map[string]Input  `yaml:"inputs"`
	Outputs     map[string]Output `yaml:"outputs"`
	Runs        Runs              `yaml:"runs"`
}

type Input struct {
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
	Default     string `yaml:"default"`
}

type Output struct {
	Description string `yaml:"description"`
	Value       string `yaml:"value"`
}

type Runs struct {
	Using string          `yaml:"using"`
	Steps []CompositeStep `yaml:"steps"`
	// JavaScript actions (using: node*)
	Main   string `yaml:"main"`
	Pre    string `yaml:"pre"`
	Post   string `yaml:"post"`
	PreIf  string `yaml:"pre-if"`
	PostIf string `yaml:"post-if"`
	// Docker actions (using: docker)
	Image          string         `yaml:"image"`
	Entrypoint     string         `yaml:"entrypoint"`
	Args           []string       `yaml:"args"`
	Env            map[string]any `yaml:"env"`
	PreEntrypoint  string         `yaml:"pre-entrypoint"`
	PostEntrypoint string         `yaml:"post-entrypoint"`
}

// CompositeStep is one entry of a composite action's runs.steps.
type CompositeStep struct {
	ID               string         `yaml:"id"`
	Name             string         `yaml:"name"`
	If               string         `yaml:"if"`
	Run              string         `yaml:"run"`
	Shell            string         `yaml:"shell"`
	WorkingDirectory string         `yaml:"working-directory"`
	Uses             string         `yaml:"uses"`
	With             map[string]any `yaml:"with"`
	Env              map[string]any `yaml:"env"`
}

// ParseMeta decodes an action.yml document.
func ParseMeta(data []byte) (*Meta, error) {
	var m Meta
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("action.yml is not valid YAML: %w", err)
	}
	m.Runs.Using = strings.ToLower(strings.TrimSpace(m.Runs.Using))
	return &m, nil
}
