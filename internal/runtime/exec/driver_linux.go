//go:build linux

// Package execruntime implements the restricted foreground Exec runtime profile.
// Core owns state writes, health windows, rollback policy and recovery sequencing.
package execruntime

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"pulltide/internal/application"
	"pulltide/internal/reconcile"
	"pulltide/internal/state"
)

type Driver struct {
	root         string
	read         func() (*state.State, error)
	source       reconcile.Source
	owner        state.Ownership
	installation string
	profile      bool
	process      supervisor
	fault        func(string) error
	command      sync.Mutex
	uncertain    atomic.Bool
}

var _ reconcile.Runtime = (*Driver)(nil)
var _ reconcile.Health = (*Driver)(nil) // Command checks only; HTTP is a future adapter.

// New requires the canonical Store ownership handle and explicit operator
// attestation of the restricted foreground/local-only command profile. It never
// enrolls/adopts resources or assumes arbitrary commands satisfy that contract.
func New(store *state.Store, source reconcile.Source, profileConfirmed bool) (*Driver, error) {
	if store == nil || source == nil || !profileConfirmed {
		return nil, ErrRuntime
	}
	st, err := store.Read()
	if err != nil || st.Ownership.Runtime != "exec" {
		return nil, ErrRuntime
	}
	return &Driver{root: store.Directory(), read: store.Read, source: source, owner: st.Ownership, installation: st.InstallationID, profile: true}, nil
}
func (d *Driver) current() (*state.State, error) {
	if !d.profile || d.read == nil || d.source == nil {
		return nil, ErrRuntime
	}
	st, err := d.read()
	if err != nil || st == nil || st.Ownership != d.owner || st.InstallationID != d.installation || st.Ownership.Runtime != "exec" {
		return nil, ErrRuntime
	}
	return st, nil
}
func (d *Driver) workspace(i state.Instance) (string, error) {
	if !relative(i.Workspace) {
		return "", ErrRuntime
	}
	dir, err := openBeneath(d.root, i.Workspace, true)
	if err != nil {
		return "", err
	}
	dir.Close()
	return filepath.Join(d.root, i.Workspace), nil
}
func execSpec(spec application.Spec) bool {
	return spec.Runtime.Type == "exec" && spec.Runtime.Exec != nil && len(spec.Runtime.Exec.Apply) > 0 && len(spec.Runtime.Exec.Stop) > 0
}
func (d *Driver) Discover(ctx context.Context, s reconcile.Snapshot, spec application.Spec) ([]string, error) {
	if !execSpec(spec) || s == nil || ctx.Err() != nil {
		return nil, ErrRuntime
	}
	entries, err := s.ReadDir(ctx, spec.Source.Path)
	if err != nil {
		return nil, ErrRuntime
	}
	for _, en := range entries {
		if en.Name == inventoryName {
			return nil, ErrRuntime
		}
	}
	roots := []string{spec.Source.Path}
	commands := [][]string{spec.Runtime.Exec.Apply, spec.Runtime.Exec.Stop}
	for _, check := range spec.Health.Checks {
		if check.Type == "command" {
			commands = append(commands, check.Command)
		}
	}
	effective := environmentMap(spec.Runtime.Exec.Env)
	for _, argv := range commands {
		if len(argv) == 0 || argv[0] == "" {
			return nil, ErrRuntime
		}
		candidates := []string{}
		if strings.Contains(argv[0], "/") && !filepath.IsAbs(argv[0]) {
			candidates = append(candidates, argv[0])
		}
		if !strings.Contains(argv[0], "/") {
			for _, prefix := range filepath.SplitList(effective["PATH"]) {
				if !filepath.IsAbs(prefix) {
					candidate := argv[0]
					if prefix != "" {
						candidate = prefix + "/" + argv[0]
					}
					candidates = append(candidates, candidate)
				}
			}
		}
		for _, candidate := range candidates {
			candidate = spec.Source.Path + "/" + candidate
			if _, err := s.ReadFile(ctx, candidate); err == nil {
				roots = append(roots, candidate)
			}
		}
	}
	if ctx.Err() != nil {
		return nil, ErrRuntime
	}
	return roots, nil
}
func (d *Driver) Prepare(ctx context.Context, i state.Instance, s reconcile.Snapshot, _ reconcile.Artifacts) (state.Instance, error) {
	if !execSpec(i.Saved) || s == nil {
		return state.Instance{}, ErrRuntime
	}
	if _, err := d.current(); err != nil {
		return state.Instance{}, err
	}
	if err := d.source.ValidateInputs(ctx, i, nil); err != nil {
		return state.Instance{}, ErrRuntime
	}
	workspace, err := d.workspace(i)
	if err != nil {
		return state.Instance{}, err
	}
	cwd, _, err := workingDirectory(workspace, i.Saved.Source.Path)
	if err != nil {
		return state.Instance{}, err
	}
	if syscall.Access(cwd, 3) != nil {
		return state.Instance{}, ErrRuntime
	}
	for _, argv := range [][]string{i.Saved.Runtime.Exec.Apply, i.Saved.Runtime.Exec.Stop} {
		if _, err = executable(d.root, workspace, cwd, argv, environmentMap(i.Saved.Runtime.Exec.Env)); err != nil {
			return state.Instance{}, err
		}
	}
	if _, err = os.Lstat(filepath.Join(cwd, inventoryName)); !os.IsNotExist(err) {
		return state.Instance{}, ErrRuntime
	}
	if ctx.Err() != nil {
		return state.Instance{}, ErrRuntime
	}
	return i, nil
}
func (*Driver) InPlace(state.Instance) bool { return false }
func (*Driver) CleanupGroups(ids []string) [][]string {
	groups := make([][]string, 0, len(ids))
	for _, id := range ids {
		groups = append(groups, []string{id})
	}
	return groups
}
func partial(st *state.State, id string, current bool) bool {
	if st.Journal == nil || st.Journal.Kind != "deployment" {
		return false
	}
	operations := append([]state.Operation{}, st.Journal.Completed...)
	if current && st.Journal.Operation != nil {
		operations = append(operations, *st.Journal.Operation)
	}
	for _, op := range operations {
		if op.Result == string(reconcile.Success) {
			continue
		}
		switch op.Stage {
		case "prior-stop-intent", "candidate-apply-intent", "restore-apply-intent", "cleanup-intent":
		default:
			continue
		}
		if op.OperandID == id {
			return true
		}
		for _, cleanup := range op.CleanupIDs {
			if cleanup == id {
				return true
			}
		}
	}
	return false
}
func (d *Driver) validateInstance(ctx context.Context, st *state.State, i state.Instance, allowCurrentPartial bool) error {
	if !execSpec(i.Saved) || d.source.ValidateInputs(ctx, i, nil) != nil {
		return ErrRuntime
	}
	for _, ref := range i.Inputs {
		if ctx.Err() != nil {
			return ErrRuntime
		}
		f, e := openBeneath(d.root, ref.Path, false)
		if e != nil {
			return e
		}
		hash := sha256.New()
		_, e = copyContext(ctx, hash, f)
		ce := f.Close()
		if e != nil || ce != nil || fmt.Sprintf("%x", hash.Sum(nil)) != ref.SHA256 {
			return ErrRuntime
		}
	}
	workspace, err := d.workspace(i)
	reserved := st.Journal != nil && st.Journal.RestorationID == i.ID && st.Journal.Stage == "restore-prepare-intent" && st.Journal.Operation != nil && st.Journal.Operation.Result != string(reconcile.Success)
	if err != nil {
		if reserved {
			p := filepath.Join(d.root, i.Workspace)
			if relative(i.Workspace) {
				if _, e := os.Lstat(p); os.IsNotExist(e) {
					return nil
				}
			}
		}
		return err
	}
	required := len(i.RequiredRecoveryFiles) > 0 || st.Active.State == "known" && st.Active.InstanceID == i.ID
	if st.Journal != nil && st.Journal.MayBeLive[i.ID] {
		ops := append([]state.Operation{}, st.Journal.Completed...)
		if st.Journal.Operation != nil {
			ops = append(ops, *st.Journal.Operation)
		}
		for _, op := range ops {
			if op.OperandID == i.ID && op.Result == string(reconcile.Success) && (op.Stage == "candidate-apply-intent" || op.Stage == "restore-apply-intent") {
				required = true
			}
		}
	}
	if required && !partial(st, i.ID, allowCurrentPartial) {
		if len(i.RequiredRecoveryFiles) == 0 {
			return ErrRuntime
		}
		if _, err = readInventory(ctx, workspace, i.Saved.Source.Path, i.RequiredRecoveryFiles, false, nil); err != nil {
			return err
		}
	}
	return nil
}
func (d *Driver) Inspect(ctx context.Context, view *state.State) (reconcile.Evidence, error) {
	st, err := d.current()
	if err != nil || view == nil || view.InstallationID != st.InstallationID || view.Ownership != st.Ownership {
		return reconcile.Evidence{}, ErrRuntime
	}
	evidence := reconcile.Evidence{Owned: true, Inputs: true, Storage: true}
	for _, i := range view.Instances {
		if d.validateInstance(ctx, view, i, true) != nil {
			evidence.Inputs = false
			break
		}
		workspace, err := d.workspace(i)
		if err == nil {
			var fs syscall.Statfs_t
			if syscall.Statfs(workspace, &fs) != nil || fs.Bavail == 0 || fs.Ffree == 0 {
				evidence.Storage = false
			}
		}
	}
	var fs syscall.Statfs_t
	if syscall.Statfs(d.root, &fs) != nil || fs.Bavail == 0 || fs.Ffree == 0 || syscall.Access(d.root, 2) != nil {
		evidence.Storage = false
	}
	evidence.Quiescent = !d.uncertain.Load() && (view.Journal == nil || view.Journal.Stage == "prepared" || view.Journal.Operation != nil && (view.Journal.Operation.Result == string(reconcile.Success) || view.Journal.Operation.Result == string(reconcile.Failure)))
	// Absence derives only from explicit enrollment/committed absence or completed
	// conforming stop evidence, never health/PID/group observations.
	evidence.Absent = view.Active.State == "absent"
	if view.Journal != nil && view.Journal.Kind == "deployment" && len(view.Journal.MayBeLive) == 0 && len(view.Journal.CleanupCompleted) > 0 {
		evidence.Absent = true
	}
	if ctx.Err() != nil {
		return reconcile.Evidence{}, ErrRuntime
	}
	return evidence, nil
}
func (d *Driver) Restore(ctx context.Context, i state.Instance, _ reconcile.Artifacts) error {
	st, err := d.current()
	if err != nil {
		return err
	}
	if d.validateInstance(ctx, st, i, false) != nil {
		return ErrRuntime
	}
	workspace, err := d.workspace(i)
	if err != nil {
		return err
	}
	cwd, _, err := workingDirectory(workspace, i.Saved.Source.Path)
	if err != nil {
		return err
	}
	for _, argv := range [][]string{i.Saved.Runtime.Exec.Apply, i.Saved.Runtime.Exec.Stop} {
		if _, err = executable(d.root, workspace, cwd, argv, environmentMap(i.Saved.Runtime.Exec.Env)); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ErrRuntime
	}
	return nil
}
func (d *Driver) Apply(ctx context.Context, i state.Instance) reconcile.Result {
	return d.execute(ctx, i, "apply")
}
func (d *Driver) Stop(ctx context.Context, instances []state.Instance) reconcile.Result {
	if len(instances) != 1 {
		return reconcile.Result{Outcome: reconcile.Failure}
	}
	return d.execute(ctx, instances[0], "stop")
}

// Check takes its authoritative command from the journaled saved check index.
func (d *Driver) Check(ctx context.Context, i state.Instance, _ application.Check) reconcile.Result {
	return d.execute(ctx, i, "check")
}
func (d *Driver) execute(ctx context.Context, requested state.Instance, kind string) reconcile.Result {
	fail := reconcile.Result{Outcome: reconcile.Failure}
	if d.uncertain.Load() {
		return reconcile.Result{Outcome: reconcile.Unknown}
	}
	if !d.command.TryLock() {
		return reconcile.Result{Outcome: reconcile.Unknown}
	}
	defer d.command.Unlock()
	st, err := d.current()
	if err != nil {
		return fail
	}
	i, ok := st.Instances[requested.ID]
	if !ok || i.Target != requested.Target || i.Workspace != requested.Workspace || st.Journal == nil || st.Journal.Operation == nil {
		return fail
	}
	op := st.Journal.Operation
	if op.ID == "" || op.OperandID != i.ID || op.Result != "" {
		return fail
	}
	stage := op.Stage
	if kind == "apply" && stage != "candidate-apply-intent" && stage != "restore-apply-intent" || kind == "stop" && stage != "prior-stop-intent" && stage != "cleanup-intent" || kind == "check" && stage != "candidate-verify-intent" && stage != "restore-verify-intent" && stage != "startup-verify-intent" {
		return fail
	}
	evidence, err := d.Inspect(ctx, st)
	if err != nil || !evidence.Owned || !evidence.Inputs || !evidence.Storage {
		if kind == "check" {
			return reconcile.Result{Outcome: reconcile.Unknown}
		}
		return fail
	}
	// Recheck stable operands after intent, without treating that new intent as
	// permission to skip their pre-invocation recovery requirements.
	if d.validateInstance(ctx, st, i, false) != nil {
		if kind == "check" {
			return reconcile.Result{Outcome: reconcile.Unknown}
		}
		return fail
	}
	workspace, err := d.workspace(i)
	if err != nil {
		if kind == "check" {
			return reconcile.Result{Outcome: reconcile.Unknown}
		}
		return fail
	}
	cwd, _, err := workingDirectory(workspace, i.Saved.Source.Path)
	if err != nil {
		if kind == "check" {
			return reconcile.Result{Outcome: reconcile.Unknown}
		}
		return fail
	}
	argv := i.Saved.Runtime.Exec.Apply
	if kind == "stop" {
		argv = i.Saved.Runtime.Exec.Stop
	}
	if kind == "check" {
		if op.CheckIndex == nil || *op.CheckIndex < 0 || *op.CheckIndex >= len(i.Saved.Health.Checks) {
			return reconcile.Result{Outcome: reconcile.Unknown}
		}
		check := i.Saved.Health.Checks[*op.CheckIndex]
		if check.Type != "command" {
			return reconcile.Result{Outcome: reconcile.Unknown}
		}
		argv = check.Command
	}
	env := environmentMap(i.Saved.Runtime.Exec.Env)
	program, err := executable(d.root, workspace, cwd, argv, env)
	if err != nil {
		return fail
	}
	if kind == "apply" {
		name := filepath.Join(cwd, inventoryName)
		if info, e := os.Lstat(name); e == nil {
			if !info.Mode().IsRegular() {
				return fail
			}
			if os.Remove(name) != nil {
				return fail
			}
		} else if !os.IsNotExist(e) {
			return fail
		}
	}
	result := d.process.run(ctx, program, argv, cwd, environment(env))
	if result.Outcome == reconcile.Unknown {
		d.uncertain.Store(true)
		return result
	}
	if kind == "check" && len(i.RequiredRecoveryFiles) > 0 {
		if _, err := readInventory(ctx, workspace, i.Saved.Source.Path, i.RequiredRecoveryFiles, false, nil); err != nil {
			d.uncertain.Store(true)
			return reconcile.Result{Outcome: reconcile.Unknown}
		}
	}
	if result.Outcome == reconcile.Success && kind == "apply" {
		refs, e := inventory(ctx, workspace, i.Saved.Source.Path, nil, d.fault)
		if e != nil {
			return fail
		}
		result.RecoveryFiles = refs
	}
	return result
}
func environmentMap(overrides map[string]string) map[string]string {
	env := map[string]string{"PATH": "/usr/bin:/bin", "LANG": "C"}
	for k, v := range overrides {
		env[k] = v
	}
	return env
}
func environment(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}
func executable(managedRoot, workspace, cwd string, argv []string, env map[string]string) (string, error) {
	if len(argv) == 0 || argv[0] == "" {
		return "", ErrRuntime
	}
	for _, arg := range argv {
		if strings.ContainsRune(arg, 0) {
			return "", ErrRuntime
		}
	}
	for k, v := range env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return "", ErrRuntime
		}
	}
	candidates := []string{argv[0]}
	if !strings.Contains(argv[0], "/") {
		candidates = nil
		prefixes := filepath.SplitList(env["PATH"])
		if len(prefixes) == 0 {
			prefixes = []string{""}
		}
		for _, prefix := range prefixes {
			candidate := argv[0]
			if prefix != "" {
				candidate = prefix + "/" + argv[0]
			}
			candidates = append(candidates, candidate)
		}
	}
	for _, candidate := range candidates {
		local := !filepath.IsAbs(candidate)
		if local {
			candidate = cwd + "/" + candidate
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		if !local {
			rel, e := filepath.Rel(managedRoot, resolved)
			if e == nil && (rel == "." || relative(rel)) {
				return "", ErrRuntime
			}
		}
		if local {
			rel, e := filepath.Rel(workspace, resolved)
			if e != nil || !relative(rel) {
				return "", ErrRuntime
			}
			f, e := openBeneath(workspace, rel, false)
			if e != nil {
				continue
			}
			f.Close()
		}
		info, e := os.Stat(resolved)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || syscall.Access(resolved, 1) != nil {
			continue
		}
		return resolved, nil
	}
	return "", ErrRuntime
}
