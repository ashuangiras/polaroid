package recovery

import (
	"context"
	"encoding/json/jsontext"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// TestOversizedHistorySurvivesBackupAndRestore: records stored before the
// write limits (ADR-0037), larger than they allow, are read, verified,
// backed up and restored unchanged, while new writes of the same size are
// refused.
func TestOversizedHistorySurvivesBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(spaced(t, "catalog"), "polaroid.db")
	a := serve(t, path)
	big := jsontext.Value(`{"log":"` + strings.Repeat("L", 2*memory.MaxDocumentBytes) + `"}`)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	// Written straight to the store, as an earlier build accepted them.
	if err := a.store.CreateProcedure(ctx, memory.Procedure{ID: "old-p", CanonicalKey: "old.big", CreatedAt: at, LatestVersion: 1},
		memory.Version{ProcedureID: "old-p", Number: 1, CreatedAt: at, Definition: memory.Definition{Philosophy: "p",
			Method: strings.Repeat("M", 3*memory.MaxTextBytes), Contract: big, Instructions: jsontext.Value(`{}`), RevisionReason: "Before the limits."}}); err != nil {
		t.Fatal(err)
	}
	run := memory.Execution{ID: "old-e", CreatedAt: at, ExecutionRecord: memory.ExecutionRecord{ProcedureID: "old-p", Version: 1,
		Repository: fixtureRepo, Commit: fixtureCommit, Environment: memory.Environment{Name: "ci", Attributes: jsontext.Value(`{}`)},
		Inputs: jsontext.Value(`{}`), Outcome: memory.OutcomeSucceeded, Evidence: big}}
	if err := a.store.CreateExecution(ctx, &run); err != nil {
		t.Fatal(err)
	}
	reads := func(a *api) map[string]string {
		out := map[string]string{}
		for _, p := range []string{"/v1/procedures/old-p", "/v1/executions/old-e", "/v1/executions/old-e/verification"} {
			out[p] = string(a.get(p))
		}
		return out
	}
	want := reads(a)
	if !strings.Contains(want["/v1/executions/old-e/verification"], `"verified":true`) || len(want["/v1/executions/old-e"]) < 2*memory.MaxDocumentBytes {
		t.Fatalf("the oversized history does not read back: %.300s", want["/v1/executions/old-e/verification"])
	}
	if code, b := a.call(http.MethodPost, "/v1/executions", strings.Replace(executionBody("old-p", ""), `{"ok": true}`, string(big), 1)); code != http.StatusRequestEntityTooLarge || !strings.Contains(string(b), `"value_too_large"`) {
		t.Fatalf("a new write of the same size: %d %.200s", code, b)
	}
	a.close()

	dir := spaced(t, "backups")
	b := mustBackup(t, path, dir)
	fresh := filepath.Join(spaced(t, "restored"), "polaroid.db")
	if _, err := Restore(ctx, RestoreRequest{Backup: b.Path, Destination: fresh, BackupDir: dir}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := reads(serve(t, fresh)); got["/v1/procedures/old-p"] != want["/v1/procedures/old-p"] ||
		got["/v1/executions/old-e"] != want["/v1/executions/old-e"] || got["/v1/executions/old-e/verification"] != want["/v1/executions/old-e/verification"] {
		t.Fatal("the restored catalog reads the oversized history differently")
	}
}
