//go:build linux

package execruntime

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"pulltide/internal/application"
	"pulltide/internal/reconcile"
	"pulltide/internal/state"
	"strings"
	"testing"
	"time"
)

type sourceDouble struct {
	reconcile.Source
	root    string
	invalid bool
}

func (s *sourceDouble) ValidateInputs(_ context.Context, i state.Instance, _ []string) error {
	if s.invalid {
		return ErrRuntime
	}
	for _, ref := range i.Inputs {
		b, e := os.ReadFile(filepath.Join(s.root, ref.Path))
		if e != nil || fmt.Sprintf("%x", sha256.Sum256(b)) != ref.SHA256 {
			return ErrRuntime
		}
	}
	return nil
}

type snapshotDouble struct{}

func (snapshotDouble) ReadFile(context.Context, string) ([]byte, error) { return nil, os.ErrNotExist }
func (snapshotDouble) ReadDir(context.Context, string) ([]reconcile.SourceEntry, error) {
	return nil, nil
}
func fixture(t *testing.T) (*Driver, *state.State, state.Instance) {
	t.Helper()
	root := privateDir(t)
	data, err := os.ReadFile("../../../tests/fixtures/contract/valid/exec-minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	app, err := application.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	app.Spec.Source.Path = "app"
	app.Spec.Runtime.Exec.Apply = []string{"/bin/sh", "-c", `umask 077; printf saved > marker; printf '%s' '{"version":1,"files":["workspaces-ignored"]}' > .pulltide-exec-recovery.json`}
	app.Spec.Runtime.Exec.Stop = []string{"/bin/true"}
	app.Spec.Health.Checks = []application.Check{{Type: "command", Command: []string{"/bin/true"}, TimeoutSeconds: 1}}
	identity, _ := state.ConfigurationIdentity(app.Spec)
	i := state.Instance{ID: "one", Workspace: "workspaces/one", Saved: app.Spec, Target: state.Target{Source: state.Revision{Driver: "test", SourceIdentity: "source", Revision: "A"}, ConfigurationIdentity: identity}}
	write(t, filepath.Join(root, i.Workspace, "app/seed"), "source")
	write(t, filepath.Join(root, "source"), "source")
	i.Inputs = []state.FileReference{{Path: "source", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("source")))}}
	st := &state.State{Version: 1, Generation: 1, InstallationID: "installation", Ownership: state.Ownership{Name: "test", Runtime: "exec", ResourceIdentity: "operator-resource"}, Instances: map[string]state.Instance{i.ID: i}, Active: state.Active{State: "absent"}, Journal: &state.Journal{Kind: "deployment", Stage: "candidate-apply-intent", OperandID: i.ID, Operation: &state.Operation{ID: "operation", Stage: "candidate-apply-intent", OperandID: i.ID}}}
	d := &Driver{root: root, read: func() (*state.State, error) { return st, nil }, source: &sourceDouble{root: root}, owner: st.Ownership, installation: st.InstallationID, profile: true, process: supervisor{grace: 40 * time.Millisecond, confirmation: 200 * time.Millisecond}}
	return d, st, i
}
func setApply(st *state.State, i state.Instance, argv []string) state.Instance {
	i.Saved.Runtime.Exec.Apply = argv
	st.Instances[i.ID] = i
	return i
}
func TestDriverLiteralArgumentsEnvironmentAndInventory(t *testing.T) {
	d, st, i := fixture(t)
	t.Setenv("HOME", "FAKE_HOST_SECRET")
	t.Setenv("LANG", "host")
	t.Setenv("PATH", "/nowhere")
	script := `umask 077; printf '%s\n' "$1" "$2" "$PATH" "$LANG" "${HOME-unset}" "$CUSTOM" "$PWD" > report; printf '%s' '{"version":1,"files":["app/report"]}' > .pulltide-exec-recovery.json`
	i.Saved.Runtime.Exec.Env = map[string]string{"CUSTOM": "saved-value"}
	i = setApply(st, i, []string{"/bin/sh", "-c", script, "fixture", "a b", "$(touch forbidden)"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := d.Apply(ctx, i)
	if result.Outcome != reconcile.Success || len(result.RecoveryFiles) != 2 {
		t.Fatal("apply", result)
	}
	report, err := os.ReadFile(filepath.Join(d.root, i.Workspace, "app/report"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(report)
	if !strings.Contains(text, "a b\n$(touch forbidden)\n/usr/bin:/bin\nC\nunset\nsaved-value\n") || strings.Contains(text, "FAKE_HOST_SECRET") {
		t.Fatal("arguments or environment changed")
	}
}
func TestDriverPreconditionsAndInventoryFailure(t *testing.T) {
	for _, which := range []string{"intent", "source", "cwd", "inventory", "sync", "unknown"} {
		t.Run(which, func(t *testing.T) {
			d, st, i := fixture(t)
			i = setApply(st, i, []string{"/bin/sh", "-c", `umask 077; printf invoked > invoked; printf '%s' '{"version":1,"files":[]}' > .pulltide-exec-recovery.json`})
			switch which {
			case "intent":
				st.Journal = nil
			case "source":
				d.source.(*sourceDouble).invalid = true
			case "cwd":
				if e := os.RemoveAll(filepath.Join(d.root, i.Workspace, "app")); e != nil {
					t.Fatal(e)
				}
			case "inventory":
				i = setApply(st, i, []string{"/bin/true"})
			case "sync":
				d.fault = func(string) error { return ErrRuntime }
			case "unknown":
				i = setApply(st, i, []string{"/bin/sh", "-c", "sleep 30 & exit 0"})
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result := d.Apply(ctx, i)
			want := reconcile.Failure
			if which == "unknown" {
				want = reconcile.Unknown
			}
			if result.Outcome != want || len(result.RecoveryFiles) != 0 {
				t.Fatal(which, result)
			}
			if which == "intent" || which == "source" || which == "cwd" {
				if _, e := os.Stat(filepath.Join(d.root, i.Workspace, "app/invoked")); !os.IsNotExist(e) {
					t.Fatal("command invoked before admission")
				}
			}
			if which == "unknown" && d.Stop(ctx, []state.Instance{i}).Outcome != reconcile.Unknown {
				t.Fatal("uncertain operation allowed another command")
			}
		})
	}
}
func TestDiscoverAndPrepareAreObservational(t *testing.T) {
	d, st, i := fixture(t)
	i = setApply(st, i, []string{"/bin/true"})
	roots, e := d.Discover(context.Background(), snapshotDouble{}, i.Saved)
	if e != nil || len(roots) == 0 || roots[0] != "app" {
		t.Fatal("discovery", roots, e)
	}
	if _, e = d.Prepare(context.Background(), i, snapshotDouble{}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(d.root, i.Workspace, "app", inventoryName)); !os.IsNotExist(e) {
		t.Fatal("prepare executed apply")
	}
	i = setApply(st, i, []string{"missing-executable"})
	if _, e = d.Prepare(context.Background(), i, snapshotDouble{}, nil); e == nil {
		t.Fatal("missing executable accepted")
	}
}

func TestStableRecoveryDeclarationsBlockChangesAndPartialCleanupWorks(t *testing.T) {
	d, st, i := fixture(t)
	i = setApply(st, i, []string{"/bin/sh", "-c", `umask 077; printf data > marker; printf '%s' '{"version":1,"files":["app/marker"]}' > .pulltide-exec-recovery.json`})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := d.Apply(ctx, i)
	if result.Outcome != reconcile.Success {
		t.Fatal(result)
	}
	i.RequiredRecoveryFiles = result.RecoveryFiles
	i.Saved.Runtime.Exec.Stop = []string{"/bin/sh", "-c", `printf stopped > stopped`}
	st.Instances[i.ID] = i
	st.Journal.Operation = &state.Operation{ID: "stop", Stage: "prior-stop-intent", OperandID: i.ID}
	st.Journal.Stage = "prior-stop-intent"
	write(t, filepath.Join(d.root, i.Workspace, "app", inventoryName), `{"version":1,"files":[]}`)
	if r := d.Stop(ctx, []state.Instance{i}); r.Outcome != reconcile.Failure {
		t.Fatal("changed inventory accepted", r)
	}
	if _, err := os.Stat(filepath.Join(d.root, i.Workspace, "app/stopped")); !os.IsNotExist(err) {
		t.Fatal("command ran with changed recovery declaration")
	}
	st.Journal.Completed = []state.Operation{{Stage: "prior-stop-intent", OperandID: i.ID, Result: string(reconcile.Failure)}}
	if r := d.Stop(ctx, []state.Instance{i}); r.Outcome != reconcile.Success {
		t.Fatal("partial cleanup could not tolerate interrupted inventory", r)
	}
}
func TestEffectivePathAndEscapingWorkingDirectory(t *testing.T) {
	d, st, i := fixture(t)
	t.Setenv("PATH", "/nowhere")
	i = setApply(st, i, []string{"sh", "-c", `umask 077; printf '%s' '{"version":1,"files":[]}' > .pulltide-exec-recovery.json`})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if r := d.Apply(ctx, i); r.Outcome != reconcile.Success {
		t.Fatal("effective saved PATH not used", r)
	}
	other := privateDir(t)
	if err := os.Symlink(other, filepath.Join(d.root, i.Workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	i.Saved.Source.Path = "escape"
	st.Instances[i.ID] = i
	if r := d.Apply(ctx, i); r.Outcome != reconcile.Failure {
		t.Fatal("escaped cwd allowed", r)
	}
}
func TestCommandChecksUseSavedContextAndReportUnavailableContextUnknown(t *testing.T) {
	d, st, i := fixture(t)
	index := 0
	st.Journal.Stage = "startup-verify-intent"
	st.Journal.Operation = &state.Operation{ID: "check", Stage: "startup-verify-intent", OperandID: i.ID, CheckIndex: &index}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if r := d.Check(ctx, i, application.Check{Type: "command", Command: []string{"/bin/false"}}); r.Outcome != reconcile.Success {
		t.Fatal("caller changed saved check", r)
	}
	d.source.(*sourceDouble).invalid = true
	if r := d.Check(ctx, i, i.Saved.Health.Checks[0]); r.Outcome != reconcile.Unknown {
		t.Fatal("unavailable context reported as unhealthy", r)
	}
}

func TestStableActiveRequiresExplicitRecoveryDeclaration(t *testing.T) {
	d, st, i := fixture(t)
	st.Journal = nil
	st.Active = state.Active{State: "known", InstanceID: i.ID}
	evidence, err := d.Inspect(context.Background(), st)
	if err == nil && evidence.Inputs {
		t.Fatal("active instance without an explicit inventory was accepted")
	}
}

func TestCommandCheckCannotPublishChangedRecoveryInventory(t *testing.T) {
	d, st, i := fixture(t)
	i = setApply(st, i, []string{"/bin/sh", "-c", `umask 077; printf data > marker; printf '%s' '{"version":1,"files":["app/marker"]}' > .pulltide-exec-recovery.json`})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := d.Apply(ctx, i)
	if result.Outcome != reconcile.Success {
		t.Fatal(result)
	}
	i.RequiredRecoveryFiles = result.RecoveryFiles
	i.Saved.Health.Checks[0].Command = []string{"/bin/sh", "-c", `printf '%s' '{"version":1,"files":[]}' > .pulltide-exec-recovery.json`}
	st.Instances[i.ID] = i
	index := 0
	st.Journal.Stage = "candidate-verify-intent"
	st.Journal.Operation = &state.Operation{ID: "verify", Stage: "candidate-verify-intent", OperandID: i.ID, CheckIndex: &index}
	if r := d.Check(ctx, i, i.Saved.Health.Checks[0]); r.Outcome != reconcile.Unknown {
		t.Fatal("nonobservational check published success", r)
	}
}

type declaredSnapshot struct {
	seen     []string
	reserved bool
}

func (s *declaredSnapshot) ReadFile(_ context.Context, name string) ([]byte, error) {
	s.seen = append(s.seen, name)
	return []byte("source bytes"), nil
}
func (s *declaredSnapshot) ReadDir(context.Context, string) ([]reconcile.SourceEntry, error) {
	if s.reserved {
		return []reconcile.SourceEntry{{Name: inventoryName}}, nil
	}
	return nil, nil
}
func TestDiscoverDeclaredExecutableSiblingsWithoutNormalizingSymlinks(t *testing.T) {
	d, _, i := fixture(t)
	i.Saved.Runtime.Exec.Apply = []string{"../sibling/tool"}
	i.Saved.Runtime.Exec.Stop = []string{"link/../stop"}
	s := &declaredSnapshot{}
	roots, err := d.Discover(context.Background(), s, i.Saved)
	if err != nil || !strings.Contains(strings.Join(roots, ","), "app/../sibling/tool") || !strings.Contains(strings.Join(s.seen, ","), "app/link/../stop") {
		t.Fatal("declared dependencies or lookup semantics lost", roots, s.seen, err)
	}
	s.reserved = true
	if _, err = d.Discover(context.Background(), s, i.Saved); err == nil {
		t.Fatal("tracked reserved inventory accepted")
	}
}
func TestRestartedDriverUsesSavedCheckEnvironment(t *testing.T) {
	d, st, i := fixture(t)
	i.Saved.Runtime.Exec.Env = map[string]string{"CUSTOM": "saved"}
	i.Saved.Health.Checks[0].Command = []string{"/bin/sh", "-c", `test "$CUSTOM" = saved && test "${HOME-unset}" = unset && test "$PATH" = /usr/bin:/bin`}
	st.Instances[i.ID] = i
	index := 0
	st.Journal.Stage = "startup-verify-intent"
	st.Journal.Operation = &state.Operation{ID: "startup-check", Stage: "startup-verify-intent", OperandID: i.ID, CheckIndex: &index}
	t.Setenv("PATH", "/nowhere")
	t.Setenv("HOME", "FAKE_HOST_SECRET")
	t.Setenv("CUSTOM", "changed")
	restarted := &Driver{root: d.root, read: d.read, source: d.source, owner: d.owner, installation: d.installation, profile: true}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if result := restarted.Check(ctx, i, i.Saved.Health.Checks[0]); result.Outcome != reconcile.Success {
		t.Fatal("saved environment changed after driver restart", result)
	}
}
