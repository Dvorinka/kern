package workflows

// failFast resolves strategy.fail-fast, which defaults to true like GitHub Actions.
func failFast(job *Job) bool {
	if job.Strategy == nil || job.Strategy.FailFast == nil {
		return true
	}
	return *job.Strategy.FailFast
}
