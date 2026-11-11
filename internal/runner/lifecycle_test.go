package runner

import (
	"testing"

	"github.com/dvorinka/kern/internal/actions"
	"go.uber.org/zap"
)

func nodeMeta(pre, post string) *actions.Meta {
	m := &actions.Meta{}
	m.Runs.Using = "node20"
	m.Runs.Main = "main.js"
	m.Runs.Pre = pre
	m.Runs.Post = post
	return m
}

func dockerMeta(pre, post string) *actions.Meta {
	m := nodeMeta(pre, post)
	m.Runs.Using = "docker"
	m.Runs.PreEntrypoint = m.Runs.Pre
	m.Runs.PostEntrypoint = m.Runs.Post
	m.Runs.Pre = ""
	m.Runs.Post = ""
	return m
}

func TestPrePostScriptPerRuntime(t *testing.T) {
	n := nodeMeta("pre.js", "post.js")
	if preScript(n) != "pre.js" || postScript(n) != "post.js" {
		t.Fatalf("node scripts: got %q/%q", preScript(n), postScript(n))
	}
	d := dockerMeta("setup.sh", "cleanup.sh")
	if preScript(d) != "setup.sh" || postScript(d) != "cleanup.sh" {
		t.Fatalf("docker scripts: got %q/%q", preScript(d), postScript(d))
	}
}

func TestArmPostHookDedupAndOrder(t *testing.T) {
	e := &executor{}
	m := nodeMeta("", "post.js")
	s1 := StepSpec{Number: 2, Uses: "o/a@v1"}
	s2 := StepSpec{Number: 5, Uses: "o/b@v1"}

	h1 := e.armPostHook(s1, m, "/kern/actions/a", "/host/a")
	again := e.armPostHook(s1, m, "/kern/actions/a", "/host/a")
	h2 := e.armPostHook(s2, m, "/kern/actions/b", "/host/b")
	if h1 == nil || h2 == nil {
		t.Fatal("hooks not armed")
	}
	if again != h1 {
		t.Fatal("same step armed twice")
	}
	if len(e.postHooks) != 2 || e.postHooks[0] != h1 || e.postHooks[1] != h2 {
		t.Fatal("post stack lost registration order")
	}
}

func TestArmPostHookSkipsUndeclared(t *testing.T) {
	e := &executor{}
	if h := e.armPostHook(StepSpec{Number: 1, Uses: "o/a@v1"}, nodeMeta("pre.js", ""), "", ""); h != nil {
		t.Fatal("armed a hook for an action without post")
	}
	if len(e.postHooks) != 0 {
		t.Fatal("post stack grew for a postless action")
	}
}

func TestMergeStateAccumulates(t *testing.T) {
	h := &actionHook{state: map[string]string{}}
	mergeState(h, map[string]string{"a": "1"})
	mergeState(h, map[string]string{"b": "2", "a": "3"})
	if h.state["a"] != "3" || h.state["b"] != "2" {
		t.Fatalf("state merge wrong: %v", h.state)
	}
}

func TestMarkPreFailedInitializes(t *testing.T) {
	e := &executor{claim: &Claim{}, log: zap.NewNop()}
	e.markPreFailed(StepSpec{Number: 3}, "fetch action: boom")
	if e.preFailed[3] != "fetch action: boom" {
		t.Fatalf("preFailed not recorded: %v", e.preFailed)
	}
}
