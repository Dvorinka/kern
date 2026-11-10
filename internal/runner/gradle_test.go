package runner

import (
	"strings"
	"testing"
)

// Under RepositoriesMode.PREFER_PROJECT (the Gradle default) an injected
// repo in an otherwise empty project handler counts as a declaration and
// shadows every settings repository — google() included — so androidx and
// other non-Central artifacts 404. The init script must only top up
// project handlers the project populated itself, after evaluation.
// Reproduced live against Gradle 8.13: an undeclared project searched
// [mirror] instead of [mirror, settings...]; a project declaring
// mavenCentral() searched [mirror, central] after the guard was added.
func TestGradleMirrorInitDoesNotShadowProjectRepos(t *testing.T) {
	eval := strings.Index(gradleMirrorInit, "p.afterEvaluate")
	guard := strings.Index(gradleMirrorInit, "p.repositories.isEmpty()")
	inject := strings.Index(gradleMirrorInit, "addMirror(p.repositories)")
	if eval < 0 || guard < 0 || inject < 0 {
		t.Fatal("project repo injection must be deferred to afterEvaluate and gated on a non-empty handler")
	}
	if !(eval < guard && guard < inject) {
		t.Fatal("addMirror(p.repositories) must run inside afterEvaluate behind the isEmpty check")
	}
	for _, keep := range []string{
		"addMirror(settings.pluginManagement.repositories)",
		"addMirror(settings.dependencyResolutionManagement.repositories)",
		"addMirror(p.buildscript.repositories)",
	} {
		if !strings.Contains(gradleMirrorInit, keep) {
			t.Fatalf("init script lost %q", keep)
		}
	}
}
