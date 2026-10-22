//go:build linux

package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInPlaceReplacementAndGroupedCleanup(t *testing.T) {
	s := fixtureStore(t)
	prior := prepared(t, s)
	promote(t, s, prior)
	candidate := instance(t, s, "workspaces/new")
	candidate.Target = target(t, "B")
	if err := s.ObserveDesired(candidate.Target); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginPreparation(FrozenPolicy{true, 300}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}
	id, err := s.Prepared(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordReplacementIntent(true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-failure", true); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginRecovery(true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCleanupIntent([]string{prior, id}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Read()
	if len(st.Journal.MayBeLive) != 0 || !st.Journal.CleanupCompleted[prior] || !st.Journal.CleanupCompleted[id] {
		t.Fatal("group result was not atomic")
	}
}

func TestRestorationReservationPrecedesWorkspace(t *testing.T) {
	s := fixtureStore(t)
	prior := prepared(t, s)
	promote(t, s, prior)
	candidate := instance(t, s, "workspaces/new")
	candidate.Target = target(t, "B")
	s.ObserveDesired(candidate.Target)
	s.BeginPreparation(FrozenPolicy{true, 300}, nil)
	s.RecordResult("completed-success", true)
	id, err := s.Prepared(candidate)
	if err != nil {
		t.Fatal(err)
	}
	s.RecordReplacementIntent(true)
	s.RecordResult("completed-failure", true)
	s.BeginRecovery(true)
	s.RecordCleanupIntent([]string{prior, id})
	s.RecordResult("completed-success", true)
	restored, err := s.AttachRestoration("workspaces/fresh")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.Read()
	if st.Journal.Operation.OperandID != restored || st.Journal.Stage != "restore-prepare-intent" {
		t.Fatal("reservation intent missing")
	}
	if _, err := os.Stat(filepath.Join(s.rootPath, "workspaces/fresh")); !os.IsNotExist(err) {
		t.Fatal("reservation created workspace")
	}
	if err := os.MkdirAll(filepath.Join(s.rootPath, "workspaces/fresh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult("completed-success", true); err != nil {
		t.Fatal(err)
	}
}
