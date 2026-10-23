//go:build linux

package state_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"pulltide/internal/application"
	"pulltide/internal/reconcile"
	"pulltide/internal/state"
)

func TestEngineValidatesBeforeAcquiringState(t *testing.T) {
	acquired := false
	_, err := reconcile.New([]byte("invalid"), func(*application.Application) (*state.Store, error) { acquired = true; return nil, nil }, nil, nil, nil, nil, nil)
	if err == nil || acquired {
		t.Fatal("invalid input reached ownership acquisition")
	}
}

// Fake adapters below assert durable intent before every simulated action.
type fakeDriver struct {
	store       *state.Store
	root        string
	revision    string
	trace       []string
	sourceErr   error
	applyHook   func(context.Context, state.Instance) reconcile.Result
	stopHook    func(context.Context, []state.Instance) reconcile.Result
	checkHook   func(context.Context, state.Instance, application.Check) reconcile.Result
	inspectHook func(*state.State) (reconcile.Evidence, error)
	prepareErr  error
	inPlace     bool
	grouped     bool
	clock       *fakeClock
	doc         []byte
}

func (f *fakeDriver) Resolve(context.Context, application.Source) (state.Revision, error) {
	if f.sourceErr != nil {
		return state.Revision{}, f.sourceErr
	}
	return state.Revision{Driver: "opaque-source", SourceIdentity: "repo", Revision: f.revision}, nil
}
func (f *fakeDriver) Materialize(ctx context.Context, t state.Target, s application.Spec, staging string, run reconcile.Artifacts) (state.Instance, error) {
	id, _ := state.NewID()
	snapshot := staging + "/" + id
	workspace := snapshot + "/workspace"
	err := run(ctx, func(context.Context) reconcile.Result {
		st, _ := f.store.Read()
		if st.Journal == nil || st.Journal.Stage != "artifact-intent" {
			panic("missing artifact intent")
		}
		err := os.MkdirAll(filepath.Join(f.root, workspace), 0700)
		if err == nil {
			err = os.WriteFile(filepath.Join(f.root, snapshot, "source"), []byte("immutable"), 0600)
		}
		if err != nil {
			return reconcile.Result{Outcome: reconcile.Failure}
		}
		return reconcile.Result{Outcome: reconcile.Success}
	})
	return state.Instance{Target: t, Saved: s, Workspace: workspace, Inputs: []state.FileReference{{Path: snapshot + "/source", SHA256: hash()}}}, err
}

type fakeSnapshot struct{ instance state.Instance }

func (s *fakeSnapshot) ReadFile(context.Context, string) ([]byte, error) {
	return []byte("immutable"), nil
}
func (s *fakeSnapshot) ReadDir(context.Context, string) ([]reconcile.SourceEntry, error) {
	return nil, nil
}
func (f *fakeDriver) Stage(ctx context.Context, t state.Target, s application.Spec, stage string, run reconcile.Artifacts) (reconcile.Snapshot, error) {
	i, err := f.Materialize(ctx, t, s, stage, run)
	if err != nil {
		return nil, err
	}
	return &fakeSnapshot{instance: i}, nil
}
func (f *fakeDriver) Publish(_ context.Context, s reconcile.Snapshot, _ []string, _ reconcile.Artifacts) (state.Instance, error) {
	return s.(*fakeSnapshot).instance, nil
}
func (f *fakeDriver) ValidateInputs(context.Context, state.Instance, []string) error { return nil }
func (f *fakeDriver) Discover(context.Context, reconcile.Snapshot, application.Spec) ([]string, error) {
	return nil, nil
}
func (f *fakeDriver) RestoreSource(ctx context.Context, i state.Instance, run reconcile.Artifacts) error {
	return run(ctx, func(context.Context) reconcile.Result {
		if os.MkdirAll(filepath.Join(f.root, i.Workspace), 0700) != nil {
			return reconcile.Result{Outcome: reconcile.Failure}
		}
		return reconcile.Result{Outcome: reconcile.Success}
	})
}
func (f *fakeDriver) Prepare(ctx context.Context, i state.Instance, _ reconcile.Snapshot, run reconcile.Artifacts) (state.Instance, error) {
	return i, f.prepareErr
}
func (f *fakeDriver) InPlace(state.Instance) bool { return f.inPlace }
func (f *fakeDriver) Inspect(_ context.Context, st *state.State) (reconcile.Evidence, error) {
	if f.inspectHook != nil {
		return f.inspectHook(st)
	}
	return reconcile.Evidence{Owned: true, Inputs: true, Storage: true, Quiescent: true, Absent: true}, nil
}
func (f *fakeDriver) CleanupGroups(ids []string) [][]string {
	if f.grouped && len(ids) > 0 {
		return [][]string{ids}
	}
	groups := [][]string{}
	for _, id := range ids {
		groups = append(groups, []string{id})
	}
	return groups
}
func (f *fakeDriver) Apply(ctx context.Context, i state.Instance) reconcile.Result {
	f.assertIntent("apply", i.ID)
	f.trace = append(f.trace, "apply:"+i.Target.Source.Revision)
	if f.applyHook != nil {
		return f.applyHook(ctx, i)
	}
	return reconcile.Result{Outcome: reconcile.Success}
}
func (f *fakeDriver) Stop(ctx context.Context, is []state.Instance) reconcile.Result {
	f.assertIntent("stop", is[0].ID)
	for _, i := range is {
		f.trace = append(f.trace, "stop:"+i.Target.Source.Revision)
	}
	if f.stopHook != nil {
		return f.stopHook(ctx, is)
	}
	return reconcile.Result{Outcome: reconcile.Success}
}
func (f *fakeDriver) Restore(context.Context, state.Instance, reconcile.Artifacts) error { return nil }
func (f *fakeDriver) Check(ctx context.Context, i state.Instance, c application.Check) reconcile.Result {
	f.assertIntent("verify", i.ID)
	f.trace = append(f.trace, "health")
	if f.checkHook != nil {
		return f.checkHook(ctx, i, c)
	}
	return reconcile.Result{Outcome: reconcile.Success}
}

func newEngine(t *testing.T) (*reconcile.Engine, *fakeDriver) {
	t.Helper()
	root := t.TempDir()
	os.Chmod(root, 0700)
	if err := state.InitializeForTest(root); err != nil {
		t.Fatal(err)
	}
	owner := state.Ownership{Name: "example", Runtime: "exec", ResourceIdentity: "operator-resource"}
	s, err := state.OpenForTest(root, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	f := &fakeDriver{store: s, root: root, revision: "A", clock: &fakeClock{now: time.Now()}}
	doc, err := os.ReadFile("../../tests/fixtures/contract/valid/exec-minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.doc = doc
	e, err := reconcile.New(doc, func(*application.Application) (*state.Store, error) { return s, nil }, f, f, f, f.clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	return e, f
}
func TestEngineDeployThenNoop(t *testing.T) {
	e, f := newEngine(t)
	if err := e.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := f.store.Read()
	if st.LastGoodID == "" || st.Journal != nil || st.Health.State != "healthy" {
		t.Fatalf("not promoted: %+v", st)
	}
	if len(f.trace) != 2 || f.trace[0] != "apply:A" {
		t.Fatalf("trace %v", f.trace)
	}
}

func hash() string { h := sha256.Sum256([]byte("immutable")); return hex.EncodeToString(h[:]) }

func (f *fakeDriver) assertIntent(action, id string) {
	st, err := f.store.Read()
	if err != nil || st.Journal == nil || st.Journal.Operation == nil {
		panic("no durable intent")
	}
	op := st.Journal.Operation
	if op.Result != "" || (op.OperandID != id && action != "stop") {
		panic("invalid intent")
	}
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	waits  []time.Duration
	onWait func()
}

func (c *fakeClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }
func (c *fakeClock) Wait(ctx context.Context, d time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.advance(d)
	c.waits = append(c.waits, d)
	if c.onWait != nil {
		c.onWait()
	}
	return nil
}
func restart(t *testing.T, f *fakeDriver, output io.Writer) *reconcile.Engine {
	t.Helper()
	old, _ := f.store.Read()
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := state.OpenForTest(f.root, old.Ownership, false)
	if err != nil {
		t.Fatal(err)
	}
	f.store = s
	t.Cleanup(func() { s.Close() })
	e, err := reconcile.New(f.doc, func(*application.Application) (*state.Store, error) { return s, nil }, f, f, f, f.clock, output)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func mustCycle(t *testing.T, e *reconcile.Engine) {
	t.Helper()
	if err := e.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestEngineSourceFailureAndRecovery(t *testing.T) {
	e, f := newEngine(t)
	mustCycle(t, e)
	before, _ := f.store.Read()
	f.sourceErr = errors.New("secret")
	if err := e.Reconcile(context.Background()); err != reconcile.ErrCycle {
		t.Fatal(err)
	}
	st, _ := f.store.Read()
	if !st.DesiredStale || st.LastGoodID != before.LastGoodID || st.Health.State != "healthy" || st.Phase != "failed" {
		t.Fatal("source failure damaged active")
	}
	f.sourceErr = nil
	mustCycle(t, e)
	st, _ = f.store.Read()
	if st.Error != nil || st.Phase != "healthy" || len(f.trace) != 2 {
		t.Fatal("no-op failed to clear cycle error")
	}
}
func TestEnginePreparationFailureDoesNotSuppress(t *testing.T) {
	e, f := newEngine(t)
	mustCycle(t, e)
	f.revision = "B"
	f.prepareErr = errors.New("secret")
	if err := e.Reconcile(context.Background()); err != reconcile.ErrCycle {
		t.Fatal(err)
	}
	st, _ := f.store.Read()
	if st.Suppressed != nil || st.Journal != nil || st.Active.State != "known" || len(f.trace) != 2 {
		t.Fatal("preparation changed workload")
	}
	f.prepareErr = nil
	mustCycle(t, e)
	if strings.Join(f.trace, ",") != "apply:A,health,stop:A,apply:B,health" {
		t.Fatal(f.trace)
	}
}
func TestEngineRollbackUsesSavedInputsAndSuppresses(t *testing.T) {
	e, f := newEngine(t)
	mustCycle(t, e)
	old, _ := f.store.Read()
	f.revision = "B"
	f.applyHook = func(_ context.Context, i state.Instance) reconcile.Result {
		if i.Target.Source.Revision == "B" {
			return reconcile.Result{Outcome: reconcile.Failure}
		}
		return reconcile.Result{Outcome: reconcile.Success}
	}
	mustCycle(t, e)
	st, _ := f.store.Read()
	if st.Phase != "rolled-back" || st.Suppressed == nil || st.Suppressed.Source.Revision != "B" || st.LastGoodID == old.LastGoodID || st.Active.InstanceID != st.LastGoodID || st.Journal != nil {
		t.Fatalf("bad restoration %+v", st)
	}
	n := len(f.trace)
	mustCycle(t, e)
	if len(f.trace) != n {
		t.Fatal("suppressed B retried")
	}
	// Successful C observation clears suppression even if C preparation fails.
	f.revision = "C"
	f.prepareErr = errors.New("unavailable")
	if err := e.Reconcile(context.Background()); err != reconcile.ErrCycle {
		t.Fatal(err)
	}
	st, _ = f.store.Read()
	if st.Suppressed != nil {
		t.Fatal("different target did not clear suppression")
	}
	f.revision = "B"
	f.prepareErr = nil
	mustCycle(t, e)
	if len(f.trace) <= n {
		t.Fatal("returning B not retried")
	}
}
func TestEngineFirstFailureCleanupAndUnknown(t *testing.T) {
	for _, outcome := range []reconcile.Outcome{reconcile.Failure, reconcile.Unknown} {
		t.Run(string(outcome), func(t *testing.T) {
			e, f := newEngine(t)
			f.applyHook = func(context.Context, state.Instance) reconcile.Result { return reconcile.Result{Outcome: outcome} }
			err := e.Reconcile(context.Background())
			st, _ := f.store.Read()
			if outcome == reconcile.Unknown {
				if err != reconcile.ErrBlocked || st.Journal == nil || st.Active.State != "unknown" || len(f.trace) != 1 {
					t.Fatal("unknown operation allowed recovery")
				}
				if e.Reconcile(context.Background()) != reconcile.ErrBlocked {
					t.Fatal("unknown retried")
				}
			} else {
				if err != nil || st.Active.State != "absent" || st.Suppressed == nil || st.Journal != nil || strings.Join(f.trace, ",") != "apply:A,stop:A" {
					t.Fatal("cleanup failed", err, f.trace)
				}
			}
		})
	}
}
func TestEngineStartupObservationNeverReapplies(t *testing.T) {
	for _, unhealthy := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "unhealthy"}[unhealthy], func(t *testing.T) {
			e, f := newEngine(t)
			mustCycle(t, e)
			old, _ := f.store.Read()
			f.trace = nil
			if unhealthy {
				f.checkHook = func(context.Context, state.Instance, application.Check) reconcile.Result {
					f.clock.advance(time.Minute)
					return reconcile.Result{Outcome: reconcile.Failure}
				}
			}
			e = restart(t, f, nil)
			err := e.Reconcile(context.Background())
			st, _ := f.store.Read()
			if st.LastGoodID != old.LastGoodID || strings.Contains(strings.Join(f.trace, ","), "apply") || st.Journal != nil {
				t.Fatal("startup altered deployment")
			}
			if unhealthy {
				if err != reconcile.ErrBlocked || st.Health.State != "unhealthy" || st.Health.ObservedAt == nil {
					t.Fatal("bad unhealthy observation", err)
				}
				if e.Reconcile(context.Background()) != reconcile.ErrBlocked {
					t.Fatal("startup retried")
				}
			} else if err != nil || st.Health.State != "healthy" {
				t.Fatal(err)
			}
		})
	}
}
func TestEngineHealthOrderedRounds(t *testing.T) {
	e, f := newEngine(t)
	// Replace input before acquiring a new engine; three checks remain ordered.
	f.doc = []byte(strings.Replace(string(f.doc), "  health:\n", "  health:\n    timeoutSeconds: 20\n    intervalSeconds: 3\n", 1) + "")
	f.doc = []byte(strings.Replace(string(f.doc), "      url: http://127.0.0.1:8080/health", "      url: http://127.0.0.1:8080/A\n    - type: http\n      url: http://127.0.0.1:8080/B\n    - type: http\n      url: http://127.0.0.1:8080/C", 1))
	// No persisted deployment yet, so reopening is safe.
	e = restart(t, f, nil)
	calls := []string{}
	failed := false
	f.checkHook = func(_ context.Context, _ state.Instance, c application.Check) reconcile.Result {
		calls = append(calls, c.URL[len(c.URL)-1:])
		if strings.HasSuffix(c.URL, "B") && !failed {
			failed = true
			f.clock.advance(2 * time.Second)
			return reconcile.Result{Outcome: reconcile.Failure}
		}
		return reconcile.Result{Outcome: reconcile.Success}
	}
	mustCycle(t, e)
	if strings.Join(calls, ",") != "A,B,A,B,C" || len(f.clock.waits) != 1 || f.clock.waits[0] != 3*time.Second {
		t.Fatal(calls, f.clock.waits)
	}
}
func TestEngineInterruptedApplyRecoveryNeverPromotesCandidate(t *testing.T) {
	e, f := newEngine(t)
	mustCycle(t, e)
	f.revision = "B"
	ctx, cancel := context.WithCancel(context.Background())
	f.applyHook = func(context.Context, state.Instance) reconcile.Result {
		cancel()
		return reconcile.Result{Outcome: reconcile.Success}
	}
	if err := e.Reconcile(ctx); err != reconcile.ErrShutdown {
		t.Fatal(err)
	}
	st, _ := f.store.Read()
	if st.Journal == nil || st.Journal.Stage != "candidate-apply-intent" || st.Journal.Operation.Result != "completed-success" {
		t.Fatal("lost completed result")
	}
	f.applyHook = nil
	f.trace = nil
	e = restart(t, f, nil)
	mustCycle(t, e)
	st, _ = f.store.Read()
	if st.Instances[st.LastGoodID].Target.Source.Revision != "A" || st.Suppressed == nil || strings.Contains(strings.Join(f.trace, ","), "apply:B") {
		t.Fatal("continued interrupted candidate", f.trace)
	}
}
func TestEngineFaultBeforeIntentAndAfterApply(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			e, f := newEngine(t)
			inject := func() {
				state.FaultForTest(f.store, func(at string) error {
					if at == "write" {
						return syscall.ENOSPC
					}
					return nil
				})
			}
			if after {
				f.applyHook = func(context.Context, state.Instance) reconcile.Result {
					inject()
					return reconcile.Result{Outcome: reconcile.Success}
				}
			} else {
				inject()
			}
			if err := e.Reconcile(context.Background()); err != reconcile.ErrBlocked {
				t.Fatal(err)
			}
			if (!after && len(f.trace) != 0) || (after && strings.Join(f.trace, ",") != "apply:A") {
				t.Fatal("dependent action after persistence failure", f.trace)
			}
		})
	}
}
func TestEngineConcurrentCyclesSerialize(t *testing.T) {
	e, f := newEngine(t)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.Reconcile(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(f.trace) != 2 {
		t.Fatal("duplicate deployment", f.trace)
	}
}
func TestEngineStatusSafeAndOutputFailureDoesNotRollback(t *testing.T) {
	e, f := newEngine(t)
	var output bytes.Buffer
	e = restart(t, f, &output)
	mustCycle(t, e)
	decoder := json.NewDecoder(&output)
	count := 0
	for decoder.More() {
		var event map[string]any
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event["statusVersion"] != "pulltide.io/status/v1alpha1" || len(event) != 13 {
			t.Fatal("invalid status", event)
		}
		count++
	}
	if count < 4 {
		t.Fatal("missing phase transitions")
	}
	e = restart(t, f, brokenWriter{})
	if err := e.Reconcile(context.Background()); err != reconcile.ErrStatus {
		t.Fatal(err)
	}
	st, _ := f.store.Read()
	if st.Journal != nil || st.Health.State != "healthy" {
		t.Fatal("output failure changed success")
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestEngineRestartJournalMatrix(t *testing.T) {
	stages := []string{"artifact-intent", "prepared", "prior-stop-intent", "candidate-apply-intent", "candidate-verify-intent", "cleanup-intent", "restore-prepare-intent", "restore-apply-intent", "restore-verify-intent"}
	results := []string{"", "completed-success", "completed-failure", "completion-unknown"}
	for _, stage := range stages {
		for _, result := range results {
			if stage == "prepared" && result != "" {
				continue
			}
			t.Run(stage+"/"+result, func(t *testing.T) {
				e, f := newEngine(t)
				mustCycle(t, e)
				prepareJournal(t, f, stage, result)
				f.trace = nil
				f.sourceErr = errors.New("source offline")
				e = restart(t, f, nil)
				if err := e.Reconcile(context.Background()); err != reconcile.ErrCycle {
					t.Fatalf("restart: %v trace=%v", err, f.trace)
				}
				st, _ := f.store.Read()
				if st.Journal != nil || st.Active.State != "known" || st.Instances[st.LastGoodID].Target.Source.Revision != "A" {
					t.Fatalf("incorrect terminal recovery %+v", st)
				}
				mutated := stage != "artifact-intent" && stage != "prepared"
				if mutated && (st.Suppressed == nil || st.Suppressed.Source.Revision != "B") {
					t.Fatal("mutating attempt not suppressed")
				}
				if !mutated && st.Suppressed != nil {
					t.Fatal("nonmutating attempt suppressed")
				}
				if strings.Contains(strings.Join(f.trace, ","), "apply:B") {
					t.Fatal("candidate continued after restart", f.trace)
				}
			})
		}
	}
}
func prepareJournal(t *testing.T, f *fakeDriver, stage, result string) {
	t.Helper()
	app, err := application.Parse(f.doc)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := state.ConfigurationIdentity(app.Spec)
	if err != nil {
		t.Fatal(err)
	}
	target := state.Target{Source: state.Revision{Driver: "opaque-source", SourceIdentity: "repo", Revision: "B"}, ConfigurationIdentity: identity}
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(f.store.ObserveDesired(target))
	check(f.store.BeginPreparation(state.FrozenPolicy{RollbackEnabled: true, OperationTimeoutSeconds: 300}, nil))
	finish := func() {
		if result != "" {
			check(f.store.RecordResult(result, result != "completion-unknown"))
		}
	}
	if stage == "artifact-intent" {
		finish()
		return
	}
	candidate, err := f.Materialize(context.Background(), target, app.Spec, "staging/matrix", func(ctx context.Context, call func(context.Context) reconcile.Result) error {
		r := call(ctx)
		return f.store.RecordResult(string(r.Outcome), true)
	})
	check(err)
	id, err := f.store.Prepared(candidate)
	check(err)
	if stage == "prepared" {
		return
	}
	st, _ := f.store.Read()
	prior := st.Journal.PriorID
	if stage == "prior-stop-intent" {
		check(f.store.RecordIntent(stage, prior, nil))
		finish()
		return
	}
	check(f.store.RecordReplacementIntent(true))
	if stage == "candidate-apply-intent" {
		finish()
		return
	}
	check(f.store.RecordResult("completed-success", true))
	if stage == "candidate-verify-intent" {
		index := 0
		check(f.store.RecordIntent(stage, id, &index))
		finish()
		return
	}
	check(f.store.BeginRecovery(true))
	check(f.store.RecordCleanupIntent([]string{prior, id}))
	if stage == "cleanup-intent" {
		finish()
		return
	}
	check(f.store.RecordResult("completed-success", true))
	restored, err := f.store.AttachRestoration("workspaces/restoration-reservation")
	check(err)
	if stage == "restore-prepare-intent" {
		if result == "completed-success" {
			check(os.MkdirAll(filepath.Join(f.root, "workspaces/restoration-reservation"), 0700))
		}
		finish()
		return
	}
	check(os.MkdirAll(filepath.Join(f.root, "workspaces/restoration-reservation"), 0700))
	check(f.store.RecordResult("completed-success", true))
	check(f.store.RecordIntent("restore-apply-intent", restored, nil))
	if stage == "restore-apply-intent" {
		finish()
		return
	}
	check(f.store.RecordResult("completed-success", true))
	index := 0
	check(f.store.RecordIntent("restore-verify-intent", restored, &index))
	finish()
}
func TestEngineUncertainRestartRequiresEvidence(t *testing.T) {
	e, f := newEngine(t)
	mustCycle(t, e)
	prepareJournal(t, f, "candidate-apply-intent", "completion-unknown")
	f.inspectHook = func(*state.State) (reconcile.Evidence, error) {
		return reconcile.Evidence{Owned: true, Inputs: true, Storage: true, Quiescent: false}, nil
	}
	f.trace = nil
	e = restart(t, f, nil)
	if e.Reconcile(context.Background()) != reconcile.ErrBlocked || len(f.trace) != 0 {
		t.Fatal("same-boot uncertainty bypassed")
	}
	st, _ := f.store.Read()
	if st.Journal == nil {
		t.Fatal("lost uncertain evidence")
	}
	// Operator establishes quiescence, then a fresh process may recover once.
	f.inspectHook = nil
	e = restart(t, f, nil)
	mustCycle(t, e)
}
func TestEngineObservationUnknownRemainsUnknown(t *testing.T) {
	e, f := newEngine(t)
	mustCycle(t, e)
	e = restart(t, f, nil)
	f.checkHook = func(context.Context, state.Instance, application.Check) reconcile.Result {
		return reconcile.Result{Outcome: reconcile.Unknown}
	}
	if e.Reconcile(context.Background()) != reconcile.ErrBlocked {
		t.Fatal("unknown observation accepted")
	}
	st, _ := f.store.Read()
	if st.Journal == nil || st.Journal.Kind != "observation" || st.Health.State != "unknown" || st.Health.ObservedAt != nil || st.Suppressed != nil {
		t.Fatal("invalid unknown observation")
	}
	f.checkHook = nil
	f.trace = nil
	e = restart(t, f, nil)
	mustCycle(t, e)
	if strings.Join(f.trace, ",") != "health" {
		t.Fatal("observation caused runtime action", f.trace)
	}
}
func TestEngineFailedRecoveryNotRetriedUntilRestart(t *testing.T) {
	e, f := newEngine(t)
	mustCycle(t, e)
	f.revision = "B"
	f.applyHook = func(context.Context, state.Instance) reconcile.Result {
		return reconcile.Result{Outcome: reconcile.Failure}
	}
	if e.Reconcile(context.Background()) != reconcile.ErrBlocked {
		t.Fatal("failed restoration not blocked")
	}
	st, _ := f.store.Read()
	if st.Journal == nil || st.LastGoodID == "" {
		t.Fatal("lost recovery inputs")
	}
	n := len(f.trace)
	if e.Reconcile(context.Background()) != reconcile.ErrBlocked || len(f.trace) != n {
		t.Fatal("recovery retried in process")
	}
	f.applyHook = nil
	e = restart(t, f, nil)
	mustCycle(t, e)
}
func TestEngineInPlaceGroupedCleanup(t *testing.T) {
	e, f := newEngine(t)
	f.inPlace = true
	f.grouped = true
	mustCycle(t, e)
	f.revision = "B"
	f.applyHook = func(_ context.Context, i state.Instance) reconcile.Result {
		if i.Target.Source.Revision == "B" {
			return reconcile.Result{Outcome: reconcile.Failure}
		}
		return reconcile.Result{Outcome: reconcile.Success}
	}
	mustCycle(t, e)
	if f.trace[2] != "apply:B" {
		t.Fatal("core forced a prior stop", f.trace)
	}
	st, _ := f.store.Read()
	if st.Phase != "rolled-back" {
		t.Fatal("in-place restore failed")
	}
}

func TestEngineSuppressedOutcomeSurvivesStartupAndSourceOutage(t *testing.T) {
	e, f := newEngine(t)
	mustCycle(t, e)
	f.revision = "B"
	f.applyHook = func(_ context.Context, i state.Instance) reconcile.Result {
		if i.Target.Source.Revision == "B" {
			return reconcile.Result{Outcome: reconcile.Failure}
		}
		return reconcile.Result{Outcome: reconcile.Success}
	}
	mustCycle(t, e)
	e = restart(t, f, nil)
	mustCycle(t, e)
	st, _ := f.store.Read()
	if st.Phase != "rolled-back" || st.Error == nil || st.Suppressed == nil {
		t.Fatal("startup erased suppressed outcome")
	}
	f.sourceErr = errors.New("offline")
	if e.Reconcile(context.Background()) != reconcile.ErrCycle {
		t.Fatal("missing source error")
	}
	st, _ = f.store.Read()
	if st.Suppressed == nil || !st.DesiredStale {
		t.Fatal("outage erased suppression")
	}
	f.sourceErr = nil
	mustCycle(t, e)
	st, _ = f.store.Read()
	if st.Phase != "failed" || st.Error == nil {
		t.Fatal("suppressed no-op cleared outage outcome")
	}
}
func TestEngineAdmissionFailurePreservesActiveAndRetries(t *testing.T) {
	e, f := newEngine(t)
	mustCycle(t, e)
	f.revision = "B"
	n := len(f.trace)
	f.inspectHook = func(*state.State) (reconcile.Evidence, error) {
		return reconcile.Evidence{Owned: true, Inputs: true, Storage: false}, nil
	}
	if e.Reconcile(context.Background()) != reconcile.ErrCycle {
		t.Fatal("admission should fail preparation")
	}
	st, _ := f.store.Read()
	if len(f.trace) != n || st.Suppressed != nil || st.Journal != nil || st.Active.State != "known" || st.Phase != "failed" {
		t.Fatal("admission damaged active")
	}
	f.inspectHook = nil
	mustCycle(t, e)
}
func TestEngineDisabledRollbackPreservesHistoricalLastGood(t *testing.T) {
	e, f := newEngine(t)
	mustCycle(t, e)
	old, _ := f.store.Read()
	f.doc = append(f.doc, []byte("  rollback:\n    enabled: false\n")...)
	f.revision = "B"
	e = restart(t, f, nil)
	f.applyHook = func(context.Context, state.Instance) reconcile.Result {
		return reconcile.Result{Outcome: reconcile.Failure}
	}
	mustCycle(t, e)
	st, _ := f.store.Read()
	if st.Active.State != "absent" || st.LastGoodID != old.LastGoodID || st.Journal != nil || st.Suppressed == nil {
		t.Fatal("disabled policy lost history")
	}
}
func TestEngineFrozenRecoveryTimeoutAndSavedHealth(t *testing.T) {
	e, f := newEngine(t)
	mustCycle(t, e)
	prepareJournal(t, f, "candidate-apply-intent", "completed-failure")
	// Pending B used 300s. New configuration's 7s cannot replace that policy.
	f.doc = []byte(strings.Replace(string(f.doc), "  health:", "  reconcile:\n    operationTimeoutSeconds: 7\n  health:", 1))
	f.sourceErr = errors.New("offline")
	f.trace = nil
	f.applyHook = func(ctx context.Context, i state.Instance) reconcile.Result {
		d, ok := ctx.Deadline()
		left := time.Until(d)
		if !ok || left < 299*time.Second || left > 300*time.Second {
			t.Errorf("recovery timeout changed: %s", left)
		}
		if i.Saved.Reconcile.OperationTimeoutSeconds != 300 {
			t.Error("saved input replaced")
		}
		return reconcile.Result{Outcome: reconcile.Success}
	}
	e = restart(t, f, nil)
	if e.Reconcile(context.Background()) != reconcile.ErrCycle {
		t.Fatal("expected source failure after successful recovery")
	}
	st, _ := f.store.Read()
	if st.LastGoodID == "" || st.Journal != nil {
		t.Fatal("frozen recovery did not finish")
	}
}
func TestEngineHealthExactDeadlineAndClippedRetry(t *testing.T) {
	for _, exact := range []bool{true, false} {
		t.Run(map[bool]string{true: "exact-success", false: "clipped-retry"}[exact], func(t *testing.T) {
			e, f := newEngine(t)
			mustCycle(t, e)
			e = restart(t, f, nil)
			calls := 0
			f.checkHook = func(context.Context, state.Instance, application.Check) reconcile.Result {
				calls++
				if exact {
					f.clock.advance(60 * time.Second)
					return reconcile.Result{Outcome: reconcile.Success}
				}
				f.clock.advance(59 * time.Second)
				return reconcile.Result{Outcome: reconcile.Failure}
			}
			if e.Reconcile(context.Background()) != reconcile.ErrBlocked {
				t.Fatal("expired startup accepted")
			}
			st, _ := f.store.Read()
			if st.Health.State != "unhealthy" || calls != 1 {
				t.Fatal("health deadline ignored")
			}
			if !exact && (len(f.clock.waits) != 1 || f.clock.waits[0] != time.Second) {
				t.Fatal("retry not clipped", f.clock.waits)
			}
		})
	}
}
func TestEngineFixedDelayAndGracefulPollingShutdown(t *testing.T) {
	e, f := newEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.clock.onWait = func() { cancel() }
	if e.Run(ctx) != reconcile.ErrShutdown {
		t.Fatal("polling did not stop")
	}
	if len(f.clock.waits) != 1 || f.clock.waits[0] != 30*time.Second || strings.Join(f.trace, ",") != "apply:A,health" {
		t.Fatal("bad fixed delay or implicit stop", f.trace, f.clock.waits)
	}
}
func TestEnginePriorStopFailurePreventsCandidateApply(t *testing.T) {
	e, f := newEngine(t)
	mustCycle(t, e)
	f.revision = "B"
	stops := 0
	f.stopHook = func(context.Context, []state.Instance) reconcile.Result {
		stops++
		if stops == 1 {
			return reconcile.Result{Outcome: reconcile.Failure}
		}
		return reconcile.Result{Outcome: reconcile.Success}
	}
	mustCycle(t, e)
	if strings.Contains(strings.Join(f.trace, ","), "apply:B") || stops != 2 {
		t.Fatal("candidate applied after failed prior stop", f.trace)
	}
	st, _ := f.store.Read()
	if st.Suppressed == nil || st.Phase != "rolled-back" {
		t.Fatal("prior stop failure not recovered")
	}
}

func TestEngineGeneratedRecoveryFilesFollowFreshInstance(t *testing.T) {
	e, f := newEngine(t)
	f.applyHook = func(_ context.Context, i state.Instance) reconcile.Result {
		if i.Target.Source.Revision == "B" {
			return reconcile.Result{Outcome: reconcile.Failure}
		}
		if err := os.WriteFile(filepath.Join(f.root, i.Workspace, "marker"), []byte(i.ID), 0600); err != nil {
			t.Error(err)
			return reconcile.Result{Outcome: reconcile.Failure}
		}
		return reconcile.Result{Outcome: reconcile.Success, RecoveryFiles: []string{"marker"}}
	}
	f.stopHook = func(_ context.Context, instances []state.Instance) reconcile.Result {
		for _, i := range instances {
			if i.Target.Source.Revision == "A" {
				b, err := os.ReadFile(filepath.Join(f.root, i.Workspace, "marker"))
				if err != nil || string(b) != i.ID {
					t.Error("wrong stop workspace")
				}
			}
		}
		return reconcile.Result{Outcome: reconcile.Success}
	}
	mustCycle(t, e)
	old, _ := f.store.Read()
	f.revision = "B"
	mustCycle(t, e)
	st, _ := f.store.Read()
	restored := st.Instances[st.LastGoodID]
	if restored.Workspace == old.Instances[old.LastGoodID].Workspace || len(restored.RequiredRecoveryFiles) != 1 {
		t.Fatal("restoration reused workspace or lost recovery inputs")
	}
	e = restart(t, f, nil)
	mustCycle(t, e)
	f.revision = "C"
	mustCycle(t, e)
}
func TestEngineMissingArtifactBlocksBeforeNextMutation(t *testing.T) {
	e, f := newEngine(t)
	mustCycle(t, e)
	st, _ := f.store.Read()
	i := st.Instances[st.LastGoodID]
	if err := os.Remove(filepath.Join(f.root, i.Inputs[0].Path)); err != nil {
		t.Fatal(err)
	}
	f.revision = "B"
	n := len(f.trace)
	if e.Reconcile(context.Background()) != reconcile.ErrBlocked || len(f.trace) != n {
		t.Fatal("missing retained input permitted mutation")
	}
}
func TestEngineConstructorReleasesOwnershipOnMismatch(t *testing.T) {
	_, f := newEngine(t)
	doc := []byte(strings.Replace(string(f.doc), "name: example", "name: different", 1))
	if _, err := reconcile.New(doc, func(*application.Application) (*state.Store, error) { return f.store, nil }, f, f, f, f.clock, nil); err != state.ErrOwnership {
		t.Fatal(err)
	}
	owner := state.Ownership{Name: "example", Runtime: "exec", ResourceIdentity: "operator-resource"}
	s, err := state.OpenForTest(f.root, owner, false)
	if err != nil {
		t.Fatal("constructor leaked ownership lock", err)
	}
	s.Close()
}

func TestEnginePersistenceFailureEmitsTerminalDiagnostic(t *testing.T) {
	e, f := newEngine(t)
	var output bytes.Buffer
	e = restart(t, f, &output)
	f.applyHook = func(context.Context, state.Instance) reconcile.Result {
		state.FaultForTest(f.store, func(string) error { return syscall.ENOSPC })
		return reconcile.Result{Outcome: reconcile.Success}
	}
	if e.Reconcile(context.Background()) != reconcile.ErrBlocked {
		t.Fatal("persistence failure accepted")
	}
	decoder := json.NewDecoder(&output)
	var last map[string]any
	for decoder.More() {
		if err := decoder.Decode(&last); err != nil {
			t.Fatal(err)
		}
	}
	if last["phase"] != "recovery-required" || last["error"].(map[string]any)["category"] != "persistence" {
		t.Fatal("missing terminal persistence status", last)
	}
}

func TestEngineMovingDesiredDoesNotChangeFrozenCandidate(t *testing.T) {
	e, f := newEngine(t)
	f.applyHook = func(_ context.Context, i state.Instance) reconcile.Result {
		if i.Target.Source.Revision == "A" {
			f.revision = "B"
		}
		return reconcile.Result{Outcome: reconcile.Success}
	}
	mustCycle(t, e)
	st, _ := f.store.Read()
	if st.Instances[st.LastGoodID].Target.Source.Revision != "A" {
		t.Fatal("moving source rewrote candidate")
	}
	mustCycle(t, e)
	st, _ = f.store.Read()
	if st.Instances[st.LastGoodID].Target.Source.Revision != "B" {
		t.Fatal("next cycle did not resolve new revision")
	}
}
func TestEngineUnavailableStartupContextIsUnknown(t *testing.T) {
	e, f := newEngine(t)
	mustCycle(t, e)
	e = restart(t, f, nil)
	n := len(f.trace)
	f.inspectHook = func(*state.State) (reconcile.Evidence, error) {
		return reconcile.Evidence{}, errors.New("secret context")
	}
	if e.Reconcile(context.Background()) != reconcile.ErrBlocked {
		t.Fatal("unavailable startup context accepted")
	}
	st, _ := f.store.Read()
	if st.Health.State != "unknown" || st.Health.ObservedAt != nil || len(f.trace) != n {
		t.Fatal("context absence reported unhealthy or executed check")
	}
}

func TestEngineInterruptedObservationEveryResult(t *testing.T) {
	for _, result := range []string{"", "completed-success", "completed-failure", "completion-unknown"} {
		t.Run(result, func(t *testing.T) {
			e, f := newEngine(t)
			mustCycle(t, e)
			old, _ := f.store.Read()
			e = restart(t, f, nil)
			if err := f.store.BeginObservation(); err != nil {
				t.Fatal(err)
			}
			index := 0
			if err := f.store.RecordIntent("startup-verify-intent", old.Active.InstanceID, &index); err != nil {
				t.Fatal(err)
			}
			if result != "" {
				if err := f.store.RecordResult(result, result != "completion-unknown"); err != nil {
					t.Fatal(err)
				}
			}
			f.trace = nil
			e = restart(t, f, nil)
			mustCycle(t, e)
			st, _ := f.store.Read()
			if st.Journal != nil || st.LastGoodID != old.LastGoodID || st.Suppressed != nil || strings.Join(f.trace, ",") != "health" {
				t.Fatal("observation restart mutated workload", f.trace)
			}
		})
	}
}
func TestEngineShutdownDuringStartupCheckRetainsJournal(t *testing.T) {
	e, f := newEngine(t)
	mustCycle(t, e)
	e = restart(t, f, nil)
	ctx, cancel := context.WithCancel(context.Background())
	f.checkHook = func(context.Context, state.Instance, application.Check) reconcile.Result {
		cancel()
		return reconcile.Result{Outcome: reconcile.Success}
	}
	if e.Reconcile(ctx) != reconcile.ErrShutdown {
		t.Fatal("shutdown not observed")
	}
	st, _ := f.store.Read()
	if st.Journal == nil || st.Journal.Kind != "observation" || st.Journal.Operation.Result != "completed-success" {
		t.Fatal("startup evidence discarded")
	}
	f.checkHook = nil
	f.trace = nil
	e = restart(t, f, nil)
	mustCycle(t, e)
	if strings.Join(f.trace, ",") != "health" {
		t.Fatal("shutdown triggered workload mutation")
	}
}
