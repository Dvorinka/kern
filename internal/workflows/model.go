// Package workflows turns GitHub Actions YAML into KERN's internal execution model.
// Nothing here executes user code: parsing, validation and compilation are pure.
package workflows

import (
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Workflow is the YAML document as written by the user.
type Workflow struct {
	Name        string            `yaml:"name"`
	RunName     string            `yaml:"run-name"`
	On          Triggers          `yaml:"on"`
	Env         map[string]any    `yaml:"env"`
	Defaults    Defaults          `yaml:"defaults"`
	Concurrency Concurrency       `yaml:"concurrency"`
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]*Job   `yaml:"jobs"`

	// Unknown keeps top-level keys KERN does not implement so validation can report them.
	Unknown map[string]struct{} `yaml:"-"`
}

type Defaults struct {
	Run RunDefaults `yaml:"run"`
}

type RunDefaults struct {
	Shell            string `yaml:"shell"`
	WorkingDirectory string `yaml:"working-directory"`
}

type Concurrency struct {
	Group            string `yaml:"group"`
	CancelInProgress bool   `yaml:"cancel-in-progress"`
}

// Triggers accepts `on: push`, `on: [push, pull_request]` and the mapping form.
type Triggers struct {
	Events []string
	Raw    map[string]any
}

func (t *Triggers) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		t.Events = []string{s}
		return nil
	case yaml.SequenceNode:
		var list []string
		if err := node.Decode(&list); err != nil {
			return err
		}
		t.Events = list
		return nil
	case yaml.MappingNode:
		raw := map[string]any{}
		if err := node.Decode(&raw); err != nil {
			return err
		}
		t.Raw = raw
		for i := 0; i < len(node.Content); i += 2 {
			t.Events = append(t.Events, node.Content[i].Value)
		}
		return nil
	}
	return fmt.Errorf("invalid 'on' trigger")
}

// StringList accepts a string or a list of strings.
type StringList []string

func (s *StringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var v string
		if err := node.Decode(&v); err != nil {
			return err
		}
		*s = StringList{v}
		return nil
	case yaml.SequenceNode:
		var v []string
		if err := node.Decode(&v); err != nil {
			return err
		}
		*s = StringList(v)
		return nil
	}
	return fmt.Errorf("expected a string or list of strings")
}

type Job struct {
	Name            string            `yaml:"name"`
	Needs           StringList        `yaml:"needs"`
	If              string            `yaml:"if"`
	RunsOn          StringList        `yaml:"runs-on"`
	Env             map[string]any    `yaml:"env"`
	Steps           []Step            `yaml:"steps"`
	TimeoutMinutes  float64           `yaml:"timeout-minutes"`
	ContinueOnError bool              `yaml:"continue-on-error"`
	Outputs         map[string]string `yaml:"outputs"`
	Strategy        *Strategy         `yaml:"strategy"`
	Uses            string            `yaml:"uses"`
	With            map[string]any    `yaml:"with"`
	Secrets         any               `yaml:"secrets"`
	Container       any               `yaml:"container"`
	Services        map[string]any    `yaml:"services"`
	Defaults        *Defaults         `yaml:"defaults"`
	Permissions     map[string]string `yaml:"permissions"`
	Environment     any               `yaml:"environment"`
}

type Strategy struct {
	Matrix      map[string][]any `yaml:"matrix"`
	FailFast    *bool            `yaml:"fail-fast"`
	MaxParallel int              `yaml:"max-parallel"`
}

type Step struct {
	ID               string         `yaml:"id"`
	Name             string         `yaml:"name"`
	If               string         `yaml:"if"`
	Run              string         `yaml:"run"`
	Shell            string         `yaml:"shell"`
	WorkingDirectory string         `yaml:"working-directory"`
	Uses             string         `yaml:"uses"`
	With             map[string]any `yaml:"with"`
	Env              map[string]any `yaml:"env"`
	ContinueOnError  bool           `yaml:"continue-on-error"`
	TimeoutMinutes   float64        `yaml:"timeout-minutes"`
}

// MatrixValue is one axis value; GitHub permits scalars and maps in include entries.
type MatrixValue map[string]any

// StepPlan is a fully resolved step.
type StepPlan struct {
	Number           int               `json:"number"`
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Run              string            `json:"run,omitempty"`
	Uses             string            `json:"uses,omitempty"`
	Shell            string            `json:"shell,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	If               string            `json:"if,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	With             map[string]any    `json:"with,omitempty"`
	ContinueOnError  bool              `json:"continue_on_error"`
	TimeoutSeconds   int               `json:"timeout_seconds"`
}

// JobPlan is a single schedulable unit (one matrix combination counts as one job).
type JobPlan struct {
	Key             string            `json:"key"`
	JobID           string            `json:"job_id"`
	Name            string            `json:"name"`
	Needs           []string          `json:"needs"`
	RunsOn          []string          `json:"runs_on"`
	Image           string            `json:"image"`
	Matrix          map[string]any    `json:"matrix,omitempty"`
	FailFast        bool              `json:"fail_fast"`
	Env             map[string]string `json:"env,omitempty"`
	If              string            `json:"if,omitempty"`
	Outputs         map[string]string `json:"outputs,omitempty"`
	TimeoutSeconds  int               `json:"timeout_seconds"`
	ContinueOnError bool              `json:"continue_on_error"`
	Priority        int               `json:"priority"`
	Environment     string            `json:"environment,omitempty"`
	EnvironmentURL  string            `json:"environment_url,omitempty"`
	Steps           []StepPlan        `json:"steps"`
}

// Edge is a dependency between plan jobs, kept for DAG rendering.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type Plan struct {
	Name        string   `json:"name"`
	Events      []string `json:"events"`
	Concurrency string   `json:"concurrency,omitempty"`
	// CancelInProgress mirrors concurrency.cancel-in-progress (GitHub semantics:
	// a newer run in the same group cancels the older one).
	CancelInProgress bool      `json:"cancel_in_progress,omitempty"`
	FailFast         bool      `json:"fail_fast"`
	Jobs             []JobPlan `json:"jobs"`
	Edges            []Edge    `json:"edges"`
	// Triggers keeps the raw trigger configuration for filtering later phases.
	Triggers map[string]any `json:"triggers,omitempty"`
}

type Diagnostic struct {
	Level   string `json:"level"`
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Result is the outcome of parsing and compiling a workflow file.
type Result struct {
	Workflow    *Workflow    `json:"-"`
	Plan        *Plan        `json:"plan,omitempty"`
	Name        string       `json:"name"`
	Events      []string     `json:"events"`
	Valid       bool         `json:"valid"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// asString converts YAML scalar values the way GitHub stringifies env values.
func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}

func stringMap(in map[string]any) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = asString(v)
	}
	return out
}

func stepID(s Step, index int) string {
	if s.ID != "" {
		return s.ID
	}
	return fmt.Sprintf("__step_%d", index+1)
}
