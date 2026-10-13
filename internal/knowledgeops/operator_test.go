package knowledgeops_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ashuangiras/polaroid/internal/gitcore"
	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/knowledgeops"
	"github.com/ashuangiras/polaroid/internal/knowledgesync"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

func bareRemote(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	dir := filepath.Join(t.TempDir(), "remote.git")
	cmd := exec.CommandContext(context.Background(), bin, "init", "--quiet", "--bare", "--initial-branch=main", dir)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

// node is one independent Polaroid instance: its own Git-core repository,
// synchronizer, feedback store, agent and operator.
type node struct {
	repo     *gitcore.Repository
	syncer   *knowledgesync.Syncer
	fb       *memory.Service
	agent    *knowledgeops.Agent
	operator *knowledgeops.Operator
}

func newNode(t *testing.T, p knowledgesync.Policy) *node {
	t.Helper()
	repo, err := gitcore.Open(t.TempDir(), gitcore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	syncer, err := knowledgesync.New(repo, p)
	if err != nil {
		t.Fatal(err)
	}
	fb := newFeedbackService(t)
	return &node{
		repo: repo, syncer: syncer, fb: fb,
		agent:    knowledgeops.NewAgent(syncer.Kernel(), fb),
		operator: knowledgeops.NewOperator(syncer, p, fb),
	}
}

func (n *node) active(t *testing.T) string {
	t.Helper()
	c, err := n.syncer.Kernel().Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return string(c.Snapshot())
}

func TestOperatorStatus(t *testing.T) {
	remote := bareRemote(t)
	policy := knowledgesync.Policy{Sources: []knowledgesync.Source{{Name: "team", URL: remote, Publishers: []string{"alice"}, Publish: true}}}
	n := newNode(t, policy)

	st, err := n.operator.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Empty || st.Keys != 0 || len(st.Sources) != 1 || st.Sources[0].Name != "team" {
		t.Fatalf("status = %+v", st)
	}
}

func TestOperatorPublishWithFindingReconciles(t *testing.T) {
	n := newNode(t, knowledgesync.Policy{})
	ctx := context.Background()

	// A feedback report naming an existing, published procedure, exactly as
	// the agent-mode feedback tool records it, with its typed knowledge
	// reference. The operator then publishes a fix for that same key.
	snap := publish(t, n.syncer.Kernel(), knowledge.Change{Key: "go.module.tidy", Procedure: &knowledge.Procedure{
		Key: "go.module.tidy", Goal: "Tidy a module", Philosophy: "p", Method: "m", Contract: []byte(`{}`), Instructions: []byte(`{}`),
	}})
	f, err := n.agent.Feedback(ctx, wire.KnowledgeFeedbackRequest{
		Key: "go.module.tidy", Snapshot: string(snap), Classification: "incorrect_instruction",
		Summary: "the step is wrong", Details: "it does not handle vendoring", Reporter: "test.agent",
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := n.operator.Publish(ctx, wire.KnowledgePublishRequest{
		Base: string(snap),
		Changes: []wire.KnowledgeChange{{
			Key: "go.module.tidy", Goal: "Tidy a module", Philosophy: "p", Method: "m, now handling vendoring",
			Contract: []byte(`{}`), Instructions: []byte(`{}`),
		}},
		Author: "operator", Summary: "fix tidy", Rationale: "addresses the report", Finding: f.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Snapshot == "" {
		t.Fatalf("publish result = %+v", res)
	}
	if res.Reconciliation == nil || res.Reconciliation.Status != "resolved" {
		t.Fatalf("auto-reconciliation = %+v", res.Reconciliation)
	}

	got, err := n.fb.Feedback(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Resolution == nil || got.Resolution.Outcome != memory.OutcomeAddressed {
		t.Fatalf("feedback resolution = %+v", got.Resolution)
	}

	// Reconciling again (independently, without republishing) is idempotent.
	again, err := n.operator.Reconcile(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != "already_resolved" {
		t.Fatalf("second reconcile = %+v", again)
	}
}

func TestOperatorReconcileNoPublicationYet(t *testing.T) {
	n := newNode(t, knowledgesync.Policy{})
	ctx := context.Background()
	snap := publish(t, n.syncer.Kernel(), knowledge.Change{Key: "go.module.tidy", Procedure: &knowledge.Procedure{
		Key: "go.module.tidy", Goal: "Tidy a module", Philosophy: "p", Method: "m", Contract: []byte(`{}`), Instructions: []byte(`{}`),
	}})
	f, err := n.agent.Feedback(ctx, wire.KnowledgeFeedbackRequest{
		Key: "go.module.tidy", Snapshot: string(snap), Classification: "incorrect_instruction", Summary: "s", Details: "d", Reporter: "r",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := n.operator.Reconcile(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "no_publication_yet" {
		t.Fatalf("reconcile with nothing published = %+v", res)
	}
	if _, err := n.operator.Reconcile(ctx, "does-not-exist"); err == nil {
		t.Fatalf("reconcile of an unknown feedback id must fail")
	}
}

// Fetch, validate, activate and push between two independent instances
// through an ordinary Git remote, entirely through the operator surface.
func TestOperatorSyncLifecycle(t *testing.T) {
	remote := bareRemote(t)
	var none wire.KnowledgeCredentials
	a := newNode(t, knowledgesync.Policy{Sources: []knowledgesync.Source{{Name: "team", URL: remote, Publishers: []string{"alice"}, Publish: true}}})
	b := newNode(t, knowledgesync.Policy{Sources: []knowledgesync.Source{{Name: "team", URL: remote, Publishers: []string{"alice"}}}})
	ctx := context.Background()

	if _, err := a.syncer.Kernel().Publish(ctx, knowledge.Proposal{
		Base: knowledge.Snapshot(a.active(t)), Changes: knowledgetest.Initial(),
		Provenance: knowledge.Provenance{Author: "alice", Summary: "seed", Rationale: "initial knowledge"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.operator.Push(ctx, "team", none); err != nil {
		t.Fatal(err)
	}

	fetched, err := b.operator.Fetch(ctx, "team", none)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Status != "fetched" {
		t.Fatalf("fetch status = %+v", fetched)
	}
	validated, err := b.operator.Validate(ctx, "team")
	if err != nil {
		t.Fatal(err)
	}
	if validated.Status != "fast-forward" || len(validated.Incoming) == 0 {
		t.Fatalf("validate status = %+v", validated)
	}
	act, err := b.operator.Activate(ctx, "team", wire.KnowledgeDecision{Author: "bob", Summary: "accept", Rationale: "validated"})
	if err != nil {
		t.Fatal(err)
	}
	if act.Snapshot == "" {
		t.Fatalf("activation result = %+v", act)
	}

	st, err := b.operator.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Keys != 3 {
		t.Fatalf("status after activation = %+v", st)
	}

	// Activating twice without a new fetch fails: the candidate is gone.
	if _, err := b.operator.Activate(ctx, "team", wire.KnowledgeDecision{Author: "bob", Summary: "accept again", Rationale: "r"}); err == nil {
		t.Fatalf("activating without a fetched candidate must fail")
	}

	acts, err := b.operator.Activations(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts.Activations) != 1 || acts.Activations[0].Author != "bob" {
		t.Fatalf("activations = %+v", acts)
	}

	if _, err := b.operator.Fetch(ctx, "no-such-source", none); err == nil {
		t.Fatalf("fetch from an unknown source must fail")
	}
}
