package runner

// Composite action runtime. A `uses: ./path` step reads action.yml from the
// checked-out workspace; `uses: owner/repo[/path]@ref` is fetched by the
// runner into the job's actions dir (mounted at /kern/actions — read-only
// for sandboxed jobs, writable for trusted ones).
// runs.using: composite executes its steps inline through the same docker-exec
// machinery as workflow steps; node*/docker runtimes fail with an explicit
// diagnostic rather than being skipped.

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dvorinka/kern/internal/actions"
	"github.com/dvorinka/kern/internal/expressions"
	"go.uber.org/zap"
)

const (
	containerActionsDir = "/kern/actions"
	maxCompositeDepth   = 10
)

// runAction executes a `uses` reference of any kind. evalCtx is the expression
// context the step's `with:` values interpolate against — the runner context
// for top-level steps, the composite context for nested ones.
func (e *executor) runAction(ctx context.Context, step StepSpec, evalCtx map[string]any, depth int) (string, int) {
	for k, v := range step.With {
		if str, ok := v.(string); ok {
			if nv, err := expressions.Interpolate(str, evalCtx); err == nil {
				step.With[k] = nv
			}
		}
	}
	ref := strings.TrimSpace(step.Uses)
	switch actions.Classify(ref) {
	case actions.KindNative:
		return e.runNativeAction(ctx, step, ref)
	case actions.KindLocal:
		if err := actions.ValidLocal(ref); err != nil {
			e.logStep(step, "KERN: "+err.Error())
			return "failure", 1
		}
		dir := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(ref)), "./")
		host := filepath.Join(e.workRoot(), "workspace", filepath.FromSlash(dir))
		return e.runActionDir(ctx, step, host, e.wsPath()+"/"+dir, evalCtx, depth)
	case actions.KindRemote:
		r, err := actions.ParseRemote(ref)
		if err != nil {
			e.logStep(step, "KERN: "+err.Error())
			return "failure", 1
		}
		host, container, err := e.fetchAction(ctx, step, r)
		if err != nil {
			e.logStep(step, "KERN: fetch action "+ref+": "+err.Error())
			return "failure", 1
		}
		return e.runActionDir(ctx, step, host, container, evalCtx, depth)
	case actions.KindDocker:
		return e.runDockerRef(ctx, step)
	}
	e.logStep(step, "KERN: action reference "+ref+" is malformed")
	return "failure", 1
}

// runNativeAction keeps the pre-M5 native actions behind their own switch.
func (e *executor) runNativeAction(ctx context.Context, step StepSpec, ref string) (string, int) {
	switch strings.ToLower(strings.SplitN(ref, "@", 2)[0]) {
	case "actions/checkout":
		return e.secondaryCheckout(ctx, step)
	case "actions/upload-artifact":
		return e.artifactUpload(step)
	case "actions/download-artifact":
		return e.artifactDownload(step)
	case "actions/cache":
		return e.cacheRestoreStep(step, true)
	case "actions/cache/restore":
		return e.cacheRestoreStep(step, false)
	case "actions/cache/save":
		return e.cacheSaveStep(step)
	}
	e.logStep(step, "KERN: action "+ref+" is not supported")
	return "failure", 1
}

// secondaryCheckout implements `actions/checkout` with `repository:`/`path:`.
// The run's own repository is materialised during prepare, so a plain
// checkout is already satisfied; a second one clones another repo (public
// https, or private when the run's token can read it) into the workspace.
func (e *executor) secondaryCheckout(ctx context.Context, step StepSpec) (string, int) {
	repository := withString(step, "repository", "")
	path := withString(step, "path", "")
	if repository == "" && path == "" {
		// The workspace was checked out during prepare; an explicit step keeps that state.
		return "success", 0
	}
	run := e.claim.Run
	if repository == "" {
		repository = run.RepoFullName
	}
	ref := withString(step, "ref", "")
	if path == "" {
		path = strings.TrimSuffix(filepath.Base(repository), ".git")
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		e.logStep(step, "KERN: checkout path must stay inside the workspace")
		return "failure", 1
	}
	dest := filepath.Join(e.workRoot(), "workspace", clean)
	if entries, err := os.ReadDir(dest); err == nil && len(entries) > 0 {
		return "success", 0 // already populated by an earlier checkout
	}
	url := repositoryURL(repository, run.RepoURL)
	args := []string{}
	if run.CloneToken != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + run.CloneToken))
		args = append(args, "-c", "http.extraHeader=Authorization: Basic "+basic)
	}
	looksSHA := len(ref) == 40 && !strings.ContainsAny(ref, "ghijklmnopqrstuvwxyz/")
	if ref != "" && !looksSHA {
		args = append(args, "clone", "--quiet", "--depth", "1", "--branch", ref, "--", url, dest)
	} else {
		args = append(args, "clone", "--quiet", "--", url, dest)
	}
	out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
	if err != nil {
		e.logStep(step, "KERN: checkout "+repository+": "+err.Error()+" ("+tailStr(string(out), 300)+")")
		return "failure", 1
	}
	if looksSHA {
		if out, err := exec.CommandContext(ctx, "git", "-C", dest, "checkout", "--force", ref).CombinedOutput(); err != nil {
			e.logStep(step, "KERN: checkout ref: "+err.Error()+" ("+tailStr(string(out), 300)+")")
			return "failure", 1
		}
	}
	chownTree(dest, e.execUID())
	return "success", 0
}

// repositoryURL turns a `repository:` input into a clone URL on the same host
// as the run's repository. A value that is already a URL is used verbatim.
func repositoryURL(repository, runRepoURL string) string {
	if strings.Contains(repository, "://") || strings.HasPrefix(repository, "git@") {
		return repository
	}
	host := "github.com"
	if runRepoURL != "" {
		if u, err := url.Parse(runRepoURL); err == nil && u.Host != "" {
			host = u.Host
		}
	}
	return "https://" + host + "/" + repository + ".git"
}

// runActionDir loads action.yml and dispatches on runs.using. actionPath is
// the container-side directory exported as GITHUB_ACTION_PATH.
func (e *executor) runActionDir(ctx context.Context, step StepSpec, hostDir, actionPath string, evalCtx map[string]any, depth int) (string, int) {
	meta, err := readActionMeta(hostDir)
	if err != nil {
		e.logStep(step, "KERN: "+err.Error())
		return "failure", 1
	}
	switch meta.Runs.Using {
	case "composite":
		return e.runComposite(ctx, step, meta, actionPath, evalCtx, depth)
	case "node12", "node16", "node20", "node24":
		return e.runNodeAction(ctx, step, meta, actionPath)
	case "docker":
		return e.runDockerAction(ctx, step, meta, hostDir, actionPath, evalCtx)
	default:
		e.logStep(step, fmt.Sprintf("KERN: action %q has unsupported runs.using %q", step.Uses, meta.Runs.Using))
		return "failure", 1
	}
}

// readActionMeta parses action.yml (or action.yaml) from an action directory.
func readActionMeta(dir string) (*actions.Meta, error) {
	for _, name := range []string{"action.yml", "action.yaml"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil {
			return actions.ParseMeta(data)
		}
	}
	return nil, fmt.Errorf("no action.yml in %s", dir)
}

// populateAction fills base with the action repository at the requested ref,
// preferring the SHA-keyed persistent cache over a direct shallow clone.
func (e *executor) populateAction(ctx context.Context, base string, r actions.RemoteRef) error {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	repoURL := e.actionRepoBase() + "/" + r.Owner + "/" + r.Repo
	fetched := false
	if sha := e.resolveActionSHA(ctx, repoURL, r.Ref); sha != "" {
		if err := e.fetchViaActionCache(ctx, repoURL, r, sha, base); err != nil {
			e.log.Debug("action cache path failed, fetching directly",
				zap.String("repo", repoURL), zap.Error(err))
		} else {
			fetched = true
		}
	}
	if !fetched {
		git := func(args ...string) error {
			cmd := exec.CommandContext(ctx, "git", append([]string{"-C", base}, args...)...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("git %s: %s", strings.Join(args, " "), tailStr(string(out), 300))
			}
			return nil
		}
		if err := git("init", "-q"); err != nil {
			return err
		}
		if err := git("remote", "add", "origin", repoURL); err != nil {
			return err
		}
		// fetch by name works for branches and tags; GitHub also serves
		// reachable commit SHAs, which covers pinned actions.
		if err := git("fetch", "--depth", "1", "origin", r.Ref); err != nil {
			return fmt.Errorf("ref %s not fetchable from %s (public https only)", r.Ref, repoURL)
		}
		if err := git("checkout", "-q", "FETCH_HEAD"); err != nil {
			return err
		}
	}
	_ = os.RemoveAll(filepath.Join(base, ".git"))
	return nil
}

// missingActionEntrypoints lists the entry files action.yml declares that are
// absent from dir — node actions need main (and pre/post when set), docker
// actions need a Dockerfile when no image is given. Composite actions declare
// no entry files.
func missingActionEntrypoints(dir string) []string {
	meta, err := readActionMeta(dir)
	if err != nil {
		return nil // readActionMeta reports this itself downstream
	}
	var missing []string
	check := func(rel string) {
		if rel == "" {
			return
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			missing = append(missing, rel)
		}
	}
	switch meta.Runs.Using {
	case "node12", "node16", "node20", "node24":
		check(meta.Runs.Main)
		check(meta.Runs.Pre)
		check(meta.Runs.Post)
	case "docker":
		if strings.EqualFold(strings.TrimSpace(meta.Runs.Image), "Dockerfile") {
			check("Dockerfile")
		}
	}
	return missing
}

// fetchAction clones an action repository at the requested ref into the job's
// actions dir and returns the host and container paths of the action
// directory. Fetches are memoised per job. Only public repositories over
// https are supported.
// ponytail: no credential support — the run's clone URL is never reused, so
// private action repos fail loudly; upgrade if private actions are requested.
func (e *executor) fetchAction(ctx context.Context, step StepSpec, r actions.RemoteRef) (string, string, error) {
	key := nonIDRe.ReplaceAllString(r.Owner+"__"+r.Repo+"__"+r.Ref, "-")
	if e.actionDirs == nil {
		e.actionDirs = map[string]string{}
	}
	base, ok := e.actionDirs[key]
	if !ok {
		base = filepath.Join(e.workRoot(), "actions", key)
		// A partial copy once shipped action trees missing their entrypoint
		// (node ran, dist/index.js did not). Verify entry files after fetch
		// and retry once on a clean dir before giving up.
		for attempt := 0; attempt < 2; attempt++ {
			if attempt > 0 {
				_ = os.RemoveAll(base)
			}
			if err := e.populateAction(ctx, base, r); err != nil {
				return "", "", err
			}
			missing := missingActionEntrypoints(filepath.Join(base, filepath.FromSlash(r.Path)))
			if len(missing) == 0 {
				break
			}
			if attempt == 1 {
				return "", "", fmt.Errorf("action %s/%s@%s fetched incomplete (missing %s)",
					r.Owner, r.Repo, r.Ref, strings.Join(missing, ", "))
			}
			e.log.Debug("action fetch incomplete; retrying",
				zap.String("repo", r.Owner+"/"+r.Repo), zap.Strings("missing", missing))
		}
		e.actionDirs[key] = base
	}
	host := filepath.Join(base, filepath.FromSlash(r.Path))
	if _, err := os.Stat(host); err != nil {
		return "", "", fmt.Errorf("action path %q not found in %s/%s@%s", r.Path, r.Owner, r.Repo, r.Ref)
	}
	container := e.actionsPath() + "/" + key
	if r.Path != "" {
		container += "/" + r.Path
	}
	return host, container, nil
}

// --- persistent action cache ----------------------------------------------
//
// Remote actions were re-cloned per job. They are now cached under
// _cache/_actions keyed by resolved commit SHA — content-addressed, so a
// branch that moved simply produces a new key and old entries age out.
// Each job still gets its own copy under the job workdir: the container
// mount stays per-job, and trusted jobs keep their read-write actions mount
// without ever touching the shared cache.

var actionCacheLocks sync.Map // cacheKey → *sync.Mutex

func actionCacheRoot(workRoot string) string {
	return filepath.Join(cacheDir(workRoot), "_actions")
}

// isActionSHA reports whether ref is already a pinned commit SHA.
func isActionSHA(ref string) bool {
	if len(ref) != 40 && len(ref) != 64 {
		return false
	}
	for _, c := range ref {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// parseLSRemote picks the best ref from `git ls-remote` output for the
// requested ref: a branch, a peeled annotated tag (^{}, i.e. the commit),
// a lightweight tag, or an exact full refname — in that order.
func parseLSRemote(out, ref string) string {
	lines := strings.Split(out, "\n")
	want := []string{
		"refs/heads/" + ref,
		"refs/tags/" + ref + "^{}",
		"refs/tags/" + ref,
		ref,
	}
	for _, w := range want {
		for _, ln := range lines {
			f := strings.Fields(ln)
			if len(f) == 2 && f[1] == w {
				return f[0]
			}
		}
	}
	for _, ln := range lines {
		f := strings.Fields(ln)
		if len(f) == 2 && (f[1] == ref || strings.HasSuffix(f[1], "/"+ref)) {
			return f[0]
		}
	}
	return ""
}

func (e *executor) refPointerPath(repoURL, ref string) string {
	return filepath.Join(actionCacheRoot(e.cfg.WorkRoot), "refs",
		nonIDRe.ReplaceAllString(repoURL+"__"+ref, "-"))
}

func (e *executor) rememberRefSHA(repoURL, ref, sha string) {
	p := e.refPointerPath(repoURL, ref)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte(sha+"\n"), 0o644)
}

func (e *executor) cachedRefSHA(repoURL, ref string) string {
	b, err := os.ReadFile(e.refPointerPath(repoURL, ref))
	if err != nil {
		return ""
	}
	sha := strings.TrimSpace(string(b))
	if !isActionSHA(sha) {
		return ""
	}
	return sha
}

// resolveActionSHA maps a mutable ref (branch/tag) to a commit SHA so the
// cache key is content-addressed. Pinned SHAs pass through untouched.
// Returns "" when the ref cannot be resolved — the caller then fetches
// directly, preserving the old behaviour for unreachable git hosts.
func (e *executor) resolveActionSHA(ctx context.Context, repoURL, ref string) string {
	if isActionSHA(ref) {
		return strings.ToLower(ref)
	}
	ctxCmd, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctxCmd, "git", "ls-remote", repoURL, ref).CombinedOutput()
	if err != nil {
		if sha := e.cachedRefSHA(repoURL, ref); sha != "" {
			e.log.Debug("ls-remote failed; using last resolved SHA",
				zap.String("repo", repoURL), zap.String("ref", ref), zap.Error(err))
			return sha
		}
		return ""
	}
	if sha := parseLSRemote(string(out), ref); sha != "" {
		e.rememberRefSHA(repoURL, ref, sha)
		return sha
	}
	return ""
}

// fetchViaActionCache exports the cached tree for sha into the job's actions
// dir. The cache itself lives outside the job dir so concurrent jobs share
// one clone; .git is stripped because actions run without it anyway (the
// official runner downloads actions as tarballs).
func (e *executor) fetchViaActionCache(ctx context.Context, repoURL string, r actions.RemoteRef, sha, base string) error {
	cacheKey := nonIDRe.ReplaceAllString(r.Owner+"__"+r.Repo+"__"+sha, "-")
	src, err := e.ensureActionCache(ctx, repoURL, cacheKey, sha, r.Ref)
	if err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "cp", "-a", src+string(os.PathSeparator)+".", base+string(os.PathSeparator)).CombinedOutput(); err != nil {
		return fmt.Errorf("copy action cache: %s", tailStr(string(out), 200))
	}
	_ = os.RemoveAll(filepath.Join(base, ".git"))
	e.maybePruneActionCache(filepath.Dir(src))
	return nil
}

func actionCacheLock(key string) *sync.Mutex {
	v, _ := actionCacheLocks.LoadOrStore(key, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func actionCacheGitDir(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && st.IsDir()
}

// ensureActionCache returns the populated cache dir for cacheKey, cloning
// into a temp dir and renaming into place so concurrent jobs never observe a
// half-fetched tree. sha is tried first; hosts that refuse SHA fetches fall
// back to the original ref.
func (e *executor) ensureActionCache(ctx context.Context, repoURL, cacheKey, sha, ref string) (string, error) {
	root := actionCacheRoot(e.cfg.WorkRoot)
	dir := filepath.Join(root, cacheKey)
	if actionCacheGitDir(dir) {
		touchDir(dir)
		return dir, nil
	}
	mu := actionCacheLock(cacheKey)
	mu.Lock()
	defer mu.Unlock()
	if actionCacheGitDir(dir) {
		touchDir(dir)
		return dir, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(root, ".tmp-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	git := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", tmp}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %s", strings.Join(args, " "), tailStr(string(out), 300))
		}
		return nil
	}
	if err := git("init", "-q"); err != nil {
		return "", err
	}
	if err := git("remote", "add", "origin", repoURL); err != nil {
		return "", err
	}
	fetchErr := git("fetch", "--depth", "1", "origin", sha)
	if fetchErr != nil && ref != sha {
		fetchErr = git("fetch", "--depth", "1", "origin", ref)
	}
	if fetchErr != nil {
		return "", fmt.Errorf("ref %s not fetchable from %s (public https only)", ref, repoURL)
	}
	if err := git("checkout", "-q", "FETCH_HEAD"); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		if actionCacheGitDir(dir) {
			return dir, nil
		}
		return "", err
	}
	return dir, nil
}

func touchDir(dir string) {
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
}

// maybePruneActionCache sweeps cache entries unused for 7 days, once per
// job. Entries in use get their mtime refreshed on every hit, so hot actions
// stay resident and cold ones age out.
func (e *executor) maybePruneActionCache(root string) {
	if e.actionCachePruned {
		return
	}
	e.actionCachePruned = true
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, en := range entries {
		if !en.IsDir() {
			continue
		}
		name := en.Name()
		if name == "refs" {
			continue
		}
		info, err := en.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, name))
	}
}

// actionRepoBase derives the git host for remote actions from the run's clone
// URL (GHES-friendly), defaulting to github.com. Credentials are never
// forwarded to other repositories.
func (e *executor) actionRepoBase() string {
	if b := strings.TrimRight(e.cfg.ActionsGitBase, "/"); b != "" {
		return b
	}
	if u := e.claim.Run.RepoURL; u != "" {
		if i := strings.Index(u, "://"); i >= 0 {
			rest := u[i+3:]
			if j := strings.IndexByte(rest, '/'); j > 0 {
				host := rest[:j]
				if k := strings.LastIndexByte(host, '@'); k >= 0 {
					host = host[k+1:] // strip userinfo
				}
				if host != "" {
					return u[:i+3] + host
				}
			}
		}
	}
	return "https://github.com"
}

// resolveInputs merges declared inputs, their defaults and the step's `with:`
// map into the `inputs` expression context. With-keys are matched
// case-insensitively; undeclared keys still land in the context, and the
// returned list names them for a warning.
func resolveInputs(meta *actions.Meta, with map[string]any) (map[string]any, []string, error) {
	inputs := map[string]any{}
	undeclared := []string{}
	for name, in := range meta.Inputs {
		var v any
		found := false
		for k, w := range with {
			if strings.EqualFold(k, name) {
				v, found = w, true
			}
		}
		if !found {
			if in.Required && in.Default == "" {
				return nil, nil, fmt.Errorf("required input %q was not supplied", name)
			}
			v = in.Default
		}
		inputs[name] = v
	}
	for k, w := range with {
		declared := false
		for name := range meta.Inputs {
			if strings.EqualFold(k, name) {
				declared = true
			}
		}
		if !declared {
			undeclared = append(undeclared, k)
		}
		if _, set := inputs[k]; !set {
			inputs[k] = w
		}
	}
	sort.Strings(undeclared)
	return inputs, undeclared, nil
}

// compositeCtx builds the expression context for steps inside a composite
// action: the caller's context plus inputs and a steps map that merges outer
// step results with the composite's own.
func compositeCtx(evalCtx map[string]any, inputs map[string]any, internal map[string]*stepOutcome, status string) map[string]any {
	steps := map[string]any{}
	if outer, ok := evalCtx["steps"].(map[string]any); ok {
		for k, v := range outer {
			steps[k] = v
		}
	}
	for id, r := range internal {
		outs := map[string]any{}
		for k, v := range r.Outputs {
			outs[k] = v
		}
		steps[id] = map[string]any{"outcome": r.Outcome, "conclusion": r.Conclusion, "outputs": outs}
	}
	ctx := map[string]any{}
	for k, v := range evalCtx {
		ctx[k] = v
	}
	ctx["inputs"] = inputs
	ctx["steps"] = steps
	ctx["__job_status"] = status
	return ctx
}

// compositeShell maps GitHub's named shells to container interpreters. Named
// shells we cannot honour (pwsh, python, cmd, powershell) fail loudly; absolute
// paths pass through and fail naturally when the image lacks them.
func compositeShell(name, jobShell string) (string, error) {
	switch strings.ToLower(name) {
	case "sh":
		return "/bin/sh", nil
	case "bash":
		return jobShell, nil // bash when the image has it, sh otherwise — same fallback as run steps
	case "pwsh", "python", "cmd", "powershell":
		return "", fmt.Errorf("shell %q is not supported inside composite actions", name)
	}
	if strings.HasPrefix(name, "/") {
		return name, nil
	}
	return "", fmt.Errorf("shell %q is not supported inside composite actions", name)
}

// runComposite executes a composite action's steps inline. Sub-step logs and
// outputs fold into the parent step: log chunks post under the parent's step
// number and declared outputs land in GITHUB_OUTPUT, which the caller reads.
func (e *executor) runComposite(ctx context.Context, step StepSpec, meta *actions.Meta, actionPath string, evalCtx map[string]any, depth int) (string, int) {
	if depth > maxCompositeDepth {
		e.logStep(step, "KERN: composite actions nested deeper than "+itoa(maxCompositeDepth))
		return "failure", 1
	}
	if step.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(step.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	inputs, undeclared, err := resolveInputs(meta, step.With)
	if err != nil {
		e.logStep(step, "KERN: "+err.Error())
		return "failure", 1
	}
	for _, name := range undeclared {
		e.logStep(step, "KERN: warning: unexpected input '"+name+"'")
	}
	if name := meta.Name; name != "" {
		e.logStep(step, "KERN: running composite action "+name)
	}

	internal := map[string]*stepOutcome{}
	status := e.jobStatusSoFar()
	code := 0
	for i, cs := range meta.Runs.Steps {
		if e.cancelled.Load() {
			return "cancelled", 1
		}
		subid := cs.ID
		if subid == "" {
			subid = "__composite_" + strconv.Itoa(i)
		}
		cctx := compositeCtx(evalCtx, inputs, internal, status)
		label := cs.Name
		if label == "" {
			label = cs.Uses
		}
		if label == "" {
			label = strings.TrimSpace(strings.SplitN(strings.TrimSpace(cs.Run), "\n", 2)[0])
		}
		if len(label) > 80 {
			label = label[:77] + "..."
		}
		ok, cerr := expressions.EvaluateCondition(cs.If, cctx)
		if cerr != nil {
			e.logStep(step, "KERN: composite step condition: "+cerr.Error())
			internal[subid] = &stepOutcome{Conclusion: "failure", Outcome: "failure"}
			status, code = "failure", 1
			break
		}
		if !ok {
			internal[subid] = &stepOutcome{Conclusion: "skipped", Outcome: "skipped"}
			continue
		}
		e.logStep(step, "KERN: ▸ "+label)

		var outcome string
		subEnv, envErr := e.compositeStepEnv(step, cs, actionPath, cctx)
		if envErr != nil {
			e.logStep(step, "KERN: composite step env: "+envErr.Error())
			outcome, code = "failure", 1
		} else if cs.Uses != "" {
			if cs.Run != "" {
				e.logStep(step, "KERN: composite step cannot define both 'run' and 'uses'")
				outcome, code = "failure", 1
			} else {
				sub := StepSpec{Number: step.Number, ID: cs.ID, Name: label, Uses: cs.Uses, With: cs.With, Env: subEnv}
				outcome, code = e.runAction(ctx, sub, cctx, depth+1)
			}
		} else {
			switch {
			case cs.Run == "":
				e.logStep(step, "KERN: composite step must define 'run' or 'uses'")
				outcome, code = "failure", 1
			case cs.Shell == "":
				e.logStep(step, "KERN: composite run steps must declare 'shell' (GitHub requires it)")
				outcome, code = "failure", 1
			default:
				shell, shErr := compositeShell(cs.Shell, e.shell)
				if shErr != nil {
					e.logStep(step, "KERN: "+shErr.Error())
					outcome, code = "failure", 1
					break
				}
				wd, wdErr := expressions.Interpolate(cs.WorkingDirectory, cctx)
				if wdErr != nil {
					e.logStep(step, "KERN: composite step working-directory: "+wdErr.Error())
					outcome, code = "failure", 1
					break
				}
				sub := StepSpec{
					Number: step.Number, ID: cs.ID, Name: label, Run: cs.Run, Shell: shell,
					WorkingDirectory: wd, Env: subEnv,
				}
				var bodyErr error
				e.exprCtx = cctx // run bodies interpolate against inputs/steps, not the job ctx
				outcome, code, bodyErr = e.runStepBody(ctx, sub)
				e.exprCtx = nil
				if bodyErr != nil {
					e.logStep(step, "KERN: "+bodyErr.Error())
				}
			}
		}
		// File-command drains: sub-step outputs belong to the sub-step id;
		// summaries accumulate for the parent step to upload once.
		outs, _ := e.drainFile("GITHUB_OUTPUT")
		e.sumBuf.Write(e.drainSummary())
		internal[subid] = &stepOutcome{Conclusion: outcome, Outcome: outcome, Outputs: outs}
		if outcome == "cancelled" {
			return "cancelled", code
		}
		if outcome == "failure" {
			status = "failure" // code already holds the failing sub-step's exit code
		}
	}
	if status == "failure" {
		return "failure", code
	}
	// Declared outputs evaluate against the composite's internal steps and are
	// appended to GITHUB_OUTPUT — the caller attributes them to this step's id.
	for name, out := range meta.Outputs {
		if strings.TrimSpace(out.Value) == "" {
			e.logStep(step, "KERN: warning: output '"+name+"' has no value mapping")
			continue
		}
		v, err := expressions.Interpolate(out.Value, compositeCtx(evalCtx, inputs, internal, status))
		if err != nil {
			e.logStep(step, "KERN: warning: output '"+name+"': "+err.Error())
			continue
		}
		e.setStepOutput(name, v)
	}
	return "success", code
}

// compositeStepEnv merges the parent step's env with the composite step's own
// (values interpolated) and stamps GITHUB_ACTION_PATH.
func (e *executor) compositeStepEnv(step StepSpec, cs actions.CompositeStep, actionPath string, evalCtx map[string]any) (map[string]string, error) {
	env := map[string]string{}
	for k, v := range step.Env {
		env[k] = v
	}
	for k, v := range cs.Env {
		s, err := expressions.Interpolate(envString(v), evalCtx)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", k, err)
		}
		env[k] = s
	}
	env["GITHUB_ACTION_PATH"] = actionPath
	return env, nil
}

func envString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

// drainFile reads and removes a file-command target in the cmd dir.
func (e *executor) drainFile(name string) (map[string]string, error) {
	p := filepath.Join(e.cmdDir(), name)
	defer os.Remove(p)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return parseKeyValue(string(data))
}

func (e *executor) drainSummary() []byte {
	p := filepath.Join(e.cmdDir(), "STEP_SUMMARY")
	data, _ := os.ReadFile(p)
	_ = os.Remove(p)
	return data
}
