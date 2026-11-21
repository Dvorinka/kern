// Package workflows turns GitHub Actions YAML into KERN's internal execution model.
// Nothing here executes user code: parsing, validation and compilation are pure.
package workflows

import (
	"fmt"
	"strconv"
	"strings"

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
	Permissions Permissions       `yaml:"permissions"`
	Jobs        map[string]*Job   `yaml:"jobs"`

	// Unknown keeps top-level keys KERN does not implement so validation can report them.
	Unknown map[string]struct{} `yaml:"-"`
}

// Permissions accepts either a mapping of scope -> read|write|none or the
// GitHub Actions shorthands `read-all` and `write-all`.
type Permissions map[string]string

var allPermissionScopes = []string{
	"actions", "checks", "contents", "deployments", "issues",
	"packages", "pages", "pull-requests", "security-events", "statuses",
}

func (p *Permissions) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		switch strings.TrimSpace(s) {
		case "read-all", "write-all":
			level := strings.TrimSuffix(strings.TrimSpace(s), "-all")
			m := make(map[string]string, len(allPermissionScopes))
			for _, sc := range allPermissionScopes {
				m[sc] = level
			}
			*p = m
			return nil
		default:
			return fmt.Errorf("invalid scalar permissions %q (expected read-all, write-all, or mapping)", s)
		}
	case yaml.MappingNode:
		var m map[string]string
		if err := node.Decode(&m); err != nil {
			return err
		}
		*p = m
		return nil
	}
	return fmt.Errorf("permissions must be read-all, write-all, or a mapping")
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
	CancelExpr       string `yaml:"-"`
}

func (c *Concurrency) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		return node.Decode(&c.Group)
	case yaml.MappingNode:
		var raw struct {
			Group            string `yaml:"group"`
			CancelInProgress any    `yaml:"cancel-in-progress"`
		}
		if err := node.Decode(&raw); err != nil {
			return err
		}
		c.Group = raw.Group
		switch v := raw.CancelInProgress.(type) {
		case nil:
		case bool:
			c.CancelInProgress = v
		case string:
			if strings.EqualFold(strings.TrimSpace(v), "true") {
				c.CancelInProgress = true
			} else if !strings.EqualFold(strings.TrimSpace(v), "false") {
				c.CancelExpr = v
			}
		default:
			return fmt.Errorf("concurrency.cancel-in-progress must be a boolean or expression")
		}
		return nil
	}
	return fmt.Errorf("concurrency must be a string or mapping")
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

// StringList accepts a string, a list of strings, or a GitHub runs-on
// mapping `{group: ..., labels: ...}`.
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
	case yaml.MappingNode:
		var m struct {
			Group  string     `yaml:"group"`
			Labels StringList `yaml:"labels"`
		}
		if err := node.Decode(&m); err != nil {
			return err
		}
		var out []string
		if m.Group != "" {
			out = append(out, m.Group)
		}
		out = append(out, m.Labels...)
		*s = StringList(out)
		return nil
	}
	return fmt.Errorf("expected a string, list of strings, or {group, labels} mapping")
}

type Job struct {
	Name                string         `yaml:"name"`
	Needs               StringList     `yaml:"needs"`
	If                  string         `yaml:"if"`
	RunsOn              StringList     `yaml:"runs-on"`
	Env                 map[string]any `yaml:"env"`
	Steps               []Step         `yaml:"steps"`
	TimeoutMinutes      float64        `yaml:"-"`
	TimeoutExpr         string         `yaml:"-"`
	ContinueOnError     bool           `yaml:"-"`
	ContinueOnErrorExpr string         `yaml:"-"`
	Outputs             map[string]string `yaml:"outputs"`
	Strategy            *Strategy      `yaml:"strategy"`
	Uses                string         `yaml:"uses"`
	With                map[string]any `yaml:"with"`
	Secrets             any            `yaml:"secrets"`
	Container           any            `yaml:"container"`
	Services            map[string]any `yaml:"services"`
	Defaults            *Defaults      `yaml:"defaults"`
	Permissions         Permissions    `yaml:"permissions"`
	Environment         any            `yaml:"environment"`
	Concurrency         Concurrency    `yaml:"concurrency"`
	// Priority is a KERN extension: higher jobs are claimed first when a
	// runner polls the queue. GitHub ignores the key, so the same file
	// stays valid on both platforms.
	Priority int `yaml:"priority"`
	// Resources is a KERN extension: per-job container limits
	// ({memory: 8g, cpus: 4, disk: 30g}). The compiler folds them into the
	// KERN_JOB_* env the executor already honours; explicit env values win.
	// Trusted tiers only — a fork run cannot pin the host's cgroup ceilings.
	Resources map[string]any `yaml:"resources"`
}

func (j *Job) UnmarshalYAML(node *yaml.Node) error {
	type plain Job
	type jobWire struct {
		plain           `yaml:",inline"`
		TimeoutMinutes  any `yaml:"timeout-minutes"`
		ContinueOnError any `yaml:"continue-on-error"`
	}
	var w jobWire
	if err := node.Decode(&w); err != nil {
		return err
	}
	*j = Job(w.plain)
	if f, expr, err := decodeFloatOrExpr(w.TimeoutMinutes, "timeout-minutes"); err != nil {
		return err
	} else {
		j.TimeoutMinutes, j.TimeoutExpr = f, expr
	}
	if b, expr, err := decodeBoolOrExpr(w.ContinueOnError, "continue-on-error"); err != nil {
		return err
	} else {
		j.ContinueOnError, j.ContinueOnErrorExpr = b, expr
	}
	return nil
}

type Strategy struct {
	Matrix       map[string][]any `yaml:"matrix"`
	FailFast     *bool            `yaml:"-"`
	FailFastExpr string           `yaml:"-"`
	MaxParallel  int              `yaml:"max-parallel"`
	Split        Split            `yaml:"split"`
}

func (s *Strategy) UnmarshalYAML(node *yaml.Node) error {
	type plain Strategy
	type stratWire struct {
		plain    `yaml:",inline"`
		FailFast any `yaml:"fail-fast"`
	}
	var w stratWire
	if err := node.Decode(&w); err != nil {
		return err
	}
	*s = Strategy(w.plain)
	if w.FailFast != nil {
		b, expr, err := decodeBoolOrExpr(w.FailFast, "fail-fast")
		if err != nil {
			return err
		}
		if expr != "" {
			s.FailFastExpr = expr
		} else {
			s.FailFast = &b
		}
	}
	return nil
}

func decodeBoolOrExpr(raw any, field string) (bool, string, error) {
	switch v := raw.(type) {
	case nil:
		return false, "", nil
	case bool:
		return v, "", nil
	case string:
		t := strings.TrimSpace(v)
		if strings.EqualFold(t, "true") {
			return true, "", nil
		}
		if strings.EqualFold(t, "false") {
			return false, "", nil
		}
		return false, v, nil
	default:
		return false, "", fmt.Errorf("%s must be a boolean or expression", field)
	}
}

func decodeFloatOrExpr(raw any, field string) (float64, string, error) {
	switch v := raw.(type) {
	case nil:
		return 0, "", nil
	case int:
		return float64(v), "", nil
	case int64:
		return float64(v), "", nil
	case float64:
		return v, "", nil
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f, "", nil
		}
		return 0, v, nil
	default:
		return 0, "", fmt.Errorf("%s must be a number or expression", field)
	}
}

// Split is strategy.split: a shard count from 1 to 32, or the string "auto".
// The compiler records it. The orchestrator expands it once runners are known.
type Split struct {
	N    int
	Auto bool
}

func (s *Split) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("strategy.split must be an integer from 1 to 32 or auto")
	}
	value := strings.TrimSpace(node.Value)
	if value == "auto" {
		s.Auto = true
		return nil
	}
	if node.Tag == "!!str" {
		return fmt.Errorf("strategy.split must be an integer from 1 to 32 or auto")
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 32 {
		return fmt.Errorf("strategy.split must be an integer from 1 to 32 or auto")
	}
	s.N = n
	return nil
}

type Step struct {
	ID                  string         `yaml:"id"`
	Name                string         `yaml:"name"`
	If                  string         `yaml:"if"`
	Run                 string         `yaml:"run"`
	Shell               string         `yaml:"shell"`
	WorkingDirectory    string         `yaml:"working-directory"`
	Uses                string         `yaml:"uses"`
	With                map[string]any `yaml:"with"`
	Env                 map[string]any `yaml:"env"`
	ContinueOnError     bool           `yaml:"-"`
	ContinueOnErrorExpr string         `yaml:"-"`
	TimeoutMinutes      float64        `yaml:"-"`
	TimeoutExpr         string         `yaml:"-"`
}

func (s *Step) UnmarshalYAML(node *yaml.Node) error {
	type plain Step
	type stepWire struct {
		plain           `yaml:",inline"`
		TimeoutMinutes  any `yaml:"timeout-minutes"`
		ContinueOnError any `yaml:"continue-on-error"`
	}
	var w stepWire
	if err := node.Decode(&w); err != nil {
		return err
	}
	*s = Step(w.plain)
	if f, expr, err := decodeFloatOrExpr(w.TimeoutMinutes, "timeout-minutes"); err != nil {
		return err
	} else {
		s.TimeoutMinutes, s.TimeoutExpr = f, expr
	}
	if b, expr, err := decodeBoolOrExpr(w.ContinueOnError, "continue-on-error"); err != nil {
		return err
	} else {
		s.ContinueOnError, s.ContinueOnErrorExpr = b, expr
	}
	return nil
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
// Inputs carries workflow_call inputs resolved by the caller (`jobs.<id>.with`),
// exposed to the job's steps as the `inputs` expression context.
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
	// SplitN and SplitAuto are expanded at run start, not at compile.
	SplitN    int        `json:"split,omitempty"`
	SplitAuto bool       `json:"split_auto,omitempty"`
	Steps     []StepPlan `json:"steps"`
	// Inputs is set on jobs expanded from a reusable-workflow call: the
	// resolved `on.workflow_call` inputs, surfaced as the `inputs` context.
	Inputs map[string]string `json:"inputs,omitempty"`
	// Services are the job's service containers, started on a shared
	// per-job network before the first step.
	Services []ServicePlan `json:"services,omitempty"`
	// Permissions is the effective GitHub Actions `permissions:` ceiling for
	// this job — job-level when declared, else the workflow-level map. The
	// control plane uses it to scope the per-run GITHUB_TOKEN.
	Permissions map[string]string `json:"permissions,omitempty"`
	// SecretsMap is the reusable-caller `jobs.<id>.secrets` spec stamped onto
	// expanded jobs: nil delivers the full eligible secret set (normal jobs
	// and 'secrets: inherit'), an empty non-nil map delivers none (the caller
	// omitted secrets), otherwise called-name -> caller expression resolved
	// server-side when the runner claims secrets. No omitempty: the
	// nil/empty distinction is load-bearing.
	SecretsMap map[string]string `json:"secrets_map"`
}

// ServicePlan is one service container beside the job.
type ServicePlan struct {
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Env     map[string]string `json:"env,omitempty"`
	Options []string          `json:"options,omitempty"` // health-check docker flags only
	// Credentials authenticates the service image pull against a private
	// registry (services.<name>.credentials). The runner logs docker in with
	// a throwaway config dir for the pull only — never persisted.
	Credentials *RegistryCredentials `json:"credentials,omitempty"`
	// Ports carries normalised "host:container" or "container" entries. A
	// service that declares ports shares the job container's network
	// namespace, so localhost:<port> works as it does on a GitHub VM
	// runner; host:container remaps are honoured (loopback forwarder on
	// bridge jobs, real -p on host-net jobs) and the service name also
	// resolves to 127.0.0.1 inside the job.
	Ports []string `json:"ports,omitempty"`
}

// RegistryCredentials is a services.<name>.credentials or
// container.credentials mapping.
type RegistryCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
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
	Workflow *Workflow `json:"-"`
	// Deps maps resolved reusable-workflow uses refs to their YAML content,
	// captured during compile so a later recompile can resolve deterministically.
	Deps        map[string]string `json:"-"`
	Plan        *Plan             `json:"plan,omitempty"`
	Name        string            `json:"name"`
	Events      []string          `json:"events"`
	Valid       bool              `json:"valid"`
	Diagnostics []Diagnostic      `json:"diagnostics"`
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
