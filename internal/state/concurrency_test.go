//go:build linux

package state

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestLockSurvivesUntilProcessDeath(t *testing.T) {
	s := fixtureStore(t)
	id := prepared(t, s)
	if err := s.RecordIntent("candidate-apply-intent", id, nil); err != nil {
		t.Fatal(err)
	}
	root, owner := s.rootPath, s.owner
	s.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStateLockChild$")
	cmd.Env = append(os.Environ(), "PULLTIDE_STATE_TEST_ROOT="+root)
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	lines := bufio.NewScanner(output)
	if !lines.Scan() || lines.Text() != "locked" {
		t.Fatal("child could not acquire lock")
	}
	if _, err := open(root, owner, false); !errors.Is(err, ErrLocked) {
		t.Fatalf("competing process: %v", err)
	}
	cmd.Process.Kill()
	cmd.Wait()
	again, err := open(root, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	state, err := again.Read()
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectRecovery(state)
	if err != nil || !inspection.RequiresQuiescence {
		t.Fatal("process death/lock release inferred quiescence")
	}
}
func TestStateLockChild(t *testing.T) {
	root := os.Getenv("PULLTIDE_STATE_TEST_ROOT")
	if root == "" {
		return
	}
	s, err := open(root, Ownership{Name: "example", Runtime: "exec", ResourceIdentity: "operator-resource"}, false)
	if err != nil {
		os.Exit(2)
	}
	defer s.Close()
	os.Stdout.WriteString("locked\n")
	for {
		time.Sleep(time.Hour)
	}
}

func TestHostLockSerializesReferencePublication(t *testing.T) {
	s := fixtureStore(t)
	host, err := takeLock(s.root, ".artifact.lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDesiredStale(); !errors.Is(err, ErrLocked) {
		t.Fatalf("bypassed artifact lock: %v", err)
	}
	unlock(host)
	if err := s.MarkDesiredStale(); err != nil {
		t.Fatal(err)
	}
}
