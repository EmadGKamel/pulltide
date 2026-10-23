package reconcile

import (
	"context"
	"io"
	"sort"
	"sync"
	"time"

	"pulltide/internal/application"
	"pulltide/internal/state"
)

// Engine owns one Store for its complete lifetime. The acquire callback is only
// invoked after validating the document; it must acquire canonical ownership.
// No other engine or caller may write that Store while the engine owns it.
type Engine struct {
	mu               sync.Mutex
	app              *application.Application
	store            *state.Store
	source           Source
	runtime          Runtime
	health           Health
	clock            Clock
	output           io.Writer
	started, blocked bool
	statusErr        bool
	recoveryDeadline time.Time
	lastStatus       *state.State
	terminal         *state.Diagnostic
}

func New(document []byte, acquire func(*application.Application) (*state.Store, error), source Source, runtime Runtime, health Health, clock Clock, output io.Writer) (*Engine, error) {
	app, err := application.Parse(document)
	if err != nil {
		return nil, err
	}
	if acquire == nil || source == nil || runtime == nil || health == nil {
		return nil, ErrBlocked
	}
	s, err := acquire(app)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, ErrBlocked
	}
	st, err := s.Read()
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	if st.Ownership.Name != app.Metadata.Name || st.Ownership.Runtime != app.Spec.Runtime.Type {
		_ = s.Close()
		return nil, state.ErrOwnership
	}
	if source == nil || runtime == nil || health == nil {
		return nil, ErrBlocked
	}
	if clock == nil {
		clock = realClock{}
	}
	if output == nil {
		output = io.Discard
	}
	return &Engine{app: app, store: s, source: source, runtime: runtime, health: health, clock: clock, output: output}, nil
}
func (e *Engine) Close() error { e.mu.Lock(); defer e.mu.Unlock(); return e.store.Close() }
func (e *Engine) Run(ctx context.Context) error {
	for {
		err := e.Reconcile(ctx)
		if err != nil && err != ErrCycle {
			return err
		}
		if err = e.clock.Wait(ctx, seconds(e.app.Spec.Reconcile.IntervalSeconds)); err != nil {
			return ErrShutdown
		}
	}
}
func (e *Engine) Reconcile(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.blocked {
		return ErrBlocked
	}
	if ctx.Err() != nil {
		return ErrShutdown
	}
	if !e.started {
		e.started = true
		e.emit()
		if err := e.startup(ctx); err != nil {
			return e.finish(err)
		}
	}
	if ctx.Err() != nil {
		return e.finish(ErrShutdown)
	}
	rev, err := e.resolve(ctx)
	if err != nil {
		if e.blocked {
			return e.finish(ErrBlocked)
		}
		if ctx.Err() != nil {
			return e.finish(ErrShutdown)
		}
		if err = e.store.MarkDesiredStale(); err != nil {
			return e.finish(e.persistence(err))
		}
		return e.finish(e.fail("source", "resolve", false))
	}
	identity, err := state.ConfigurationIdentity(e.app.Spec)
	if err != nil {
		return e.finish(e.fail("validation", "startup", true))
	}
	target := state.Target{Source: rev, ConfigurationIdentity: identity}
	if err = e.store.ObserveDesired(target); err != nil {
		return e.finish(e.persistence(err))
	}
	st, err := e.store.Read()
	if err != nil {
		return e.finish(e.persistence(err))
	}
	if st.Suppressed != nil && *st.Suppressed == target {
		return e.finish(nil)
	}
	if st.Active.State == "known" && st.Health.State == "healthy" && st.Instances[st.Active.InstanceID].Target == target {
		if err = e.store.RecordHealthyNoop(); err != nil {
			return e.finish(e.persistence(err))
		}
		return e.finish(nil)
	}
	if _, err = e.guard(ctx, false); err != nil {
		return e.finish(err)
	}
	policy := state.FrozenPolicy{RollbackEnabled: e.app.Spec.Rollback.Enabled, OperationTimeoutSeconds: e.app.Spec.Reconcile.OperationTimeoutSeconds}
	// The staging namespace is owned and journaled before adapter creation.
	staging, err := state.NewID()
	if err != nil {
		return e.finish(e.persistence(err))
	}
	if err = e.store.BeginPreparation(policy, []string{"staging/" + staging}); err != nil {
		return e.finish(e.persistence(err))
	}
	e.emit()
	run, seal := e.artifacts("artifact-intent", "", policy.OperationTimeoutSeconds)
	snapshot, err := e.source.Stage(ctx, target, cloneSpec(e.app.Spec), "staging/"+staging, run)
	var candidate state.Instance
	var inputs []string
	if err == nil {
		result := bounded(ctx, seconds(policy.OperationTimeoutSeconds), func(c context.Context) Result {
			var discoverErr error
			inputs, discoverErr = e.runtime.Discover(c, snapshot, cloneSpec(e.app.Spec))
			if discoverErr != nil {
				return Result{Outcome: Failure}
			}
			return Result{Outcome: Success}
		})
		if result.Outcome != Success {
			err = ErrCycle
		}
		if result.Outcome == Unknown {
			e.blocked = true
		}
	}
	if err == nil {
		candidate, err = e.source.Publish(ctx, snapshot, inputs, run)
	}
	if err == nil {
		candidate, err = e.runtime.Prepare(ctx, candidate, snapshot, run)
	}
	if err == nil {
		result := bounded(ctx, seconds(policy.OperationTimeoutSeconds), func(c context.Context) Result {
			if e.source.ValidateInputs(c, candidate, nil) != nil {
				return Result{Outcome: Failure}
			}
			return Result{Outcome: Success}
		})
		if result.Outcome != Success {
			err = ErrCycle
		}
		if result.Outcome == Unknown {
			e.blocked = true
		}
	}
	seal()
	if e.blocked {
		return e.finish(ErrBlocked)
	}
	if ctx.Err() != nil {
		return e.finish(ErrShutdown)
	}
	if err != nil {
		if e.blocked {
			return e.finish(ErrBlocked)
		}
		st, _ = e.store.Read()
		if st == nil || st.Journal == nil {
			return e.finish(e.fail("preparation", "prepare", true))
		}
		op := st.Journal.Operation
		if op != nil && op.Result == "" {
			// No callback ran: no external artifact operation was invoked.
			if err = e.store.RecordResult("completed-failure", true); err != nil {
				return e.finish(e.persistence(err))
			}
			st, _ = e.store.Read()
			op = st.Journal.Operation
		}
		if op == nil || (op.Result != "completed-success" && op.Result != "completed-failure") {
			return e.finish(e.fail("uncertain-operation", "prepare", true))
		}
		if err = e.store.AbandonUnmutated(true); err != nil {
			return e.finish(e.persistence(err))
		}
		return e.finish(e.fail("preparation", "prepare", false))
	}
	// Saved policy and target are core-owned, not mutable adapter output.
	candidate.Target = target
	candidate.Saved = cloneSpec(e.app.Spec)
	id, err := e.store.Prepared(candidate)
	if err != nil {
		return e.finish(e.persistence(err))
	}
	st, _ = e.store.Read()
	candidate = st.Instances[id]
	if !e.runtime.InPlace(candidate) && st.Journal.PriorID != "" {
		prior := st.Instances[st.Journal.PriorID]
		if err = e.action(ctx, "prior-stop-intent", prior.ID, nil, false, nil, policy.OperationTimeoutSeconds, func(c context.Context) Result { return e.runtime.Stop(c, []state.Instance{prior}) }); err != nil {
			return e.finish(e.afterMutation(ctx, err))
		}
	}
	if err = e.action(ctx, "candidate-apply-intent", id, nil, e.runtime.InPlace(candidate), nil, policy.OperationTimeoutSeconds, func(c context.Context) Result { return e.runtime.Apply(c, candidate) }); err != nil {
		return e.finish(e.afterMutation(ctx, err))
	}
	if err = e.verify(ctx, candidate, "candidate-verify-intent"); err != nil {
		return e.finish(e.afterMutation(ctx, err))
	}
	// Completed verification may still commit on shutdown; no new external action.
	if err = e.store.CommitVerified(); err != nil {
		return e.finish(e.persistence(err))
	}
	return e.finish(nil)
}
func (e *Engine) startup(ctx context.Context) error {
	st, err := e.store.Read()
	if err != nil {
		return e.persistence(err)
	}
	inspection, err := state.InspectRecovery(st)
	if err != nil {
		return e.persistence(err)
	}
	if st.Journal != nil {
		if st.Journal.Kind == "deployment" && st.Journal.Stage != "prepared" {
			return e.recover(ctx)
		}
		if _, err = e.guard(ctx, inspection.RequiresQuiescence); err != nil {
			return err
		}
		switch st.Journal.Kind {
		case "preparation":
			err = e.store.AbandonUnmutated(true)
		case "observation":
			err = e.store.AbandonObservation(true)
		case "deployment":
			if st.Journal.Stage == "prepared" {
				err = e.store.AbandonUnmutated(true)
			} else {
				return e.recover(ctx)
			}
		}
		if err != nil {
			return e.persistence(err)
		}
	}
	st, err = e.store.Read()
	if err != nil {
		return e.persistence(err)
	}
	if st.Active.State == "unknown" {
		return e.fail("recovery", "startup", true)
	}
	if st.Active.State == "absent" {
		return nil
	}
	if _, err = e.guard(ctx, false); err != nil {
		return err
	}
	if err = e.store.BeginObservation(); err != nil {
		return e.persistence(err)
	}
	e.emit()
	instance := st.Instances[st.Active.InstanceID]
	err = e.verify(ctx, instance, "startup-verify-intent")
	if err == ErrCycle {
		if commitErr := e.store.CommitObservation(false); commitErr != nil {
			return e.persistence(commitErr)
		}
		e.blocked = true
		return ErrBlocked
	}
	if err != nil {
		return err
	}
	if err = e.store.CommitObservation(true); err != nil {
		return e.persistence(err)
	}
	e.emit()
	return nil
}
func (e *Engine) afterMutation(ctx context.Context, err error) error {
	st, readErr := e.store.Read()
	if readErr != nil {
		return e.persistence(readErr)
	}
	if !e.blocked && st.Journal != nil && st.Journal.Stage == "prepared" && err != ErrShutdown {
		if abandonErr := e.store.AbandonUnmutated(true); abandonErr != nil {
			return e.persistence(abandonErr)
		}
		return e.fail("preparation", "prepare", false)
	}
	if err == ErrBlocked || e.blocked {
		return err
	}
	if ctx.Err() != nil || err == ErrShutdown {
		return ErrShutdown
	}
	return e.recover(ctx)
}
func (e *Engine) recover(ctx context.Context) error {
	if ctx.Err() != nil {
		return ErrShutdown
	}
	st, err := e.store.Read()
	if err != nil {
		return e.persistence(err)
	}
	recoveryStart := e.clock.Now()
	evidence, err := e.guard(ctx, true)
	if err != nil {
		return err
	}
	if st.Error == nil {
		if err = e.store.RecordCycleFailure(state.Diagnostic{Category: "recovery", Stage: "startup"}, true); err != nil {
			return e.persistence(err)
		}
	}
	policy := *st.Journal.Policy
	restoring := policy.RollbackEnabled && st.LastGoodID != ""
	budget := 5 * (seconds(policy.OperationTimeoutSeconds) + 10*time.Second)
	if restoring {
		unit := seconds(policy.OperationTimeoutSeconds) + 10*time.Second
		health := seconds(st.Instances[st.LastGoodID].Saved.Health.TimeoutSeconds) + 10*time.Second
		maxImages := int64((time.Duration(1<<63-1)-health)/unit) - 7
		if int64(evidence.MissingImages) > maxImages {
			return e.fail("recovery", "prepare", true)
		}
		budget = time.Duration(evidence.MissingImages+7)*unit + health
	}
	e.recoveryDeadline = recoveryStart.Add(budget)
	defer func() { e.recoveryDeadline = time.Time{} }()
	if err = e.store.BeginRecovery(true); err != nil {
		return e.persistence(err)
	}
	e.emit()
	// Replay an interrupted exact cleanup scope before planning remaining work.
	if op := st.Journal.Operation; op != nil && op.Stage == "cleanup-intent" && op.Result != "completed-success" {
		ids := op.CleanupIDs
		if len(ids) == 0 {
			ids = []string{op.OperandID}
		}
		if err = e.cleanup(ctx, ids, policy.OperationTimeoutSeconds); err != nil {
			return e.recoveryFailure(err)
		}
	}
	st, err = e.store.Read()
	if err != nil {
		return e.persistence(err)
	}
	ids := []string{}
	for id, live := range st.Journal.MayBeLive {
		if live {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	groups := e.runtime.CleanupGroups(append([]string(nil), ids...))
	if !partition(ids, groups) {
		return e.fail("recovery", "cleanup", true)
	}
	for _, group := range groups {
		if err = e.cleanup(ctx, group, policy.OperationTimeoutSeconds); err != nil {
			return e.recoveryFailure(err)
		}
	}
	if !restoring {
		evidence, err = e.guard(ctx, false)
		if err != nil {
			return err
		}
		if !evidence.Absent {
			return e.fail("recovery", "cleanup", true)
		}
		if err = e.store.CommitAbsent(true); err != nil {
			return e.persistence(err)
		}
		e.emit()
		return nil
	}
	workspace, err := state.NewID()
	if err != nil {
		return e.persistence(err)
	}
	if _, err = e.guard(ctx, false); err != nil {
		return err
	}
	id, err := e.store.AttachRestoration("workspaces/" + workspace)
	if err != nil {
		return e.persistence(err)
	}
	e.emit()
	st, _ = e.store.Read()
	instance := st.Instances[id]
	run, seal := e.artifacts("restore-prepare-intent", id, policy.OperationTimeoutSeconds)
	sourceContext, cancelSource := context.WithTimeout(ctx, e.remaining(seconds(policy.OperationTimeoutSeconds)))
	err = e.source.RestoreSource(sourceContext, instance, run)
	cancelSource()
	if err == nil {
		err = e.runtime.Restore(ctx, instance, run)
	}
	seal()
	if err != nil {
		return e.recoveryFailure(err)
	}
	if err = e.action(ctx, "restore-apply-intent", id, nil, false, nil, policy.OperationTimeoutSeconds, func(c context.Context) Result { return e.runtime.Apply(c, instance) }); err != nil {
		return e.recoveryFailure(err)
	}
	if err = e.verify(ctx, instance, "restore-verify-intent"); err != nil {
		return e.recoveryFailure(err)
	}
	if err = e.store.CommitVerified(); err != nil {
		return e.persistence(err)
	}
	e.emit()
	return nil
}
func (e *Engine) cleanup(ctx context.Context, ids []string, timeout int) error {
	st, err := e.store.Read()
	if err != nil {
		return e.persistence(err)
	}
	instances := make([]state.Instance, 0, len(ids))
	for _, id := range ids {
		instances = append(instances, st.Instances[id])
	}
	return e.action(ctx, "cleanup-intent", ids[0], nil, false, ids, timeout, func(c context.Context) Result { return e.runtime.Stop(c, instances) })
}
func partition(ids []string, groups [][]string) bool {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	for _, group := range groups {
		if len(group) == 0 {
			return false
		}
		for _, id := range group {
			if !set[id] {
				return false
			}
			delete(set, id)
		}
	}
	return len(set) == 0
}
func seconds(n int) time.Duration { return time.Duration(n) * time.Second }
func (e *Engine) recoveryFailure(err error) error {
	if err == ErrShutdown {
		return err
	}
	if e.blocked {
		return err
	}
	return e.fail("recovery", "restore", true)
}
func (e *Engine) persistence(_ error) error {
	e.terminal = &state.Diagnostic{Category: "persistence", Stage: "persist"}
	e.blocked = true
	return ErrBlocked
}
func (e *Engine) fail(category, stage string, blocked bool) error {
	if err := e.store.RecordCycleFailure(state.Diagnostic{Category: category, Stage: stage}, blocked); err != nil {
		return e.persistence(err)
	}
	if blocked {
		e.blocked = true
		return ErrBlocked
	}
	return ErrCycle
}
func (e *Engine) finish(err error) error {
	e.emit()
	if err == nil && e.statusErr {
		return ErrStatus
	}
	return err
}
