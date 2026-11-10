package runner

import (
	"os"
	"path/filepath"

	"go.uber.org/zap"
)

// gradleMirrorInit is seeded into <cache>/gradle/init.d/ by
// ensureGradleMirror and activates through GRADLE_USER_HOME (jobEnv). It
// inserts the fleet Maven Central mirror FIRST in every repository list —
// plugin management, settings-level dependency resolution, legacy project
// and buildscript blocks — so repo.maven.apache.org rate limits degrade to
// a fallback instead of failing the resolve. Declared repositories are
// never removed: artifacts the mirror lacks still resolve from their
// original source. Projects on RepositoriesMode.FAIL_ON_PROJECT_REPOS get
// the mirror only through dependencyResolutionManagement — project-level
// additions are skipped so their declared mode keeps working.
const gradleMirrorInit = `// KERN-managed: fleet Maven Central mirror. Do not edit — the runner
// rewrites this file on every job.
// Inserted FIRST everywhere Gradle resolves artifacts so repo.maven.apache.org
// becomes the fallback, not the default. Nothing is removed.
// KERN_MAVEN_MIRROR overrides the URL (e.g. a LAN pull-through cache).
def mirrorUrl = System.getenv('KERN_MAVEN_MIRROR') ?: 'https://maven-central.storage-download.googleapis.com/maven2/'
def addMirror
addMirror = { repos ->
    if (repos.findByName('kern-central-mirror') == null) {
        repos.maven { url mirrorUrl; name 'kern-central-mirror' }
        def repo = repos.findByName('kern-central-mirror')
        repos.remove(repo)
        repos.add(0, repo)
    }
}
gradle.settingsEvaluated { settings ->
    try { addMirror(settings.pluginManagement.repositories) } catch (Throwable ignored) {}
    try { addMirror(settings.dependencyResolutionManagement.repositories) } catch (Throwable ignored) {}
    def mode = ''
    try { mode = settings.dependencyResolutionManagement.repositoriesMode.get().name() } catch (Throwable ignored) {}
    if (mode != 'FAIL_ON_PROJECT_REPOS') {
        settings.gradle.projectsLoaded {
            settings.gradle.rootProject { root ->
                root.allprojects { p ->
                    addMirror(p.buildscript.repositories)
                    addMirror(p.repositories)
                }
            }
        }
    }
}
`

// ensureGradleMirror seeds <cache>/gradle/init.d/ with the fleet mirror
// script. GRADLE_USER_HOME (jobEnv) points every job at the same home, so
// the mirror — and the dependency/build cache — works on any job image
// without a rebuild. Best effort: a write failure degrades to the job's
// own repositories, never a hard fail.
func (e *executor) ensureGradleMirror() {
	root := filepath.Join(cacheDir(e.cfg.WorkRoot), "gradle")
	dir := filepath.Join(root, "init.d")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		e.log.Warn("gradle mirror: mkdir failed", zap.Error(err))
		return
	}
	// Job containers run uid 1000 and write Gradle state next to init.d.
	_ = os.Chmod(root, 0o777)
	_ = os.Chmod(dir, 0o777)
	p := filepath.Join(dir, "kern-mirror.init.gradle")
	if cur, err := os.ReadFile(p); err == nil && string(cur) == gradleMirrorInit {
		return
	}
	if err := os.WriteFile(p, []byte(gradleMirrorInit), 0o644); err != nil {
		e.log.Warn("gradle mirror: write failed", zap.Error(err))
	}
}
