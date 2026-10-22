//go:build linux

package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pulltide/internal/application"
)

func target(t *testing.T, revision string) Target {
	t.Helper()
	b, err := os.ReadFile("../../tests/fixtures/contract/valid/exec-minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	app, err := application.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ConfigurationIdentity(app.Spec)
	if err != nil {
		t.Fatal(err)
	}
	return Target{Revision{"opaque-driver", "opaque-repository", revision}, identity}
}
func instance(t *testing.T, s *Store, workspace string) Instance {
	t.Helper()
	b, _ := os.ReadFile("../../tests/fixtures/contract/valid/exec-minimal.yaml")
	app, err := application.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.rootPath, workspace), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.rootPath, "inputs/nested"), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("immutable fixture manifest")
	if err := os.WriteFile(filepath.Join(s.rootPath, "inputs/nested/manifest"), data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return Instance{Target: target(t, "A"), Saved: app.Spec, Workspace: workspace, Inputs: []FileReference{{Path: "inputs/nested/manifest", SHA256: hex.EncodeToString(sum[:])}}}
}
func prepared(t *testing.T, s *Store) string {
	t.Helper()
	if err := s.ObserveDesired(target(t, "A")); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginPreparation(FrozenPolicy{true, 300}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}
	id, err := s.Prepared(instance(t, s, "workspaces/candidate"))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func promote(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.RecordIntent("candidate-apply-intent", id, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}
	check := 0
	if err := s.RecordIntent("candidate-verify-intent", id, &check); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitVerified(); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("promoted before whole-window proof: %v", err)
	}
	if err := s.RecordHealthWindow(true, true); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitVerified(); err != nil {
		t.Fatal(err)
	}
}
func TestIntentResultRestartAndPromotion(t *testing.T) {
	s := fixtureStore(t)
	id := prepared(t, s)
	r, err := InspectRecovery(s.state)
	if err != nil || r.Action != "abandon-prepared" || r.RequiresQuiescence {
		t.Fatalf("prepared: %+v %v", r, err)
	}
	if err := s.RecordIntent("candidate-apply-intent", id, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIntent("candidate-verify-intent", id, nil); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("advanced pending operation: %v", err)
	}
	root, owner := s.rootPath, s.owner
	s.Close()
	again, err := open(root, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	state, _ := again.Read()
	r, err = InspectRecovery(state)
	if err != nil || !r.RequiresQuiescence || r.Action != "recover-candidate" {
		t.Fatalf("restart: %+v %v", r, err)
	}
	if err := again.RecordResult("completed-success", false); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("journal substituted for driver evidence")
	}
	if err := again.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}

	check := 0
	if err := again.RecordIntent("candidate-verify-intent", id, &check); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("resumed old candidate")
	}
	if err := again.BeginRecovery(true); err != nil {
		t.Fatal(err)
	}
	if err := again.RecordIntent("cleanup-intent", id, nil); err != nil {
		t.Fatal(err)
	}

}
func TestSuppressionAndObservation(t *testing.T) {
	s := fixtureStore(t)
	id := prepared(t, s)
	if err := s.RecordIntent("candidate-apply-intent", id, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-failure", true); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginRecovery(false); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("accepted no quiescence proof")
	}
	if err := s.BeginRecovery(true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIntent("cleanup-intent", id, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitAbsent(true); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Read()
	if state.Journal != nil || state.Suppressed == nil || state.Active.State != "absent" {
		t.Fatal("suppression not atomic")
	}
	if err := s.MarkDesiredStale(); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveDesired(target(t, "A")); err != nil {
		t.Fatal(err)
	}
	state, _ = s.Read()
	if state.Suppressed == nil {
		t.Fatal("same desired target cleared suppression")
	}
	root, owner := s.rootPath, s.owner
	s.Close()
	again, err := open(root, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if err := again.ObserveDesired(target(t, "B")); err != nil {
		t.Fatal(err)
	}
	state, _ = again.Read()
	if state.Suppressed != nil {
		t.Fatal("different target did not clear episode")
	}
}
func TestObservationKeepsInstanceAndLastGood(t *testing.T) {
	s := fixtureStore(t)
	id := prepared(t, s)
	promote(t, s, id)
	root, owner := s.rootPath, s.owner
	s.Close()
	s, err := open(root, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.BeginObservation(); err != nil {
		t.Fatal(err)
	}
	check := 0
	if err := s.RecordIntent("startup-verify-intent", id, &check); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completion-unknown", false); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitObservation(false); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("cleared unknown check")
	}
	state, _ := s.Read()
	if state.Active.InstanceID != id || state.LastGoodID != id || state.Suppressed != nil {
		t.Fatal("observation rewrote deployment")
	}
	r, _ := InspectRecovery(state)
	if !r.RequiresQuiescence || r.Action != "fresh-startup-observation" {
		t.Fatal("lost observation evidence")
	}
}
func TestEveryRestartStageResult(t *testing.T) {
	s := fixtureStore(t)
	id := prepared(t, s)
	base, _ := s.Read()
	for _, stage := range []string{"artifact-intent", "prepared", "prior-stop-intent", "candidate-apply-intent", "candidate-verify-intent", "cleanup-intent", "restore-prepare-intent", "restore-apply-intent", "restore-verify-intent", "startup-verify-intent"} {
		for _, result := range []string{"", "completed-success", "completed-failure", "completion-unknown"} {
			t.Run(stage+"/"+result, func(t *testing.T) {
				st, _ := copyState(base)
				j := st.Journal
				j.Stage = stage
				j.Operation = nil
				j.Completed = nil
				j.OperandID = ""
				if stage == "artifact-intent" {
					j.Kind = "preparation"
					j.CandidateID = ""
					st.Instances = map[string]Instance{}
				}
				if stage != "prepared" {
					operand := id
					switch stage {
					case "artifact-intent":
						operand = ""
					case "prior-stop-intent":
						previous := st.Instances[id]
						previous.ID = "prior"
						previous.Workspace = "workspaces/prior"
						st.Instances["prior"] = previous
						j.PriorID = "prior"
						operand = "prior"
					case "restore-prepare-intent", "restore-apply-intent", "restore-verify-intent":
						restored := st.Instances[id]
						restored.ID = "restored"
						restored.Workspace = "workspaces/restored"
						st.Instances["restored"] = restored
						j.RestorationID = "restored"
						operand = "restored"
					case "startup-verify-intent":
						j.Kind = "observation"
						j.CandidateID = ""
						j.Target = nil
						j.Policy = nil
						h := st.Instances[id].Saved.Health
						j.SavedHealth = &h
						st.Active = Active{"known", id}
						st.LastGoodID = id
						st.Health = Health{State: "unknown"}
					}
					index := 0
					j.OperandID = operand
					j.Operation = &Operation{ID: "operation", Stage: stage, OperandID: operand, BootID: "old-boot", Result: result}
					if stage == "candidate-verify-intent" || stage == "restore-verify-intent" || stage == "startup-verify-intent" {
						j.Operation.CheckIndex = &index
					}
				}
				live := map[string]bool{}
				if j.PriorID != "" {
					live[j.PriorID] = true
				}
				if oneOf(stage, "candidate-apply-intent", "candidate-verify-intent", "cleanup-intent") {
					live[id] = true
				}
				if oneOf(stage, "restore-apply-intent", "restore-verify-intent") {
					live[j.RestorationID] = true
				}
				if oneOf(stage, "prior-stop-intent", "cleanup-intent") && result == "completed-success" {
					delete(live, operandIDForTest(j))
					j.CleanupCompleted[operandIDForTest(j)] = true
				}
				j.MayBeLive = live
				if oneOf(stage, "candidate-verify-intent", "cleanup-intent") {
					j.Completed = append(j.Completed, Operation{ID: "apply-history", Stage: "candidate-apply-intent", OperandID: id, BootID: "old-boot", Result: "completed-success"})
				}
				if stage == "restore-verify-intent" {
					j.Completed = append(j.Completed, Operation{ID: "restore-history", Stage: "restore-apply-intent", OperandID: j.RestorationID, BootID: "old-boot", Result: "completed-success"})
				}
				r, err := InspectRecovery(st)
				if err != nil {
					t.Fatal(err)
				}
				if r.Action == "" || r.RequiresQuiescence != (stage != "prepared" && (result == "" || result == "completion-unknown")) {
					t.Fatalf("wrong dispatch: %+v", r)
				}
			})
		}
	}
	invalid, _ := copyState(base)
	invalid.Journal.Operation = &Operation{ID: "stray"}
	if _, err := InspectRecovery(invalid); err == nil {
		t.Fatal("prepared accepted stray action")
	}
}
func TestShortWriteAndReferenceCorruption(t *testing.T) {
	s := fixtureStore(t)
	s.writeRecord = func(f *os.File, b []byte) (int, error) { return len(b) / 2, nil }
	if err := s.MarkDesiredStale(); !errors.Is(err, ErrPersistence) {
		t.Fatalf("short write: %v", err)
	}
	s = fixtureStore(t)
	prepared(t, s)
	if err := os.WriteFile(filepath.Join(s.rootPath, "inputs/nested/manifest"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	root, owner := s.rootPath, s.owner
	s.Close()
	if _, err := open(root, owner, false); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("bad retained input: %v", err)
	}
}

func TestRestartCannotContinueCandidate(t *testing.T) {
	s := fixtureStore(t)
	id := prepared(t, s)
	if err := s.RecordIntent("candidate-apply-intent", id, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}
	root, owner := s.rootPath, s.owner
	s.Close()
	again, err := open(root, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	index := 0
	if err := again.RecordIntent("candidate-verify-intent", id, &index); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("resumed candidate after restart: %v", err)
	}
}

func operandIDForTest(j *Journal) string {
	if j.Operation != nil {
		return j.Operation.OperandID
	}
	return ""
}

func TestWorkspaceBarrierFailureRecordsUncertainty(t *testing.T) {
	s := fixtureStore(t)
	id := prepared(t, s)
	if err := s.RecordIntent("candidate-apply-intent", id, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.rootPath, "workspaces/candidate/generated"), []byte("recovery marker"), 0600); err != nil {
		t.Fatal(err)
	}
	s.fault = func(point string) error {
		if point == "workspace-file-sync" {
			return ErrPersistence
		}
		return nil
	}
	if err := s.RecordResult("completed-success", true); !errors.Is(err, ErrPersistence) {
		t.Fatalf("workspace barrier bypassed: %v", err)
	}
	root, owner := s.rootPath, s.owner
	s.Close()
	again, err := open(root, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	st, _ := again.Read()
	if st.Journal == nil || st.Journal.Operation.Result != "completion-unknown" || st.LastGoodID != "" {
		t.Fatal("lost pending operation after failed barrier")
	}
}

func TestFreshRestorationKeepsFrozenPolicy(t *testing.T) {
	s := fixtureStore(t)
	prior := prepared(t, s)
	promote(t, s, prior)
	candidate := instance(t, s, "workspaces/next")
	candidate.Saved.Reconcile.OperationTimeoutSeconds = 17
	identity, err := ConfigurationIdentity(candidate.Saved)
	if err != nil {
		t.Fatal(err)
	}
	b := Target{Source: Revision{"opaque-driver", "opaque-repository", "B"}, ConfigurationIdentity: identity}
	candidate.Target = b
	if err := s.ObserveDesired(b); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginPreparation(FrozenPolicy{true, 17}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}
	id, err := s.Prepared(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIntent("prior-stop-intent", prior, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIntent("candidate-apply-intent", id, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-failure", true); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginRecovery(true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIntent("cleanup-intent", id, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.rootPath, "workspaces/restored"), 0700); err != nil {
		t.Fatal(err)
	}
	restored, err := s.AttachRestoration("workspaces/restored")
	if err != nil {
		t.Fatal(err)
	}
	if restored == prior || restored == id {
		t.Fatal("instance ID reused")
	}
	if err := s.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIntent("restore-apply-intent", restored, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}
	index := 0
	if err := s.RecordIntent("restore-verify-intent", restored, &index); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordHealthWindow(true, true); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Read()
	if st.Journal.Policy.OperationTimeoutSeconds != 17 {
		t.Fatal("recovery policy changed")
	}
	if err := s.CommitVerified(); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Read()
	if st.LastGoodID != restored || st.Active.InstanceID != restored || st.Journal != nil || st.Suppressed == nil || *st.Suppressed != b {
		t.Fatal("restoration outcome was not atomic")
	}
	if st.Instances[restored].Workspace != "workspaces/restored" || st.Instances[restored].Target != target(t, "A") {
		t.Fatal("restoration lost saved inputs")
	}
}

func TestGeneratedRecoveryFilesRemainMutableButRequired(t *testing.T) {
	s := fixtureStore(t)
	id := prepared(t, s)
	if err := s.RecordIntent("candidate-apply-intent", id, nil); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(s.rootPath, "workspaces/candidate/marker")
	if err := os.WriteFile(marker, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCheckpoint("completed-success", true, []string{"marker"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("changed mutable contents"), 0600); err != nil {
		t.Fatal(err)
	}
	root, owner := s.rootPath, s.owner
	s.Close()
	again, err := open(root, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := open(root, owner, false); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("missing recorded recovery file: %v", err)
	}
}

func TestObservationFailureRetriesAfterRestartOnly(t *testing.T) {
	s := fixtureStore(t)
	id := prepared(t, s)
	promote(t, s, id)
	root, owner := s.rootPath, s.owner
	s.Close()
	s, err := open(root, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.BeginObservation(); err != nil {
		t.Fatal(err)
	}
	index := 0
	if err := s.RecordIntent("startup-verify-intent", id, &index); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-failure", true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordHealthWindow(false, true); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitObservation(false); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginObservation(); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("retried failed startup window in same process")
	}
	s.Close()
	again, err := open(root, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	st, _ := again.Read()
	if st.Health.State != "unknown" || st.LastGoodID != id {
		t.Fatal("historical health survived restart")
	}
	if err := again.BeginObservation(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryFailureCannotLoop(t *testing.T) {
	s := fixtureStore(t)
	id := prepared(t, s)
	if err := s.RecordIntent("candidate-apply-intent", id, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-failure", true); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginRecovery(true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIntent("cleanup-intent", id, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-failure", true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIntent("cleanup-intent", id, nil); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("retried failed recovery in process")
	}
}

func TestReadAndInputOwnership(t *testing.T) {
	s := fixtureStore(t)
	id := prepared(t, s)
	st, _ := s.Read()
	value := st.Instances[id]
	value.Saved.Runtime.Exec.Apply[0] = "changed"
	st.Instances[id] = value
	actual, _ := s.Read()
	if actual.Instances[id].Saved.Runtime.Exec.Apply[0] == "changed" {
		t.Fatal("read exposed writable authoritative state")
	}
	before, _ := s.Read()
	err := s.update(func(next *State) error {
		v := next.Instances[id]
		v.Workspace = "other"
		next.Instances[id] = v
		return nil
	}, true)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatal("changed immutable instance binding")
	}
	after, _ := s.Read()
	if before.Generation != after.Generation {
		t.Fatal("invalid transition wrote state")
	}
}

func TestFailedPromotionRetainsRecoveryJournal(t *testing.T) {
	s := fixtureStore(t)
	id := prepared(t, s)
	if err := s.RecordIntent("candidate-apply-intent", id, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}
	index := 0
	if err := s.RecordIntent("candidate-verify-intent", id, &index); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordHealthWindow(true, true); err != nil {
		t.Fatal(err)
	}
	failed := false
	s.fault = func(point string) error {
		if point == "directory-sync" && !failed {
			failed = true
			return ErrPersistence
		}
		return nil
	}
	if err := s.CommitVerified(); !errors.Is(err, ErrPersistence) {
		t.Fatalf("expected promotion persistence error: %v", err)
	}
	root, owner := s.rootPath, s.owner
	s.Close()
	again, err := open(root, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	st, _ := again.Read()
	if st.Journal == nil || st.LastGoodID != "" || st.Journal.CandidateID != id {
		t.Fatal("failed promotion lost recovery operands")
	}
}
