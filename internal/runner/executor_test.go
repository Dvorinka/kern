package runner

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestJobCAMount(t *testing.T) {
	got := jobCAMount()
	if _, err := os.Stat(hostCABundle); err != nil {
		if got != nil {
			t.Fatal(got)
		}
		return
	}
	if len(got) != 2 || got[1] != hostCABundle+":"+containerCABundle+":ro" {
		t.Fatal(got)
	}
	if env := caEnv(); len(env) == 0 || !strings.Contains(strings.Join(env, " "), containerCABundle) {
		t.Fatal(env)
	}
}

func TestImageValidation(t *testing.T) {
	for _, img := range []string{"ubuntu:24.04", "ghcr.io/dvorinka/kern-runner", "alpine", "node:22-bookworm@sha256:abc-_.0"} {
		if !imageRe.MatchString(img) {
			t.Errorf("valid image rejected: %q", img)
		}
	}
	for _, img := range []string{"ubuntu;rm -rf /", "img $(id)", "a b", "${VAR}", "img`id`"} {
		if imageRe.MatchString(img) {
			t.Errorf("unsafe image accepted: %q", img)
		}
	}
}

func TestParseKeyValue(t *testing.T) {
	got, err := parseKeyValue("# comment\nKEY=value\nEMPTY=\nspaced=1\n\n")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"KEY": "value", "EMPTY": "", "spaced": "1"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q: want %q got %q", k, v, got[k])
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected keys: %v", got)
	}
}

// @actions/core ≥1.11 emits every file command as `KEY<<ghadelimiter_<uuid>`
// heredoc blocks; both shapes must parse or node-action env/outputs vanish.
func TestParseKeyValueHeredoc(t *testing.T) {
	in := "ANDROID_HOME<<ghadelimiter_aaa\n/github/home/.android/sdk\nghadelimiter_aaa\n" +
		"MULTI<<ghadelimiter_bbb\nline1\nline2=x\nghadelimiter_bbb\n" +
		"PLAIN=still\n"
	got, err := parseKeyValue(in)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ANDROID_HOME": "/github/home/.android/sdk",
		"MULTI":        "line1\nline2=x",
		"PLAIN":        "still",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q: want %q got %q", k, v, got[k])
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected keys: %v", got)
	}
}

func TestParsePathLines(t *testing.T) {
	in := "/kern/node/v20.18.1/bin\n" +
		"PATH<<ghadelimiter_x\n/kern/cache/_toolcache/go/1.26.10/x64/bin\nghadelimiter_x\n" +
		"/usr/local/custom/bin\n"
	got := parsePathLines(in)
	want := []string{
		"/kern/node/v20.18.1/bin",
		"/kern/cache/_toolcache/go/1.26.10/x64/bin",
		"/usr/local/custom/bin",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestPathLinesTrailingNewline(t *testing.T) {
	b := newEnvBuilder(&Claim{}, nil)
	if got := b.pathLines(); got != "" {
		t.Fatalf("empty: %q", got)
	}
	b.persistPaths([]string{"/kern/node/v20.18.1/bin"})
	got := b.pathLines()
	if got != "/kern/node/v20.18.1/bin\n" {
		t.Fatalf("%q", got)
	}
	// File commands append onto this file (bare lines or PATH<<delim blocks);
	// a missing trailing newline would glue the next entry onto this path.
	appended := got + "/kern/cache/_toolcache/go/1.26.10/x64/bin\n"
	lines := strings.Split(strings.TrimRight(appended, "\n"), "\n")
	if len(lines) != 2 || lines[0] != "/kern/node/v20.18.1/bin" || lines[1] != "/kern/cache/_toolcache/go/1.26.10/x64/bin" {
		t.Fatal(lines)
	}
}

func TestEnvBuilderRender(t *testing.T) {
	claim := &Claim{}
	claim.Job.Env = map[string]string{"V_JOB": "1"}
	e := newEnvBuilder(claim, map[string]string{"SECRET_TOKEN": "hunter2", "BAD KEY": "x"})
	e.persistKV("PERSISTED", "yes")
	e.persistPaths([]string{"/first", " ", "/second"})
	step := StepSpec{Env: map[string]string{"V_STEP": "2", "MULTI": "a\nb"}}
	content, extra := e.render(step)

	for _, want := range []string{"V_JOB=1", "PERSISTED=yes", "SECRET_TOKEN=hunter2", "V_STEP=2"} {
		if !strings.Contains(content, want) {
			t.Errorf("env file missing %q\n%s", want, content)
		}
	}
	if strings.Contains(content, "BAD KEY=") {
		t.Errorf("env key with space produced invalid line:\n%s", content)
	}
	if strings.Contains(content, "MULTI=") || extra["MULTI"] != "a\nb" {
		t.Errorf("multi-line values must ride -e args, not the env file: %q %v", content, extra)
	}
}

func TestEnvBuilderRenderWhitespaceAndPrecedence(t *testing.T) {
	// Single-line values with spaces (minified or inline JSON) are valid
	// env-file content — docker keeps everything after the first `=`.
	claim := &Claim{}
	claim.Job.Env = map[string]string{"JOB_MULTI": "j1\nj2", "SHARED": "job-env"}
	e := newEnvBuilder(claim, map[string]string{
		"SA_JSON": `{"type": "service_account", "project_id": "x"}`,
		"SHARED":  "sec1\nsec2",
	})
	step := StepSpec{Env: map[string]string{"SHARED": "step-wins", "PEM": "-----BEGIN KEY-----\nabc\n-----END KEY-----\n"}}
	content, extra := e.render(step)

	if !strings.Contains(content, `SA_JSON={"type": "service_account", "project_id": "x"}`) {
		t.Errorf("single-line spaced value must stay in the env file:\n%s", content)
	}
	if strings.Contains(content, "PEM=") || extra["PEM"] != "-----BEGIN KEY-----\nabc\n-----END KEY-----" {
		t.Errorf("PEM key must ride -e with newlines preserved: %q %v", content, extra["PEM"])
	}
	// SHARED resolves multi-line in secrets but single-line in step.Env:
	// the later layer wins, so the stale extra entry must be dropped.
	if !strings.Contains(content, "SHARED=step-wins") {
		t.Errorf("env file missing step-level SHARED:\n%s", content)
	}
	if _, ok := extra["SHARED"]; ok {
		t.Errorf("stale extra would override the newer file value: %v", extra)
	}
}

func TestCloneArgs(t *testing.T) {
	pub := cloneArgs(ClaimRun{RepoURL: "https://github.com/o/r.git"}, "/ws")
	if got := strings.Join(pub, " "); got != "clone --quiet --depth 1 -- https://github.com/o/r.git /ws" {
		t.Errorf("public: %q", got)
	}
	priv := cloneArgs(ClaimRun{RepoURL: "https://github.com/o/r.git", SHA: strings.Repeat("a", 40), CloneToken: "tok"}, "/ws")
	want := "http.extraHeader=Authorization: Basic " +
		base64.StdEncoding.EncodeToString([]byte("x-access-token:tok"))
	if priv[1] != want {
		t.Errorf("missing auth header: %v", priv)
	}
	for _, a := range priv {
		if a == "--depth" {
			t.Errorf("sha checkout must keep full history: %v", priv)
		}
	}
}

func TestPrivilegedExecUID(t *testing.T) {
	e := &executor{claim: &Claim{}}
	e.claim.Run.TrustLevel = "fork"
	if e.execUID() != jobUID {
		t.Error("untrusted runs must stay uid 1000")
	}
	e.claim.Run.TrustLevel = "trusted"
	if e.execUID() != 0 {
		t.Error("trusted runs must run as root")
	}
}

func TestSanitizeID(t *testing.T) {
	if got := sanitizeID("abc/def-123"); got != "abc-def-123" {
		t.Errorf("sanitizeID: got %q", got)
	}
}

func TestJobStatusSoFarAndContext(t *testing.T) {
	cfg := Config{WorkRoot: t.TempDir()}
	claim := &Claim{}
	claim.Job.Image = "ubuntu:24.04"
	claim.Job.ID = "test-job-1"
	e, err := newJobExecutor(cfg, &client{}, claim, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if e.jobStatusSoFar() != "" {
		t.Errorf("fresh executor status = %q", e.jobStatusSoFar())
	}
	e.stepResults["s1"] = &stepOutcome{Conclusion: "success", Outcome: "success"}
	if got := e.jobStatusSoFar(); got != "" {
		t.Errorf("after success status = %q", got)
	}
	e.stepResults["s2"] = &stepOutcome{Conclusion: "failure", Outcome: "failure"}
	if got := e.jobStatusSoFar(); got != "failure" {
		t.Errorf("after failure status = %q", got)
	}
	ctx := e.runnerContext()
	if _, ok := ctx["github"]; !ok {
		t.Errorf("context missing github")
	}
	if _, ok := ctx["steps"]; !ok {
		t.Errorf("context missing steps")
	}
}

func TestOrphanedContainers(t *testing.T) {
	pre := map[string]bool{"aaa111": true, "bbb222": true}
	ps := "aaa111|kern-job-old-1\n" +
		"ccc333|cal\n" +
		"ddd444|workspace-postgres-1\n" +
		"eee555|buildx_buildkit_builder-abc0\n" +
		"fff666|kern-svc-123-postgres\n" +
		"ggg777|\n" + // anonymous spawn
		"hhh888|\n"
	got := orphanedContainers(pre, ps)
	want := map[string]bool{"ccc333": true, "ddd444": true, "ggg777": true, "hhh888": true}
	if len(got) != len(want) {
		t.Fatalf("orphanedContainers = %v, want %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected id %s in %v", id, got)
		}
	}
}

func TestPersistGitPushAuth(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	ws := t.TempDir()
	if out, err := exec.Command("git", "-C", ws, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	e := &executor{claim: &Claim{Run: ClaimRun{
		RepoURL:     "https://github.com/o/r.git",
		GitHubToken: "ghs_testtoken",
	}}}
	e.persistGitPushAuth(context.Background(), ws)
	want := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghs_testtoken"))
	out, err := exec.Command("git", "-C", ws, "config", "--get", "http.https://github.com/.extraheader").Output()
	if err != nil {
		t.Fatalf("read extraheader: %v", err)
	}
	if strings.TrimSpace(string(out)) != want {
		t.Fatalf("extraheader = %q, want %q", strings.TrimSpace(string(out)), want)
	}

	// No token → nothing persisted.
	ws2 := t.TempDir()
	if out, err := exec.Command("git", "-C", ws2, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	e2 := &executor{claim: &Claim{Run: ClaimRun{RepoURL: "https://github.com/o/r.git"}}}
	e2.persistGitPushAuth(context.Background(), ws2)
	if _, err := exec.Command("git", "-C", ws2, "config", "--get", "http.https://github.com/.extraheader").Output(); err == nil {
		t.Fatal("extraheader persisted without a token")
	}
}

func TestRunnerContextSecretsVarsAndEnvResolution(t *testing.T) {
	cfg := Config{WorkRoot: t.TempDir()}
	claim := &Claim{}
	claim.Job.Image = "ubuntu:24.04"
	claim.Job.ID = "test-job-1"
	claim.Job.Env = map[string]string{
		"KEYSTORE_BASE64": "${{ secrets.ANDROID_KEYSTORE_BASE64 }}",
		"GREETING":        "hello ${{ matrix.name }}",
		"PLAIN":           "static",
	}
	claim.Job.Matrix = map[string]any{"name": "world"}
	secretsMap := map[string]string{"ANDROID_KEYSTORE_BASE64": "c2VjcmV0MTIz"}
	e, err := newJobExecutor(cfg, &client{}, claim, secretsMap, nil, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx := e.runnerContext()

	sec, ok := ctx["secrets"].(map[string]any)
	if !ok {
		t.Fatalf("secrets context missing or wrong type: %T", ctx["secrets"])
	}
	if sec["ANDROID_KEYSTORE_BASE64"] != "c2VjcmV0MTIz" {
		t.Errorf("secrets context: %v", sec)
	}
	if vars, ok := ctx["vars"].(map[string]any); !ok || len(vars) != 0 {
		t.Errorf("vars context should be an empty map, got %v", ctx["vars"])
	}
	env, ok := ctx["env"].(map[string]any)
	if !ok {
		t.Fatalf("env context missing: %T", ctx["env"])
	}
	if env["KEYSTORE_BASE64"] != "c2VjcmV0MTIz" {
		t.Errorf("env.KEYSTORE_BASE64 = %v, want resolved secret", env["KEYSTORE_BASE64"])
	}
	if env["GREETING"] != "hello world" {
		t.Errorf("env.GREETING = %v, want matrix interpolation", env["GREETING"])
	}
	if env["PLAIN"] != "static" {
		t.Errorf("env.PLAIN = %v", env["PLAIN"])
	}
}

// $GITHUB_ENV writes persist into the env expression context for later
// steps, matching GitHub — the Nothing_Modes keystore decode incident was a
// persisted value resolving empty in the next step's env:.
func TestRunnerContextPersistedEnv(t *testing.T) {
	cfg := Config{WorkRoot: t.TempDir()}
	claim := &Claim{}
	claim.Job.Image = "ubuntu:24.04"
	claim.Job.ID = "test-job-env"
	claim.Job.Env = map[string]string{"MODE": "job"}
	e, err := newJobExecutor(cfg, &client{}, claim, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	e.enc.persistKV("NOTHING_MODES_KEYSTORE", "/kern/home/keystore.jks")
	e.enc.persistKV("MODE", "persisted") // later writes win, like GitHub
	ctx := e.runnerContext()
	env := ctx["env"].(map[string]any)
	if env["NOTHING_MODES_KEYSTORE"] != "/kern/home/keystore.jks" {
		t.Errorf("persisted env missing: %v", env)
	}
	if env["MODE"] != "persisted" {
		t.Errorf("persisted value should override job env, got %v", env["MODE"])
	}
	// The step's own env: belongs to the env context for that step.
	e.stepEnvCtx = map[string]string{"STEP_ONLY": "yes"}
	env = e.runnerContext()["env"].(map[string]any)
	if env["STEP_ONLY"] != "yes" {
		t.Errorf("step env missing from context: %v", env)
	}
}

// Release run 92c54930 wrote NOTHING_MODES_KEYSTORE to $GITHUB_ENV in the
// Decode step; the next step's `env:` evaluated ${{ env.X }} as empty and
// failed "Cannot convert '' to File". Cover the whole chain: file write →
// harvestEnv → per-step env: interpolation → env-file render.
func TestGithubEnvParityPerStep(t *testing.T) {
	cfg := Config{WorkRoot: t.TempDir()}
	claim := &Claim{}
	claim.Job.Image = "ubuntu:24.04"
	claim.Job.ID = "test-env-parity"
	e, err := newJobExecutor(cfg, &client{}, claim, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	cmdDir := filepath.Join(e.workRoot(), "cmd")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "GITHUB_ENV"),
		[]byte("NOTHING_MODES_KEYSTORE=/kern/home/keystore.jks\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.harvestEnv()

	step := StepSpec{Number: 2, Env: map[string]string{
		"KS":   "${{ env.NOTHING_MODES_KEYSTORE }}",
		"DER":  "${{ env.NOTHING_MODES_KEYSTORE }}.der",
		"LIT":  "plain",
		"MISS": "${{ env.NO_SUCH_VAR }}",
	}}
	step.Env = e.interpolateEnv(step.Env)
	if step.Env["KS"] != "/kern/home/keystore.jks" {
		t.Fatalf("env: interpolation of persisted var = %q", step.Env["KS"])
	}
	if step.Env["DER"] != "/kern/home/keystore.jks.der" {
		t.Fatalf("compound interpolation = %q", step.Env["DER"])
	}
	if step.Env["MISS"] != "" {
		t.Fatalf("missing var should interpolate empty, got %q", step.Env["MISS"])
	}

	content, _ := e.enc.render(step)
	if !strings.Contains(content, "KS=/kern/home/keystore.jks\n") {
		t.Fatalf("env file missing resolved step env:\n%s", content)
	}
	if !strings.Contains(content, "NOTHING_MODES_KEYSTORE=/kern/home/keystore.jks\n") {
		t.Fatalf("env file missing persisted var:\n%s", content)
	}
}

func TestRunnerContextSecretsEmptyWhenWithheld(t *testing.T) {
	cfg := Config{WorkRoot: t.TempDir()}
	claim := &Claim{}
	claim.Job.Image = "ubuntu:24.04"
	claim.Job.ID = "test-job-1"
	claim.Job.Env = map[string]string{"TOKEN": "${{ secrets.GITHUB_TOKEN }}"}
	// nil secretMap models an untrusted run: secrets withheld.
	e, err := newJobExecutor(cfg, &client{}, claim, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx := e.runnerContext()
	env := ctx["env"].(map[string]any)
	if env["TOKEN"] != "" {
		t.Errorf("withheld secret should resolve empty, got %v", env["TOKEN"])
	}
}

func TestWSContainerPerJobForPrivileged(t *testing.T) {
	cfg := Config{WorkRoot: t.TempDir()}
	claim := &Claim{}
	claim.Job.Image = "ubuntu:24.04"
	claim.Job.ID = "job-1"

	e, err := newJobExecutor(cfg, &client{}, claim, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if got := e.wsContainer(); got != containerWorkspace {
		t.Errorf("untrusted wsContainer = %q, want %q", got, containerWorkspace)
	}

	claim.Run.TrustLevel = "trusted"
	e2, err := newJobExecutor(cfg, &client{}, claim, nil, nil, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	want := "/github/workspaces/job-1"
	if got := e2.wsContainer(); got != want {
		t.Errorf("trusted wsContainer = %q, want %q", got, want)
	}
}

func TestRefNameAndType(t *testing.T) {
	cases := []struct{ ref, name, typ string }{
		{"refs/tags/v0.6.3", "v0.6.3", "tag"},
		{"refs/heads/master", "master", "branch"},
		{"refs/pull/242/head", "242/head", "branch"},
		{"v0.6.3", "v0.6.3", "branch"},
	}
	for _, c := range cases {
		if got := refName(c.ref); got != c.name {
			t.Errorf("refName(%q) = %q, want %q", c.ref, got, c.name)
		}
		if got := refType(c.ref); got != c.typ {
			t.Errorf("refType(%q) = %q, want %q", c.ref, got, c.typ)
		}
	}
}

// kern#247: the release pattern echo "version=${GITHUB_REF_NAME#v}" >>
// $GITHUB_OUTPUT wrote an empty output because ref_name was expression-only.
func TestJobEnvGitHubContract(t *testing.T) {
	e := &executor{
		cfg: Config{WorkRoot: t.TempDir(), Name: "test"},
		c:   &client{},
		claim: &Claim{Run: ClaimRun{
			Ref:          "refs/tags/v1.3.0",
			RepoFullName: "o/r",
			RepoURL:      "https://github.com/o/r.git",
			Actor:        "dvorak",
			HeadRef:      "feature/x",
			Source:       map[string]any{"base_ref": "main"},
		}},
	}
	env := e.jobEnv()
	for k, want := range map[string]string{
		"GITHUB_REF_NAME":         "v1.3.0",
		"GITHUB_REF_TYPE":         "tag",
		"GITHUB_HEAD_REF":         "feature/x",
		"GITHUB_BASE_REF":         "main",
		"GITHUB_REPOSITORY_OWNER": "o",
		"GITHUB_RUN_ATTEMPT":      "0",
		"GITHUB_TRIGGERING_ACTOR": "dvorak",
		"GITHUB_GRAPHQL_URL":      "https://api.github.com/graphql",
	} {
		if env[k] != want {
			t.Errorf("jobEnv[%s] = %q, want %q", k, env[k], want)
		}
	}
	// No base_ref in Source → key absent (GitHub only sets it on PR runs).
	e.claim.Run.Source = nil
	if _, ok := e.jobEnv()["GITHUB_BASE_REF"]; ok {
		t.Error("GITHUB_BASE_REF set without a PR base")
	}
}

func TestPathExists(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "marker")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !pathExists(f) {
		t.Error("pathExists(file) = false, want true")
	}
	if !pathExists(dir) {
		t.Error("pathExists(dir) = false, want true")
	}
	if pathExists(filepath.Join(dir, "missing")) {
		t.Error("pathExists(missing) = true, want false")
	}
}

// The audit line carries names only — a secret VALUE in the log header would
// defeat the masker. Assert names appear and no value does.
func TestEnvAuditLine(t *testing.T) {
	e := &executor{
		cfg: Config{WorkRoot: t.TempDir(), Name: "test"},
		c:   &client{},
		claim: &Claim{Run: ClaimRun{
			Ref: "refs/heads/main", RepoFullName: "o/r",
		}},
		secrets: map[string]string{"NPM_TOKEN": "s3cr3t-value", "GITHUB_TOKEN": "ghp_x"},
		vars:    map[string]string{"REGISTRY": "npm.example"},
	}
	e.claim.Job.Env = map[string]string{"MY_FLAG": "on"}
	line := e.envAuditLine()
	for _, name := range []string{"MY_FLAG", "NPM_TOKEN", "GITHUB_TOKEN", "REGISTRY", "GITHUB_SHA"} {
		if !strings.Contains(line, name) {
			t.Errorf("audit line missing %s: %s", name, line)
		}
	}
	for _, val := range []string{"s3cr3t-value", "ghp_x", "npm.example"} {
		if strings.Contains(line, val) {
			t.Errorf("audit line leaked value %q: %s", val, line)
		}
	}
	if !strings.HasPrefix(line, "KERN: env: ") {
		t.Errorf("bad prefix: %q", line)
	}
}
