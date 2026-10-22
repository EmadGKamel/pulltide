// Package reconcile implements serialized policy over evidence-returning
// adapters. It contains no source transport or runtime execution implementation.
package reconcile

import (
	"context"
	"errors"
	"time"

	"pulltide/internal/application"
	"pulltide/internal/state"
)

type Outcome string

const (
	Success Outcome = "completed-success"
	Failure Outcome = "completed-failure"
	Unknown Outcome = "completion-unknown"
)

// Completed outcomes assert termination of all operation-side effects, including
// asynchronous work. Process exit alone is not that assertion.
type Result struct {
	Outcome       Outcome
	RecoveryFiles []string
}

// Adapter orchestration and plan methods are synchronous and observational; all
// filesystem/artifact and workload side effects must run inside the provided
// callback/action. They must not launch background work or independently retry.
// Materialize owns only the supplied journaled staging namespace until Prepared.
// Artifacts executes each external preparatory step under its own durable intent.
// Adapters must call it synchronously and must not retain it or spawn callers.
type Artifacts func(context.Context, func(context.Context) Result) error

type Source interface {
	Resolve(context.Context, application.Source) (state.Revision, error)
	Materialize(context.Context, state.Target, application.Spec, string, Artifacts) (state.Instance, error)
}
type Runtime interface {
	// Prepare validates the complete input closure before any artifact action.
	Prepare(context.Context, state.Instance, Artifacts) (state.Instance, error)
	InPlace(state.Instance) bool
	// Inspect is observational: no imports, builds, pulls, cleanup or checks.
	// Inputs includes retained archives even when daemon content exists; Storage
	// is conservative admission, never a guarantee that future writes succeed.
	Inspect(context.Context, *state.State) (Evidence, error)
	// CleanupGroups partitions all possibly-live IDs into exact owned action sets.
	CleanupGroups([]string) [][]string
	Apply(context.Context, state.Instance) Result
	Stop(context.Context, []state.Instance) Result
	// Restore uses saved local inputs only, into the already reserved fresh
	// workspace. Each creation/import uses Artifacts; no rebuild/network fallback.
	Restore(context.Context, state.Instance, Artifacts) error
}
type Health interface {
	Check(context.Context, state.Instance, application.Check) Result
}
type Evidence struct {
	Owned, Inputs, Storage, Quiescent, Absent bool
	MissingImages                             int
}

type Clock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) Wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

var (
	ErrBlocked  = errors.New("reconciliation requires operator repair and restart")
	ErrCycle    = errors.New("reconciliation cycle failed")
	ErrShutdown = errors.New("reconciliation stopped")
	ErrStatus   = errors.New("status emission failed")
)
