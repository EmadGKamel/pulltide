package reconcile

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"pulltide/internal/application"
	"pulltide/internal/state"
)

func (e *Engine) remaining(limit time.Duration) time.Duration {
	if !e.recoveryDeadline.IsZero() {
		if left := e.recoveryDeadline.Sub(e.clock.Now()) - 10*time.Second; left < limit {
			return left
		}
	}
	return limit
}

// bounded never interprets a deadline, cancellation, panic, or returned invalid
// outcome as proof of termination. Late results cannot advance core state.
func bounded(ctx context.Context, limit time.Duration, call func(context.Context) Result) Result {
	if limit <= 0 {
		return Result{Outcome: Failure}
	}
	operation, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
	defer cancel()
	done := make(chan Result, 1)
	go func() {
		result := Result{Outcome: Unknown}
		defer func() { _ = recover(); done <- result }()
		result = call(operation)
		if result.Outcome != Success && result.Outcome != Failure {
			result = Result{Outcome: Unknown}
		}
	}()
	select {
	case result := <-done:
		if result.Outcome == Success && operation.Err() != nil {
			result.Outcome = Failure
		}
		return result
	case <-operation.Done():
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case result := <-done:
			if result.Outcome == Success {
				result.Outcome = Failure
			}
			return result
		case <-timer.C:
			return Result{Outcome: Unknown}
		}
	}
}
func (e *Engine) resolve(ctx context.Context) (state.Revision, error) {
	var revision state.Revision
	result := bounded(ctx, seconds(e.app.Spec.Reconcile.OperationTimeoutSeconds), func(c context.Context) Result {
		value, err := e.source.Resolve(c, e.app.Spec.Source)
		if err != nil {
			return Result{Outcome: Failure}
		}
		revision = value
		return Result{Outcome: Success}
	})
	if result.Outcome == Unknown {
		return state.Revision{}, e.fail("uncertain-operation", "resolve", true)
	}
	if result.Outcome != Success {
		return state.Revision{}, ErrCycle
	}
	return revision, nil
}
func (e *Engine) guard(ctx context.Context, quiescence bool) (Evidence, error) {
	if e.blocked {
		return Evidence{}, ErrBlocked
	}
	if ctx.Err() != nil {
		return Evidence{}, ErrShutdown
	}
	st, err := e.store.Read()
	if err != nil {
		return Evidence{}, e.persistence(err)
	}
	timeout := e.app.Spec.Reconcile.OperationTimeoutSeconds
	if st.Journal != nil && st.Journal.Policy != nil {
		timeout = st.Journal.Policy.OperationTimeoutSeconds
	}
	var evidence Evidence
	result := bounded(ctx, e.remaining(seconds(timeout)), func(c context.Context) Result {
		for _, instance := range st.Instances {
			if e.source.ValidateInputs(c, instance, nil) != nil {
				return Result{Outcome: Failure}
			}
		}
		value, err := e.runtime.Inspect(c, st)
		if err != nil {
			return Result{Outcome: Failure}
		}
		evidence = value
		return Result{Outcome: Success}
	})
	// The worker may outlive its bound. Only publish evidence through the result
	// channel; bounded's successful receive establishes the happens-before edge.
	preparing := !quiescence && ((st.Journal == nil && st.Health.State != "unknown") || (st.Journal != nil && (st.Journal.Kind == "preparation" || st.Journal.Stage == "prepared")))
	if result.Outcome != Success {
		if preparing && result.Outcome == Failure {
			return Evidence{}, e.fail("preparation", "prepare", false)
		}
		return Evidence{}, e.fail("recovery", "prepare", true)
	}
	if !evidence.Owned {
		return Evidence{}, e.fail("ownership", "prepare", true)
	}
	if !evidence.Inputs || !evidence.Storage || evidence.MissingImages < 0 {
		if preparing {
			return Evidence{}, e.fail("preparation", "prepare", false)
		}
		return Evidence{}, e.fail("recovery", "prepare", true)
	}
	if quiescence && !evidence.Quiescent && !conclusive(st) {
		// Changed local host boot is sufficient only for the earlier local operation.
		if !newBoot(st) {
			return Evidence{}, e.fail("uncertain-operation", "prepare", true)
		}
	}
	return evidence, nil
}
func (e *Engine) action(ctx context.Context, stage, id string, index *int, inPlace bool, cleanup []string, timeout int, call func(context.Context) Result) error {
	if e.blocked {
		return ErrBlocked
	}
	if ctx.Err() != nil {
		return ErrShutdown
	}
	if e.remaining(seconds(timeout)) <= 0 {
		return e.fail("recovery", "prepare", true)
	}
	if _, err := e.guard(ctx, false); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ErrShutdown
	}
	var err error
	switch {
	case stage == "candidate-apply-intent":
		err = e.store.RecordReplacementIntent(inPlace)
	case stage == "cleanup-intent":
		err = e.store.RecordCleanupIntent(cleanup)
	default:
		err = e.store.RecordIntent(stage, id, index)
	}
	if err != nil {
		return e.persistence(err)
	}
	e.emit()
	return e.invoke(ctx, e.remaining(seconds(timeout)), call)
}
func (e *Engine) invoke(ctx context.Context, limit time.Duration, call func(context.Context) Result) error {
	result := Result{Outcome: Failure}
	if ctx.Err() == nil {
		result = bounded(ctx, limit, call)
	}
	stopped := result.Outcome != Unknown
	files := result.RecoveryFiles
	if result.Outcome != Success {
		files = nil
	}
	if err := e.store.RecordCheckpoint(string(result.Outcome), stopped, files); err != nil {
		return e.persistence(err)
	}
	e.emit()
	if result.Outcome == Unknown {
		e.blocked = true
		return ErrBlocked
	}
	if ctx.Err() != nil {
		return ErrShutdown
	}
	if result.Outcome == Failure {
		return ErrCycle
	}
	return nil
}
func (e *Engine) artifacts(stage, id string, timeout int) (Artifacts, func()) {
	var mu sync.Mutex
	live := true
	run := func(ctx context.Context, call func(context.Context) Result) error {
		mu.Lock()
		defer mu.Unlock()
		if !live || call == nil {
			return ErrBlocked
		}
		if ctx.Err() != nil {
			return ErrShutdown
		}
		if _, err := e.guard(ctx, false); err != nil {
			return err
		}
		st, err := e.store.Read()
		if err != nil {
			return e.persistence(err)
		}
		op := st.Journal.Operation
		// BeginPreparation/AttachRestoration already persisted the first intent.
		if op == nil || op.Result != "" || op.Stage != stage || op.OperandID != id {
			if err = e.store.RecordIntent(stage, id, nil); err != nil {
				return e.persistence(err)
			}
		}
		e.emit()
		return e.invoke(ctx, e.remaining(seconds(timeout)), call)
	}
	return run, func() { mu.Lock(); live = false; mu.Unlock() }
}
func (e *Engine) verify(ctx context.Context, instance state.Instance, stage string) error {
	policy := instance.Saved.Health
	deadline := e.clock.Now().Add(seconds(policy.TimeoutSeconds))
	if !e.recoveryDeadline.IsZero() && deadline.After(e.recoveryDeadline.Add(-10*time.Second)) {
		deadline = e.recoveryDeadline.Add(-10 * time.Second)
	}
	for {
		for index, check := range policy.Checks {
			if ctx.Err() != nil {
				return ErrShutdown
			}
			// Persist even a check that expires before launch: core knows it did not
			// invoke that action, so its conclusive failure needs no driver stop proof.
			if err := e.store.RecordIntent(stage, instance.ID, &index); err != nil {
				return e.persistence(err)
			}
			e.emit()
			left := deadline.Sub(e.clock.Now())
			if left > 0 {
				if _, err := e.guard(ctx, false); err != nil {
					return err
				}
				left = deadline.Sub(e.clock.Now())
			}
			var err error
			if left <= 0 {
				err = e.invoke(ctx, 0, nil)
			} else {
				limit := seconds(check.TimeoutSeconds)
				if left < limit {
					limit = left
				}
				err = e.invoke(ctx, limit, func(c context.Context) Result { return e.health.Check(c, instance, check) })
			}
			if err == ErrBlocked || e.blocked {
				return ErrBlocked
			}
			if err == ErrShutdown {
				return err
			}
			if err != nil || !e.clock.Now().Before(deadline) {
				left = deadline.Sub(e.clock.Now())
				if left <= 0 {
					if err = e.store.RecordHealthWindow(false, true); err != nil {
						return e.persistence(err)
					}
					return ErrCycle
				}
				delay := seconds(policy.IntervalSeconds)
				if left < delay {
					delay = left
				}
				if err = e.clock.Wait(ctx, delay); err != nil {
					return ErrShutdown
				}
				break
			}
			if index == len(policy.Checks)-1 {
				if err = e.store.RecordHealthWindow(true, true); err != nil {
					return e.persistence(err)
				}
				return nil
			}
		}
	}
}

func conclusive(st *state.State) bool {
	if st.Journal == nil || st.Journal.Stage == "prepared" {
		return true
	}
	op := st.Journal.Operation
	// A just-created observation window contains no invoked check yet.
	if op == nil {
		return st.Journal.Kind == "observation"
	}
	return op.Result == "completed-success" || op.Result == "completed-failure"
}

func cloneSpec(spec application.Spec) application.Spec {
	// Only validated typed models enter this helper; JSON is used for an owned
	// copy of all nested maps/slices, preserving significant array order.
	data, _ := json.Marshal(spec)
	var copy application.Spec
	_ = json.Unmarshal(data, &copy)
	return copy
}
