package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/github"
	"github.com/dvorinka/kern/internal/store"
)

// getPublicRun serves a reduced run view with no session. The org setting
// public_ci must be "on"; otherwise the response is the same 404 as a missing
// run. Logs, env, scripts, and step specs stay on the authenticated route.
func (s *Server) getPublicRun(c *gin.Context) {
	id, ok := s.runIDParam(c)
	if !ok {
		return
	}
	run, err := s.Store.Run(c.Request.Context(), id)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if !s.publicCIEnabled(c.Request.Context(), run.ProjectID) {
		fail(c, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	jobs, err := s.Store.JobsForRun(c.Request.Context(), run.ID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	med, _ := s.Store.StepMedians(c.Request.Context(), run.WorkflowID)
	view, pjobs := publicView(run, jobs, med)
	c.JSON(http.StatusOK, gin.H{"run": view, "jobs": pjobs})
}

func (s *Server) publicCIEnabled(ctx context.Context, projectID uuid.UUID) bool {
	orgID, err := s.Store.ProjectOrg(ctx, projectID)
	if err != nil {
		return false
	}
	raw, err := s.Store.OrgSettings(ctx, orgID)
	if err != nil {
		return false
	}
	return settingsValue(raw, "public_ci", "off") == "on"
}

// runPageURL is the page GitHub should open for this run. Public links are
// used only while the org toggle is on.
func (s *Server) runPageURL(ctx context.Context, run store.Run) string {
	return s.Cfg.PublicBaseURL + runPagePath(s.publicCIEnabled(ctx, run.ProjectID), run.ID.String())
}

func runPagePath(public bool, id string) string {
	if public {
		return "/ci/" + id
	}
	return "/runs/" + id
}

type publicRun struct {
	ID          string     `json:"id"`
	Number      int        `json:"run_number"`
	Workflow    string     `json:"workflow_name"`
	Event       string     `json:"event"`
	Ref         string     `json:"ref"`
	SHA         string     `json:"sha"`
	Commit      string     `json:"commit_message"`
	Actor       string     `json:"actor,omitempty"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	Project     string     `json:"project_slug"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type publicJob struct {
	Name        string       `json:"name"`
	Status      string       `json:"status"`
	Conclusion  string       `json:"conclusion"`
	StartedAt   *time.Time   `json:"started_at"`
	CompletedAt *time.Time   `json:"completed_at"`
	Steps       []publicStep `json:"steps"`
	// Live progress for agents watching the public run endpoint (#331):
	// which step is running, how long the job sat in queue, and a median-
	// history ETA for the rest of the lane (#335).
	CurrentStep      string `json:"current_step,omitempty"`
	StepNumber       int    `json:"step_number,omitempty"`
	StepCount        int    `json:"step_count,omitempty"`
	QueueWaitSeconds int64  `json:"queue_wait_seconds,omitempty"`
	ETASeconds       int64  `json:"eta_seconds,omitempty"`
}

type publicStep struct {
	Number      int        `json:"number"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	// Diagnosis exposes the KERN-authored parts of the step's failure
	// diagnosis — kind, summary and suggestion. The detail field carries log
	// content and stays private.
	Diagnosis *publicDiagnosis `json:"diagnosis,omitempty"`
}

type publicDiagnosis struct {
	Kind       string `json:"kind"`
	Summary    string `json:"summary"`
	Suggestion string `json:"suggestion"`
}

func publicView(run store.Run, jobs []store.Job, med map[string]map[string]float64) (publicRun, []publicJob) {
	msg := run.CommitMessage
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	actor := run.Actor
	if strings.Contains(actor, "@") {
		actor = ""
	}
	view := publicRun{
		ID: run.ID.String(), Number: run.RunNumber, Workflow: run.WorkflowName,
		Event: run.Event, Ref: run.Ref, SHA: run.SHA, Commit: msg, Actor: actor,
		Status: run.Status, Conclusion: run.Conclusion, Project: run.ProjectSlug,
		StartedAt: run.StartedAt, CompletedAt: run.CompletedAt,
	}
	out := make([]publicJob, 0, len(jobs))
	for _, j := range jobs {
		pj := publicJob{
			Name: j.Name, Status: j.Status, Conclusion: j.Conclusion,
			StartedAt: j.StartedAt, CompletedAt: j.CompletedAt,
			Steps: make([]publicStep, 0, len(j.Steps)),
		}
		for _, st := range j.Steps {
			pj.Steps = append(pj.Steps, publicStep{
				Number: st.Number, Name: st.Name, Status: st.Status, Conclusion: st.Conclusion,
				StartedAt: st.StartedAt, CompletedAt: st.CompletedAt,
				Diagnosis: publicDiagnosisOf(st.Diagnosis),
			})
		}
		p := jobProgress(j, med[j.JobKey], time.Now())
		pj.CurrentStep = p.current
		pj.StepNumber = p.num
		pj.StepCount = len(j.Steps)
		pj.QueueWaitSeconds = p.queueWait
		if p.eta >= 0 {
			pj.ETASeconds = p.eta
		}
		out = append(out, pj)
	}
	return view, out
}

// progress is one job's live position, shared by the check-run narrative
// and the public run JSON (#331).
type progress struct {
	current   string // name of the running step ("" when none)
	num       int    // 1-based index of the current step
	done      int    // steps already finished
	queueWait int64  // seconds from queue to claim (or to now, still queued)
	eta       int64  // seconds remaining from median history; -1 = unknown
}

// jobProgress derives a job's live position from its stored steps. The ETA
// sums each unfinished step's median duration — the running step's median
// minus its elapsed time — from the workflow's recent history (#335). A
// step with no history falls back to the median of the job's known
// medians; a job with no history at all reports eta -1.
func jobProgress(j store.Job, med map[string]float64, now time.Time) progress {
	var p progress
	p.eta = -1
	if j.StartedAt != nil {
		p.queueWait = int64(j.StartedAt.Sub(j.QueuedAt).Seconds())
	} else if j.Status == "queued" || j.Status == "waiting" {
		p.queueWait = int64(now.Sub(j.QueuedAt).Seconds())
	}
	if len(j.Steps) == 0 {
		return p
	}
	// Fallback per-step median = median of this job's known medians, so a
	// newly added step still prices into the estimate.
	var vals []float64
	for _, st := range j.Steps {
		if m, ok := med[st.Name]; ok {
			vals = append(vals, m)
		}
	}
	fallback := float64(-1)
	if len(vals) > 0 {
		sort.Float64s(vals)
		fallback = vals[len(vals)/2]
	}
	for i, st := range j.Steps {
		switch st.Status {
		case "completed", "skipped", "cancelled":
			p.done++
			continue
		}
		m, ok := med[st.Name]
		if !ok {
			m = fallback
		}
		if st.Status == "in_progress" {
			if p.current == "" {
				p.current = st.Name
				p.num = i + 1
			}
			if st.StartedAt != nil && m >= 0 {
				rem := m - now.Sub(*st.StartedAt).Seconds()
				if rem < 0 {
					rem = 0
				}
				if p.eta < 0 {
					p.eta = 0
				}
				p.eta += int64(rem)
			}
			continue
		}
		// queued/pending step — full median ahead of it.
		if m >= 0 {
			if p.eta < 0 {
				p.eta = 0
			}
			p.eta += int64(m)
		}
	}
	if p.current == "" {
		for i, st := range j.Steps {
			if st.Status == "queued" || st.Status == "pending" {
				p.current = st.Name + " (pending)"
				p.num = i + 1
				break
			}
		}
	}
	return p
}

// fmtSeconds renders a duration for check text: "45s", "3m", "1h12m".
func fmtSeconds(n int64) string {
	d := time.Duration(n) * time.Second
	if d < time.Minute {
		return fmt.Sprintf("%ds", n)
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int64(d.Minutes()), n%60)
	}
	return fmt.Sprintf("%dh%dm", int64(d.Hours()), int64(d.Minutes())%60)
}

// publicDiagnosisOf trims a stored step diagnosis to the fields safe for an
// unauthenticated page.
func publicDiagnosisOf(raw json.RawMessage) *publicDiagnosis {
	if len(raw) == 0 {
		return nil
	}
	var d struct {
		Kind       string `json:"kind"`
		Summary    string `json:"summary"`
		Suggestion string `json:"suggestion"`
	}
	if json.Unmarshal(raw, &d) != nil || d.Kind == "" {
		return nil
	}
	return &publicDiagnosis{Kind: d.Kind, Summary: d.Summary, Suggestion: d.Suggestion}
}

// postJobCheck upserts one check run for a single workflow job — the per-job
// granularity GitHub renders on a pull request (#180). The check name mirrors
// GitHub's "workflow / job" shape; ExternalID scopes it to run+job so a rerun
// never rewrites a sibling run's job check. Failures are logged, never
// propagated — a status post must not undo the state transition that
// triggered it.
func (s *Server) postJobCheck(ctx context.Context, run store.Run, job store.Job, state string) {
	var src map[string]any
	_ = json.Unmarshal(run.Source, &src)
	repo, _ := src["repo_full_name"].(string)
	if repo == "" || run.SHA == "" {
		return
	}
	cl, err := s.githubClient(ctx, repo)
	if err != nil {
		s.Log.Warn("github client", zap.String("repo", repo), zap.Error(err))
		return
	}
	status, conclusion := jobCheck(state, job)
	name := job.Name
	if name == "" {
		name = job.Key
	}
	var text string
	if status == "completed" && (conclusion == "failure" || conclusion == "timed_out") {
		text = s.checkLogExcerpt(ctx, []store.Job{job})
	}
	summary, title := jobCheckNarrative(ctx, s, run, job, name, state, conclusion)
	if err := cl.UpsertCheckRun(ctx, repo, github.CheckRun{
		Name: "kern/" + run.WorkflowName + " / " + name, HeadSHA: run.SHA,
		Status: status, Conclusion: conclusion,
		DetailsURL: s.runPageURL(ctx, run),
		ExternalID: run.ID.String() + "/" + job.Key,
		Title:      title, Summary: summary, Text: text,
		StartedAt: job.StartedAt, CompletedAt: job.CompletedAt,
	}); err != nil {
		s.Log.Warn("job check run failed",
			zap.String("repo", repo), zap.String("job", job.Key), zap.Error(err))
	}
}

// jobCheckNarrative renders the live summary/title a job check carries
// between claim and completion (#331): queue wait while queued, then the
// running step plus a median-history ETA. Terminal states keep the old
// one-line summary — the log excerpt in text does the talking there.
func jobCheckNarrative(ctx context.Context, s *Server, run store.Run,
	job store.Job, name, state, conclusion string) (summary, title string) {
	title = name + ": " + jobCheckTitle(state, conclusion)
	summary = "KERN job " + name + "."
	if state == "completed" || s.Store == nil {
		return summary, title
	}
	if len(job.Steps) == 0 {
		if steps, err := s.Store.StepsForJob(ctx, job.ID); err == nil {
			job.Steps = steps
		}
	}
	var med map[string]float64
	if m, err := s.Store.StepMedians(ctx, run.WorkflowID); err == nil {
		med = m[job.JobKey]
	}
	p := jobProgress(job, med, time.Now())
	switch status, _ := jobCheck(state, job); status {
	case "queued":
		summary = fmt.Sprintf("KERN job %s — queued %s.", name, fmtSeconds(p.queueWait))
	case "in_progress":
		if p.current != "" {
			title = fmt.Sprintf("%s: step %d/%d", name, p.num, len(job.Steps))
			summary = fmt.Sprintf("KERN job %s — step %d/%d: %s (%d done).",
				name, p.num, len(job.Steps), p.current, p.done)
		} else {
			summary = fmt.Sprintf("KERN job %s — preparing.", name)
		}
		if p.eta >= 0 {
			summary += fmt.Sprintf(" ~%s left.", fmtSeconds(p.eta))
		}
	}
	return summary, title
}

// checkTextCap keeps output.text well under GitHub's 64 KiB output limit.
const checkTextCap = 8000

// checkLogExcerpt builds the check-run output.text for a failed check: each
// failed step's name, exit code and diagnosis, plus the tail of its stored
// log (masked at upload time), so Checks API consumers can diagnose a lane
// without opening the dashboard (#324). At most three steps are quoted.
func (s *Server) checkLogExcerpt(ctx context.Context, jobs []store.Job) string {
	var b strings.Builder
	shown := 0
	for _, j := range jobs {
		steps := j.Steps
		if len(steps) == 0 && s.Store != nil {
			if st, err := s.Store.StepsForJob(ctx, j.ID); err == nil {
				steps = st
			}
		}
		name := j.Name
		if name == "" {
			name = j.Key
		}
		for _, st := range steps {
			if st.Conclusion != "failure" && st.Conclusion != "timed_out" {
				continue
			}
			if shown >= 3 || b.Len() > checkTextCap {
				return b.String()
			}
			shown++
			fmt.Fprintf(&b, "### %s — %s\n\n", name, st.Name)
			if st.ExitCode != nil {
				fmt.Fprintf(&b, "exit code %d\n\n", *st.ExitCode)
			}
			if line := stepDiagnosisLine(st); line != "" {
				b.WriteString(line + "\n\n")
			}
			if tail := s.logTail(st.LogRef, 4096); tail != "" {
				fmt.Fprintf(&b, "```\n%s\n```\n\n", tail)
			}
		}
	}
	return b.String()
}

// stepDiagnosisLine renders a stored step diagnosis as a one-line finding.
func stepDiagnosisLine(st store.Step) string {
	if len(st.Diagnosis) == 0 {
		return ""
	}
	var d struct {
		Kind       string `json:"kind"`
		Summary    string `json:"summary"`
		Suggestion string `json:"suggestion"`
	}
	if json.Unmarshal(st.Diagnosis, &d) != nil || d.Kind == "" {
		return ""
	}
	line := "**" + d.Kind + "** — " + d.Summary
	if d.Suggestion != "" {
		line += "\n" + d.Suggestion
	}
	return line
}

// logTail returns the last max bytes of a stored log, starting on a line
// boundary so the excerpt does not open mid-line.
func (s *Server) logTail(ref string, max int64) string {
	if ref == "" || s.Blobs == nil {
		return ""
	}
	size, err := s.Blobs.Size(ref)
	if err != nil || size == 0 {
		return ""
	}
	off := int64(0)
	if size > max {
		off = size - max
	}
	data, _, err := s.Blobs.ReadFrom(ref, off, int(max))
	if err != nil {
		return ""
	}
	text := string(data)
	if off > 0 {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	return text
}

// jobCheck maps a stored job state onto Checks API status/conclusion.
// Terminal states carry the stored conclusion; a completed job with no
// conclusion is a store anomaly and reports neutral rather than failure.
func jobCheck(state string, job store.Job) (status, conclusion string) {
	switch state {
	case "queued", "waiting", "waiting_approval":
		return "queued", ""
	case "in_progress":
		return "in_progress", ""
	case "skipped":
		return "completed", "skipped"
	case "cancelled":
		return "completed", "cancelled"
	default: // completed
		switch job.Conclusion {
		case "success", "failure", "cancelled", "skipped", "timed_out", "neutral", "action_required":
			return "completed", job.Conclusion
		case "":
			return "completed", "neutral"
		default:
			return "completed", "failure"
		}
	}
}

// jobCheckTitle is the one-word outcome GitHub shows in the check row.
func jobCheckTitle(state, conclusion string) string {
	if conclusion != "" {
		return conclusion
	}
	return state
}

// githubCheck maps a commit-status state onto the Checks API.
func githubCheck(state string, run store.Run) (status, conclusion string) {
	if state == "pending" {
		return "in_progress", ""
	}
	switch run.Conclusion {
	case "success", "failure", "cancelled", "skipped", "timed_out", "neutral", "action_required":
		return "completed", run.Conclusion
	case "":
		if state == "success" {
			return "completed", "success"
		}
		return "completed", "failure"
	default:
		return "completed", "failure"
	}
}

func statusDescription(run store.Run, state string, jobs []store.Job) string {
	if len(jobs) == 0 {
		return "KERN " + run.WorkflowName + ": " + state
	}
	failed := 0
	for _, j := range jobs {
		if j.Conclusion == "failure" {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Sprintf("KERN %s: %s (%d/%d jobs failed)", run.WorkflowName, state, failed, len(jobs))
	}
	return fmt.Sprintf("KERN %s: %s (%d jobs)", run.WorkflowName, state, len(jobs))
}

func checkTitle(run store.Run, state string) string {
	label := state
	if run.Conclusion != "" {
		label = run.Conclusion
	}
	return fmt.Sprintf("%s #%d %s", run.WorkflowName, run.RunNumber, label)
}

func checkSummary(run store.Run, jobs []store.Job) string {
	var b strings.Builder
	sha := run.SHA
	if len(sha) > 7 {
		sha = sha[:7]
	}
	fmt.Fprintf(&b, "%s #%d · %s · `%s`\n\n", run.WorkflowName, run.RunNumber, run.Event, sha)
	if len(jobs) == 0 {
		b.WriteString("No jobs yet.\n")
		return b.String()
	}
	b.WriteString("| Job | Status |\n| --- | --- |\n")
	for _, j := range jobs {
		st := j.Conclusion
		if st == "" {
			st = j.Status
		}
		fmt.Fprintf(&b, "| %s | %s |\n", mdCell(j.Name), mdCell(st))
		for _, step := range j.Steps {
			sst := step.Conclusion
			if sst == "" {
				sst = step.Status
			}
			fmt.Fprintf(&b, "| %s | %s |\n", mdCell("↳ "+step.Name), mdCell(sst))
		}
	}
	s := b.String()
	diag := firstFailureDiagnosis(jobs)
	if diag != "" {
		// Reserve room for the diagnosis: truncate the table, never the
		// finding itself.
		if len(s)+len(diag) > 6000 {
			s = s[:6000-len(diag)]
		}
		s += diag
	}
	if len(s) > 6000 {
		return s[:6000]
	}
	return s
}

// firstFailureDiagnosis renders the first failing step's diagnosis for the
// check-run summary — kind, summary and suggestion only. The detail field
// carries log content and stays private, matching publicDiagnosisOf.
func firstFailureDiagnosis(jobs []store.Job) string {
	for _, j := range jobs {
		for _, st := range j.Steps {
			if st.Conclusion != "failure" || len(st.Diagnosis) == 0 {
				continue
			}
			var d struct {
				Kind       string `json:"kind"`
				Summary    string `json:"summary"`
				Suggestion string `json:"suggestion"`
			}
			if json.Unmarshal(st.Diagnosis, &d) != nil || d.Kind == "" {
				continue
			}
			var b strings.Builder
			fmt.Fprintf(&b, "\n**Failure — %s / %s:** `%s` — %s",
				j.JobKey, st.Name, d.Kind, mdCell(d.Summary))
			if d.Suggestion != "" {
				fmt.Fprintf(&b, "\n%s", mdCell(d.Suggestion))
			}
			return b.String()
		}
	}
	return ""
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "/")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}
