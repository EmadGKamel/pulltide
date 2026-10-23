//go:build linux

package state_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"pulltide/internal/application"
	"pulltide/internal/reconcile"
	"pulltide/internal/state"
)

type phaseSource struct {
	reconcile.Source
	trace           *[]string
	fail            string
	corruptRecovery bool
}

func (s *phaseSource) Stage(ctx context.Context, target state.Target, spec application.Spec, namespace string, run reconcile.Artifacts) (reconcile.Snapshot, error) {
	*s.trace = append(*s.trace, "stage")
	if s.fail == "stage" {
		return nil, errors.New("staging failure")
	}
	return s.Source.Stage(ctx, target, spec, namespace, run)
}
func (s *phaseSource) Publish(ctx context.Context, snap reconcile.Snapshot, inputs []string, run reconcile.Artifacts) (state.Instance, error) {
	*s.trace = append(*s.trace, "publish")
	if s.fail == "publish" {
		return state.Instance{}, errors.New("invalid closure")
	}
	return s.Source.Publish(ctx, snap, inputs, run)
}
func (s *phaseSource) ValidateInputs(ctx context.Context, i state.Instance, inputs []string) error {
	if i.ID == "" {
		*s.trace = append(*s.trace, "validate")
		if s.fail == "retained" {
			return errors.New("retained corruption")
		}
	}
	if s.corruptRecovery {
		return errors.New("required retained input is corrupt")
	}
	return s.Source.ValidateInputs(ctx, i, inputs)
}
func (s *phaseSource) RestoreSource(ctx context.Context, i state.Instance, run reconcile.Artifacts) error {
	*s.trace = append(*s.trace, "source-restore")
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("source restoration is missing frozen deadline")
	}
	return s.Source.RestoreSource(ctx, i, run)
}

type phaseRuntime struct {
	reconcile.Runtime
	trace *[]string
	fail  bool
}

func (r *phaseRuntime) Discover(ctx context.Context, s reconcile.Snapshot, spec application.Spec) ([]string, error) {
	*r.trace = append(*r.trace, "discover")
	if r.fail {
		return nil, errors.New("dependency cycle")
	}
	return r.Runtime.Discover(ctx, s, spec)
}
func (r *phaseRuntime) Prepare(ctx context.Context, i state.Instance, s reconcile.Snapshot, run reconcile.Artifacts) (state.Instance, error) {
	*r.trace = append(*r.trace, "prepare")
	return r.Runtime.Prepare(ctx, i, s, run)
}
func (r *phaseRuntime) Restore(ctx context.Context, i state.Instance, run reconcile.Artifacts) error {
	*r.trace = append(*r.trace, "runtime-restore")
	return r.Runtime.Restore(ctx, i, run)
}
func (r *phaseRuntime) Apply(ctx context.Context, i state.Instance) reconcile.Result {
	*r.trace = append(*r.trace, "apply")
	return r.Runtime.Apply(ctx, i)
}
func phaseEngine(t *testing.T) (*reconcile.Engine, *fakeDriver, *phaseSource, *phaseRuntime, *[]string) {
	t.Helper()
	_, f := newEngine(t)
	trace := []string{}
	source := &phaseSource{Source: f, trace: &trace}
	runtime := &phaseRuntime{Runtime: f, trace: &trace}
	e, err := reconcile.New(f.doc, func(*application.Application) (*state.Store, error) { return f.store, nil }, source, runtime, f, f.clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	return e, f, source, runtime, &trace
}
func TestEngineTwoPhaseSourcePreparationFailuresPreserveActive(t *testing.T) {
	for _, failure := range []string{"stage", "discover", "publish", "retained"} {
		t.Run(failure, func(t *testing.T) {
			e, f, source, runtime, trace := phaseEngine(t)
			mustCycle(t, e)
			if got := strings.Join(*trace, ","); got != "stage,discover,publish,prepare,validate,apply" {
				t.Fatal("preparation order", got)
			}
			old, err := f.store.Read()
			if err != nil {
				t.Fatal(err)
			}
			commands := len(f.trace)
			*trace = nil
			f.revision = "B"
			source.fail = failure
			runtime.fail = failure == "discover"
			if err = e.Reconcile(context.Background()); err != reconcile.ErrCycle {
				t.Fatal("preparation failure", err)
			}
			now, err := f.store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if now.Active != old.Active || now.LastGoodID != old.LastGoodID || now.Suppressed != nil || now.Journal != nil || len(f.trace) != commands {
				t.Fatal("preparation failure affected running deployment", f.trace)
			}
			got := strings.Join(*trace, ",")
			want := map[string]string{"stage": "stage", "discover": "stage,discover", "publish": "stage,discover,publish", "retained": "stage,discover,publish,prepare,validate"}[failure]
			if got != want {
				t.Fatal("dependent phase invoked", got, want)
			}
		})
	}
}
func TestEngineSourceRestorationPrecedesRuntimeRestoration(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "intact", true: "corrupt"}[corrupt], func(t *testing.T) {
			e, f, source, _, trace := phaseEngine(t)
			mustCycle(t, e)
			old, err := f.store.Read()
			if err != nil {
				t.Fatal(err)
			}
			*trace = nil
			f.revision = "B"
			f.applyHook = func(_ context.Context, i state.Instance) reconcile.Result {
				if i.Target.Source.Revision == "B" {
					source.corruptRecovery = corrupt
					return reconcile.Result{Outcome: reconcile.Failure}
				}
				return reconcile.Result{Outcome: reconcile.Success}
			}
			err = e.Reconcile(context.Background())
			now, readErr := f.store.Read()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if corrupt {
				if err != reconcile.ErrBlocked || now.Journal == nil || now.LastGoodID != old.LastGoodID || strings.Contains(strings.Join(*trace, ","), "restore") {
					t.Fatal("corrupt recovery invoked dependent action", err, *trace)
				}
				return
			}
			if err != nil || now.LastGoodID == old.LastGoodID || now.Instances[now.LastGoodID].Target.Source.Revision != "A" {
				t.Fatal("restoration failed", err)
			}
			if !strings.Contains(strings.Join(*trace, ","), "source-restore,runtime-restore,apply") {
				t.Fatal("restoration order", *trace)
			}
		})
	}
}
