//go:build linux

package state_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"pulltide/internal/application"
	"pulltide/internal/reconcile"
	execruntime "pulltide/internal/runtime/exec"
	"pulltide/internal/state"
)

func execEngine(t *testing.T, rollback bool) (*reconcile.Engine, *fakeDriver, string, string) {
	t.Helper()
	_, f := newEngine(t)
	app, err := application.Parse(f.doc)
	if err != nil {
		t.Fatal(err)
	}
	app.Spec.Source.Path = "."
	app.Spec.Rollback.Enabled = rollback
	service := filepath.Join(t.TempDir(), "managed-service")
	failure := filepath.Join(t.TempDir(), "fail-next-apply")
	// Harmless stand-in for a synchronous externally managed service operation.
	script := `umask 077; printf running > "$1"; printf saved > marker; printf '%s' '{"version":1,"files":["marker"]}' > .pulltide-exec-recovery.json; if [ -f "$2" ]; then rm -f "$2"; exit 9; fi`
	app.Spec.Runtime.Exec.Apply = []string{"/bin/sh", "-c", script, "fixture", service, failure}
	app.Spec.Runtime.Exec.Stop = []string{"/bin/sh", "-c", `rm -f "$1"`, "fixture", service}
	app.Spec.Health.Checks = []application.Check{{Type: "command", Command: []string{"/bin/sh", "-c", `test -f "$1"`, "fixture", service}, TimeoutSeconds: 2}}
	f.doc, err = json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := execruntime.New(f.store, f, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = execruntime.New(f.store, f, false); err == nil {
		t.Fatal("foreground profile was not required")
	}
	engine, err := reconcile.New(f.doc, func(*application.Application) (*state.Store, error) { return f.store, nil }, f, driver, driver, f.clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	return engine, f, service, failure
}
func TestExecRuntimeRecoveryAndExternallyManagedService(t *testing.T) {
	for _, rollback := range []bool{true, false} {
		t.Run(map[bool]string{true: "restore", false: "cleanup"}[rollback], func(t *testing.T) {
			e, f, service, failure := execEngine(t, rollback)
			mustCycle(t, e)
			old, err := f.store.Read()
			if err != nil {
				t.Fatal(err)
			}
			first := old.Instances[old.LastGoodID]
			if len(first.RequiredRecoveryFiles) != 2 {
				t.Fatal("inventory references not recorded", first.RequiredRecoveryFiles)
			}
			if err = os.WriteFile(failure, []byte("fail"), 0600); err != nil {
				t.Fatal(err)
			}
			f.revision = "B"
			if err = e.Reconcile(context.Background()); err != nil {
				t.Fatal("recovery", err)
			}
			now, err := f.store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if now.Journal != nil || now.Suppressed == nil || now.Suppressed.Source.Revision != "B" {
				t.Fatal("recovery/suppression incomplete")
			}
			if rollback {
				restored := now.Instances[now.LastGoodID]
				if restored.Workspace == first.Workspace || restored.ID == first.ID || restored.Target.Source.Revision != "A" || len(restored.RequiredRecoveryFiles) != 2 {
					t.Fatal("restoration did not bind fresh inputs")
				}
				if _, err = os.Stat(filepath.Join(f.root, restored.Workspace, "marker")); err != nil {
					t.Fatal(err)
				}
				if b, err := os.ReadFile(service); err != nil || string(b) != "running" {
					t.Fatal("service not restored", err)
				}
			} else if now.Active.State != "absent" || now.LastGoodID != old.LastGoodID {
				t.Fatal("cleanup lost historical last-good")
			}
			mustCycle(t, e) // Suppressed B never replays commands.
		})
	}
}
func TestExecRuntimeCoreDurabilityFailureRetainsPendingEvidence(t *testing.T) {
	e, f, _, _ := execEngine(t, true)
	state.FaultForTest(f.store, func(point string) error {
		if point == "workspace-file-sync" {
			return errors.New("injected persistence fault")
		}
		return nil
	})
	if err := e.Reconcile(context.Background()); err != reconcile.ErrBlocked {
		t.Fatal("durability failure", err)
	}
	if _, err := f.store.Read(); err != state.ErrRecoveryRequired {
		t.Fatal("failed handle was not poisoned", err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := state.OpenForTest(f.root, state.Ownership{Name: "example", Runtime: "exec", ResourceIdentity: "operator-resource"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	now, err := reopened.Read()
	if err != nil {
		t.Fatal(err)
	}
	if now.LastGoodID != "" || now.Journal == nil || strings.Contains(now.Journal.Stage, "verify") {
		t.Fatal("failed checkpoint advanced lifecycle")
	}
}

func TestExecInterruptedControllerBlocksSameBootRecovery(t *testing.T) {
	if root := os.Getenv("PULLTIDE_EXEC_CHILD_ROOT"); root != "" {
		store, err := state.OpenForTest(root, state.Ownership{Name: "example", Runtime: "exec", ResourceIdentity: "operator-resource"}, false)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		doc, err := os.ReadFile(os.Getenv("PULLTIDE_EXEC_CHILD_DOC"))
		if err != nil {
			t.Fatal(err)
		}
		f := &fakeDriver{store: store, root: root, revision: "A", clock: &fakeClock{now: time.Now()}, doc: doc}
		runtime, err := execruntime.New(store, f, true)
		if err != nil {
			t.Fatal(err)
		}
		engine, err := reconcile.New(doc, func(*application.Application) (*state.Store, error) { return store, nil }, f, runtime, runtime, f.clock, nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = engine.Reconcile(context.Background())
		return
	}
	_, f, _, _ := execEngine(t, true)
	app, err := application.Parse(f.doc)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "group")
	app.Spec.Runtime.Exec.Apply = []string{"/bin/sh", "-c", `printf '%s' "$$" > "$1"; sleep 30`, "fixture", marker}
	doc, err := json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	docfile := filepath.Join(t.TempDir(), "application.json")
	if err = os.WriteFile(docfile, doc, 0600); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Close(); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestExecInterruptedControllerBlocksSameBootRecovery$")
	child.Env = append(os.Environ(), "PULLTIDE_EXEC_CHILD_ROOT="+f.root, "PULLTIDE_EXEC_CHILD_DOC="+docfile)
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	group := 0
	defer func() {
		if !waited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
		if group > 0 {
			_ = syscall.Kill(-group, syscall.SIGKILL)
		}
	}()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if data, e := os.ReadFile(marker); e == nil {
			group, _ = strconv.Atoi(string(data))
			if group > 0 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if group == 0 {
		t.Fatal("child command did not begin")
	}
	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	waited = true
	reopened, err := state.OpenForTest(f.root, state.Ownership{Name: "example", Runtime: "exec", ResourceIdentity: "operator-resource"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	f.store = reopened
	runtime, err := execruntime.New(reopened, f, true)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := reconcile.New(doc, func(*application.Application) (*state.Store, error) { return reopened, nil }, f, runtime, runtime, f.clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.Reconcile(context.Background()); err != reconcile.ErrBlocked {
		t.Fatal("interrupted mutation allowed recovery", err)
	}
	now, err := reopened.Read()
	if err != nil {
		t.Fatal(err)
	}
	if now.Journal == nil || now.Journal.Operation.Result != "" || now.LastGoodID != "" || now.Suppressed != nil {
		t.Fatal("interrupted evidence lost")
	}
	if err = syscall.Kill(-group, 0); err != nil {
		t.Fatal("test command should remain observable until explicit cleanup", err)
	}
}

func TestExecUnsupportedBackgroundWorkNeverStartsRollback(t *testing.T) {
	_, f, _, _ := execEngine(t, true)
	app, err := application.Parse(f.doc)
	if err != nil {
		t.Fatal(err)
	}
	stopMarker := filepath.Join(t.TempDir(), "stop-invoked")
	checkMarker := filepath.Join(t.TempDir(), "check-invoked")
	app.Spec.Runtime.Exec.Apply = []string{"/bin/sh", "-c", "sleep 30 & exit 0"}
	app.Spec.Runtime.Exec.Stop = []string{"/bin/sh", "-c", `printf stopped > "$1"`, "fixture", stopMarker}
	app.Spec.Health.Checks = []application.Check{{Type: "command", Command: []string{"/bin/sh", "-c", `printf checked > "$1"`, "fixture", checkMarker}, TimeoutSeconds: 2}}
	doc, err := json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := execruntime.New(f.store, f, true)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := reconcile.New(doc, func(*application.Application) (*state.Store, error) { return f.store, nil }, f, driver, driver, f.clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.Reconcile(context.Background()); err != reconcile.ErrBlocked {
		t.Fatal("uncertain command allowed recovery", err)
	}
	now, err := f.store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if now.Journal == nil || now.Journal.Operation.Result != string(reconcile.Unknown) || now.LastGoodID != "" || now.Suppressed != nil {
		t.Fatal("uncertain result was not retained")
	}
	for _, name := range []string{stopMarker, checkMarker} {
		if _, err = os.Stat(name); !os.IsNotExist(err) {
			t.Fatal("dependent command ran after uncertainty", err)
		}
	}
}
