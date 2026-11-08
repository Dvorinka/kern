// API types mirroring the control-plane JSON contract (internal/store, internal/api).
// ponytail: generated OpenAPI client lands when a spec exists; keep this aligned by hand.

export interface User {
	id: string;
	email: string;
	name: string;
	org_id: string;
	role: string;
	created_at: string;
}

export interface Project {
	id: string;
	org_id: string;
	slug: string;
	name: string;
	provider: string;
	repo_full_name: string;
	repo_url: string;
	default_branch: string;
	local_path: string;
	created_at: string;
	updated_at: string;
}

export interface Workflow {
	id: string;
	project_id: string;
	path: string;
	name: string;
	created_at: string;
	updated_at: string;
}

export type RunStatus = "awaiting_approval" | "queued" | "in_progress" | "completed" | "cancelled";
export type Conclusion = "" | "success" | "failure" | "cancelled" | "skipped";

export interface Run {
	id: string;
	project_id: string;
	workflow_id: string;
	workflow_version_id: string;
	run_number: number;
	event: string;
	ref: string;
	sha: string;
	commit_message: string;
	actor: string;
	status: RunStatus;
	conclusion: Conclusion;
	trust_level: string;
	concurrency_group: string;
	started_at: string | null;
	completed_at: string | null;
	created_at: string;
	workflow_name: string;
	workflow_path: string;
	project_slug: string;
	project_name: string;
}

export interface Step {
	id: string;
	job_id: string;
	number: number;
	key: string;
	name: string;
	status: "queued" | "running" | "completed" | "skipped" | "cancelled";
	conclusion: Conclusion;
	exit_code: number | null;
	started_at: string | null;
	completed_at: string | null;
	spec?: StepSpec;
	log_bytes: number;
	has_summary?: boolean;
	annotations?: Annotation[];
	diagnosis?: {
		kind: string;
		summary: string;
		suggestion: string;
		agent_prompt: string;
		fixable: boolean;
		remediation?: { fix: string[]; applied?: boolean; approved?: boolean; dismissed?: boolean; result?: string };
	} | null;
}

export interface Annotation {
	level: "error" | "warning" | "notice" | "debug" | string;
	message: string;
	title?: string;
	file?: string;
	line?: number;
	col?: number;
	end_line?: number;
	end_col?: number;
}

export interface StepSpec {
	number?: number;
	id?: string;
	name?: string;
	run?: string;
	uses?: string;
	shell?: string;
	working_directory?: string;
	if?: string;
	env?: Record<string, string>;
	continue_on_error?: boolean;
	timeout_seconds?: number;
}

export interface Job {
	id: string;
	run_id: string;
	job_key: string;
	key: string;
	name: string;
	status:
		| "queued"
		| "waiting"
		| "waiting_approval"
		| "in_progress"
		| "completed"
		| "cancelled"
		| "skipped";
	conclusion: Conclusion;
	needs: string[];
	runs_on: string[];
	image: string;
	matrix: Record<string, unknown> | null;
	env: Record<string, string> | null;
	timeout_seconds: number;
	continue_on_error: boolean;
	attempt: number;
	runner_id: string | null;
	queued_at: string;
	started_at: string | null;
	completed_at: string | null;
	error: string;
	outputs: Record<string, string> | null;
	environment: string;
	environment_url: string;
	steps?: Step[];
}

export interface Approval {
	user_id: string;
	email: string;
	at: string;
}

export interface Environment {
	id: string;
	project_id: string;
	name: string;
	protected: boolean;
	required_approvals: number;
	reviewers: string[];
	reviewer_emails: string[];
	wait_minutes: number;
	branch_policy: "all" | "selected";
	branch_patterns: string[];
	created_at: string;
}

export interface Deployment {
	id: string;
	project_id: string;
	environment_id: string;
	environment_name: string;
	run_id: string;
	job_id: string;
	ref: string;
	sha: string;
	status: "queued" | "in_progress" | "success" | "failure" | "rolled_back";
	url: string;
	approvals: Approval[];
	required_approvals: number;
	reviewer_emails: string[];
	wait_until: string | null;
	run_number: number;
	project_slug: string;
	job_name: string;
	created_at: string;
	completed_at: string | null;
}

export interface RunDetail {
	run: Run;
	jobs: Job[];
	plan: unknown;
	deployments: Deployment[];
	workflow: { id: string; path: string; name: string; commit_sha: string };
}

export interface Runner {
	id: string;
	name: string;
	hostname: string;
	os: string;
	arch: string;
	cpus: number;
	memory_bytes: number;
	disk_bytes: number;
	labels: string[];
	version: string;
	status: "online" | "offline" | "draining";
	draining: boolean;
	last_heartbeat_at: string | null;
	cpu_percent: number | null;
	mem_used_bytes: number | null;
	disk_free_bytes: number | null;
	metrics_at: string | null;
	geo_lat: number | null;
	geo_lng: number | null;
	geo_city: string;
	geo_country: string;
	geo_source: "" | "auto" | "manual";
	geo_ip: string;
	geo_updated_at: string | null;
	icon: string;
	created_at: string;
	active_jobs?: number;
}

export interface FleetMapPin {
	id: string;
	name: string;
	status: string;
	lat: number | null;
	lng: number | null;
	city: string;
	country: string;
	geo_source: "" | "auto" | "manual";
	jobs: { runner_id: string; key: string; name: string }[];
}

export interface FleetMapResponse {
	control: { lat: number; lng: number; city: string; country: string; source: string } | null;
	runners: FleetMapPin[];
}

export interface Overview {
	runs_total: number;
	runs_success: number;
	runs_failure: number;
	runs_active: number;
	jobs_queued: number;
	jobs_running: number;
	runners_online: number;
	runners_total: number;
	avg_queue_seconds: number;
	avg_run_seconds: number;
	p95_queue_seconds: number;
	last_run_at: string | null;
}

export interface OverviewResponse {
	overview: Overview;
	series: { time: string; value: number }[];
	failures: Run[];
	cache: { entries: number; bytes: number };
	webhooks?: { failed_24h: number; last_error: string };
}

export interface SecretSummary {
	id: string;
	name: string;
	project_id: string | null;
	environment_id: string | null;
	created_at: string;
	updated_at: string;
}

export interface Artifact {
	id: string;
	run_id: string;
	job_id: string | null;
	name: string;
	size_bytes: number;
	sha256: string;
	created_at: string;
	expires_at: string | null;
	run_number: number;
	project_slug: string;
}

export interface CacheEntry {
	id: string;
	project_id: string;
	key: string;
	version: string;
	scope: "trusted" | "pull_request";
	size_bytes: number;
	created_at: string;
	last_used_at: string;
	expires_at: string | null;
}

export interface APIToken {
	id: string;
	name: string;
	prefix: string;
	scopes: string[];
	last_used_at: string | null;
	expires_at: string | null;
	created_at: string;
}

export interface WebhookDelivery {
	id: string;
	provider: string;
	delivery_id: string;
	event: string;
	payload?: Record<string, unknown>;
	status: "received" | "processed" | "ignored" | "failed";
	error: string;
	received_at: string;
	processed_at: string | null;
}

export interface UsageReport {
	totals: {
		runner_seconds: number;
		billable_minutes: number;
		jobs: number;
		runs: number;
		runs_success: number;
		queue_median_seconds: number;
		job_median_seconds: number;
	};
	days: {
		date: string;
		seconds: number;
		billable_minutes: number;
		jobs: number;
		failed_jobs: number;
	}[];
	projects: { slug: string; name: string; seconds: number; runs: number }[];
	runners: { name: string; seconds: number; jobs: number }[];
	failures: { kind: string; count: number; fixable: number }[];
}

export interface UsageResponse {
	usage: UsageReport;
	days: number;
	rate_usd_per_min: number;
	hosted_equiv_usd: number;
}

export interface Notification {
	id: string;
	kind: "run_approval" | "remediation" | "run_failed" | "runner_offline" | "webhook_failed";
	title: string;
	sub: string;
	href: string;
	job_id?: string;
	step?: number;
	fix?: string[];
	created_at: string;
}

export interface AuditEvent {
	id: string;
	action: string;
	target: string;
	actor: string;
	metadata: Record<string, unknown>;
	created_at: string;
}

export interface UpdateInfo {
	ok: boolean;
	current: string;
	latest: string;
	url: string;
	published_at?: string;
	available: boolean;
	checked_at: string;
	repo: string;
	error?: string;
	auto_update: string;
	manifest_ok: boolean;
	manifest_error?: string;
	git_sha?: string;
	runner_image?: string;
	apply_block?: string;
	updater: string;
	updater_error?: string;
	updater_at?: string;
}

export interface Member {
	id: string;
	email: string;
	name: string;
	role: string;
	created_at: string;
}
