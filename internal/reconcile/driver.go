// Package reconcile implements serialized policy over evidence-returning
// adapters. It contains no source transport or runtime execution implementation.
package reconcile

import (
	"context"
	"errors"
	"io/fs"
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
// retained-source/workspace, runtime-artifact and workload side effects run
// inside the provided callback/action. Resolution owns its disposable cache.
// Adapters must not launch background work or independently retry.
// Stage/Publish own only the supplied journaled staging namespace until Prepared.
// Artifacts executes each external preparatory step under its own durable intent.
// Adapters must call it synchronously and must not retain it or spawn callers.
type Artifacts func(context.Context, func(context.Context) Result) error

// Snapshot exposes owned copies of immutable repository-relative source content.
// Discovery cannot obtain a writable filesystem path or mutate backing buffers.
// After publication, reads are limited to the accepted closure. Entry metadata
// retains executable modes and symlink text for exact private builder copies.
type Snapshot interface {
	ReadFile(context.Context, string) ([]byte, error)
	ReadDir(context.Context, string) ([]SourceEntry, error)
}
type SourceEntry struct {
	Name       string
	Mode       fs.FileMode
	LinkTarget string // Stored target text for symlinks; empty for other entries.
}
type Source interface {
	Resolve(context.Context, application.Source) (state.Revision, error)
	Stage(context.Context, state.Target, application.Spec, string, Artifacts) (Snapshot, error)
	Publish(context.Context, Snapshot, []string, Artifacts) (state.Instance, error)
	ValidateInputs(context.Context, state.Instance, []string) error
	RestoreSource(context.Context, state.Instance, Artifacts) error
}
type Runtime interface {
	// Discover reads source only; returns the complete source-backed input roots.
	// Directories include all entries before ignore filtering. No commands/writes.
	Discover(context.Context, Snapshot, application.Spec) ([]string, error)
	// Prepare validates the complete input closure before any artifact action.
	Prepare(context.Context, state.Instance, Snapshot, Artifacts) (state.Instance, error)
	InPlace(state.Instance) bool
	// Inspect is observational: no imports, builds, pulls, cleanup or checks.
	// Inputs includes retained archives even when daemon content exists; Storage
	// is conservative admission, never a guarantee that future writes succeed.
	Inspect(context.Context, *state.State) (Evidence, error)
	// CleanupGroups partitions all possibly-live IDs into exact owned action sets.
	CleanupGroups([]string) [][]string
	Apply(context.Context, state.Instance) Result
	Stop(context.Context, []state.Instance) Result
	// Restore handles runtime artifacts only, after source restoration, in the fresh
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
