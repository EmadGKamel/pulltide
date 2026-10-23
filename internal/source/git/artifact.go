package git

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pulltide/internal/application"
	"pulltide/internal/reconcile"
	"pulltide/internal/state"
)

// snapshot is private to one uninterrupted attempt. Its tree never changes.
// Discovery gets copies, not a checkout or the staging path.
type snapshot struct {
	owner     *Driver
	target    state.Target
	spec      application.Spec
	stage     string
	tree      *tree
	checksum  string
	mu        sync.Mutex
	published bool
	inputs    []string
}

func (s *snapshot) ReadFile(ctx context.Context, name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ErrSource
	}
	m, err := s.tree.index()
	if err != nil {
		return nil, err
	}
	name, err = resolve(m, name, map[string]bool{})
	if err != nil {
		return nil, err
	}
	if !s.allowed(name) {
		return nil, ErrInvalid
	}
	en := m[name]
	if en.Mode != "100644" && en.Mode != "100755" {
		return nil, ErrInvalid
	}
	return append([]byte(nil), en.Data...), nil
}
func (s *snapshot) ReadDir(ctx context.Context, name string) ([]reconcile.SourceEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ErrSource
	}
	m, err := s.tree.index()
	if err != nil {
		return nil, err
	}
	name, err = resolve(m, name, map[string]bool{})
	if err != nil || m[name].Mode != "040000" {
		return nil, ErrInvalid
	}
	if !s.allowed(name) {
		return nil, ErrInvalid
	}
	entries := []reconcile.SourceEntry{}
	for _, en := range s.tree.Entries {
		if ctx.Err() != nil {
			return nil, ErrSource
		}
		if en.Name == "." || path.Dir(en.Name) != name {
			continue
		}
		metadata := reconcile.SourceEntry{Name: path.Base(en.Name)}
		switch en.Mode {
		case "040000":
			metadata.Mode = os.ModeDir | 0755
		case "100644":
			metadata.Mode = 0644
		case "100755":
			metadata.Mode = 0755
		case "120000":
			metadata.Mode = os.ModeSymlink | 0777
			metadata.LinkTarget = string(en.Data)
		default:
			metadata.Mode = os.ModeIrregular
		}
		entries = append(entries, metadata)
	}
	return entries, nil
}

func (s *snapshot) allowed(name string) bool {
	if !s.published {
		return true
	}
	for _, root := range s.inputs {
		if root == "." || name == root || strings.HasPrefix(name, root+"/") {
			return true
		}
	}
	return false
}

// sourceManifest binds the accepted closure and the actual retained snapshot.
// It is separate from runtime manifests and contains no transport credentials.
type sourceManifest struct {
	Version        int
	Source         state.Revision
	Selected       string
	SnapshotSHA256 string
	Inputs         []string
}

func namespace(name, prefix string) bool {
	return safeRelative(name) && filepath.Clean(name) == name && strings.HasPrefix(name, prefix+"/") && len(strings.Split(name, "/")) == 2
}
func (d *Driver) Stage(ctx context.Context, target state.Target, spec application.Spec, staging string, run reconcile.Artifacts) (reconcile.Snapshot, error) {
	if application.ValidateSource(spec.Source) != nil || target.Source.Driver != "git" || target.Source.SourceIdentity != identity(spec.Source.URL) || !oidPattern.MatchString(target.Source.Revision) || !namespace(staging, "staging") || run == nil || spec.Reconcile.OperationTimeoutSeconds < 1 {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(spec.Reconcile.OperationTimeoutSeconds)*time.Second)
	defer cancel()
	base := "git-cache/" + strings.TrimPrefix(target.Source.SourceIdentity, "sha256:")
	lock, err := d.lock(ctx, base+"/lock")
	if err != nil {
		return nil, ErrSource
	}
	repo, err := d.path(base+"/repository", true)
	var tr *tree
	if err == nil {
		tr, err = d.readTree(ctx, repo, target.Source.Revision)
	}
	unlock(lock)
	if err != nil {
		return nil, ErrSource
	}
	tr.SourceIdentity = target.Source.SourceIdentity
	// Selected-source safety checks precede even the source-only staging action.
	if err = tr.validate(ctx, spec.Source.Path, nil); err != nil {
		return nil, err
	}
	var data bytes.Buffer
	if gob.NewEncoder(&data).Encode(tr) != nil {
		return nil, ErrSource
	}
	staged := &snapshot{owner: d, target: target, spec: cloneSaved(spec), stage: staging, tree: tr, checksum: digest(data.Bytes())}
	done := false
	err = run(ctx, func(c context.Context) reconcile.Result {
		fail := reconcile.Result{Outcome: reconcile.Failure}
		if d.boundary(c, "stage-start") != nil || d.mkdir("staging") != nil {
			return fail
		}
		dir, e := d.path(staging, false)
		if e != nil || os.Mkdir(dir, 0700) != nil {
			return fail
		}
		if d.boundary(c, "stage-created") != nil || writeDurable(filepath.Join(dir, "candidate.snapshot"), data.Bytes(), 0400) != nil {
			return fail
		}
		if d.boundary(c, "snapshot-synced") != nil || d.syncParents(c, dir) != nil || staged.verify(c) != nil {
			return fail
		}
		done = true
		return reconcile.Result{Outcome: reconcile.Success}
	})
	if err != nil || !done {
		return nil, ErrSource
	}
	return staged, nil
}
func cloneSaved(spec application.Spec) application.Spec {
	data, _ := json.Marshal(spec)
	var copy application.Spec
	_ = json.Unmarshal(data, &copy)
	return copy
}
func (s *snapshot) verify(ctx context.Context) error {
	if ctx.Err() != nil {
		return ErrSource
	}
	p, e := s.owner.path(s.stage+"/candidate.snapshot", true)
	if e != nil {
		return ErrSource
	}
	info, e := os.Lstat(p)
	if e != nil || !info.Mode().IsRegular() {
		return ErrSource
	}
	data, e := os.ReadFile(p)
	if e != nil || digest(data) != s.checksum || ctx.Err() != nil {
		return ErrSource
	}
	return nil
}
func (d *Driver) Publish(ctx context.Context, view reconcile.Snapshot, inputs []string, run reconcile.Artifacts) (state.Instance, error) {
	s, ok := view.(*snapshot)
	if !ok || s.owner != d || run == nil {
		return state.Instance{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.published {
		return state.Instance{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.spec.Reconcile.OperationTimeoutSeconds)*time.Second)
	defer cancel()
	if err := s.verify(ctx); err != nil {
		return state.Instance{}, err
	}
	roots, err := s.tree.closure(ctx, s.spec.Source.Path, inputs)
	if err != nil {
		return state.Instance{}, err
	}
	manifest := sourceManifest{Version: 1, Source: s.target.Source, Selected: s.spec.Source.Path, SnapshotSHA256: s.checksum, Inputs: roots}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return state.Instance{}, ErrSource
	}
	var instance state.Instance
	err = run(ctx, func(c context.Context) reconcile.Result {
		fail := reconcile.Result{Outcome: reconcile.Failure}
		if d.boundary(c, "publication-start") != nil || s.verify(c) != nil {
			return fail
		}
		dir, e := d.path(s.stage, true)
		if e != nil {
			return fail
		}
		data, e := os.ReadFile(filepath.Join(dir, "candidate.snapshot"))
		if e != nil || digest(data) != s.checksum {
			return fail
		}
		for _, file := range []struct {
			name string
			data []byte
		}{{"source.artifact", data}, {"source.manifest", manifestBytes}} {
			pending := filepath.Join(dir, ".pending-"+file.name)
			if writeDurable(pending, file.data, 0400) != nil || d.boundary(c, file.name+"-synced") != nil {
				return fail
			}
			if os.Rename(pending, filepath.Join(dir, file.name)) != nil || d.boundary(c, file.name+"-renamed") != nil || syncDir(dir) != nil {
				return fail
			}
		}
		if d.extract(c, s.tree, s.stage+"/workspace") != nil || d.syncParents(c, dir) != nil || d.boundary(c, "published") != nil || s.verify(c) != nil {
			return fail
		}
		candidate := state.Instance{Target: s.target, Saved: cloneSaved(s.spec), Workspace: s.stage + "/workspace", Inputs: []state.FileReference{
			{Path: s.stage + "/source.artifact", SHA256: s.checksum}, {Path: s.stage + "/source.manifest", SHA256: digest(manifestBytes)},
		}}
		if d.ValidateInputs(c, candidate, nil) != nil {
			return fail
		}
		instance = candidate
		s.inputs = append([]string(nil), roots...)
		s.published = true
		return reconcile.Result{Outcome: reconcile.Success}
	})
	if err != nil || instance.Workspace == "" {
		return state.Instance{}, ErrSource
	}
	return instance, nil
}
func (d *Driver) ValidateInputs(ctx context.Context, i state.Instance, inputs []string) error {
	tr, manifest, err := d.retained(ctx, i)
	if err != nil {
		return err
	}
	if err = tr.validate(ctx, manifest.Selected, manifest.Inputs); err != nil {
		return err
	}
	index, err := tr.index()
	if err != nil {
		return err
	}
	for _, name := range inputs {
		resolved, e := resolve(index, name, map[string]bool{})
		if e != nil {
			return ErrInvalid
		}
		allowed := false
		for _, root := range manifest.Inputs {
			if root == "." || resolved == root || strings.HasPrefix(resolved, root+"/") {
				allowed = true
				break
			}
		}
		if !allowed {
			return ErrInvalid
		}
	}
	return nil
}

// RestoreSource consumes only verified local retained bytes. Core supplies the
// fresh workspace reservation and the candidate's frozen recovery deadline.
func (d *Driver) RestoreSource(ctx context.Context, i state.Instance, run reconcile.Artifacts) error {
	if run == nil || !namespace(i.Workspace, "workspaces") {
		return ErrInvalid
	}
	// Direct-child namespaces prevent descendants/ancestors of live workspaces.
	dest, e := d.path(i.Workspace, false)
	if e != nil {
		return e
	}
	if _, e = os.Lstat(dest); !os.IsNotExist(e) {
		return ErrInvalid
	}
	if e = d.ValidateInputs(ctx, i, nil); e != nil {
		return e
	}
	done := false
	e = run(ctx, func(c context.Context) reconcile.Result {
		fail := reconcile.Result{Outcome: reconcile.Failure}
		tr, manifest, err := d.retained(c, i)
		if err != nil || tr.validate(c, manifest.Selected, manifest.Inputs) != nil {
			return fail
		}
		if d.extract(c, tr, i.Workspace) != nil || d.ValidateInputs(c, i, nil) != nil {
			return fail
		}
		done = true
		return reconcile.Result{Outcome: reconcile.Success}
	})
	if e != nil || !done {
		return ErrSource
	}
	return nil
}
func (d *Driver) retained(ctx context.Context, i state.Instance) (*tree, *sourceManifest, error) {
	refs := map[string]state.FileReference{}
	for _, ref := range i.Inputs {
		for _, name := range []string{"source.artifact", "source.manifest"} {
			if strings.HasSuffix(ref.Path, "/"+name) {
				if _, ok := refs[name]; ok {
					return nil, nil, ErrRetained
				}
				refs[name] = ref
			}
		}
	}
	read := func(name string) ([]byte, error) {
		ref, ok := refs[name]
		if !ok || ctx.Err() != nil {
			return nil, ErrRetained
		}
		p, e := d.path(ref.Path, true)
		if e != nil {
			return nil, ErrRetained
		}
		info, e := os.Lstat(p)
		if e != nil || !info.Mode().IsRegular() {
			return nil, ErrRetained
		}
		data, e := os.ReadFile(p)
		if e != nil || digest(data) != ref.SHA256 || ctx.Err() != nil {
			return nil, ErrRetained
		}
		return data, nil
	}
	data, e := read("source.artifact")
	if e != nil {
		return nil, nil, e
	}
	var tr tree
	dec := gob.NewDecoder(bytes.NewReader(data))
	if dec.Decode(&tr) != nil {
		return nil, nil, ErrRetained
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, nil, ErrRetained
	}
	data, e = read("source.manifest")
	if e != nil {
		return nil, nil, e
	}
	var manifest sourceManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(&extra) != io.EOF {
		return nil, nil, ErrRetained
	}
	if manifest.Version != 1 || manifest.Source != i.Target.Source || manifest.Selected != i.Saved.Source.Path || manifest.SnapshotSHA256 != refs["source.artifact"].SHA256 || len(manifest.Inputs) == 0 ||
		tr.Driver != i.Target.Source.Driver || tr.SourceIdentity != i.Target.Source.SourceIdentity || tr.Revision != i.Target.Source.Revision || identity(i.Saved.Source.URL) != tr.SourceIdentity {
		return nil, nil, ErrRetained
	}
	if filepath.Dir(refs["source.artifact"].Path) != filepath.Dir(refs["source.manifest"].Path) {
		return nil, nil, ErrRetained
	}
	if _, e = tr.index(); e != nil {
		return nil, nil, ErrRetained
	}
	return &tr, &manifest, nil
}

func (d *Driver) extract(ctx context.Context, tr *tree, workspace string) error {
	if !safeRelative(workspace) || workspace == "." {
		return ErrInvalid
	}
	if _, e := tr.index(); e != nil {
		return e
	}
	if d.mkdir(filepath.Dir(workspace)) != nil {
		return ErrSource
	}
	dest, e := d.path(workspace, false)
	if e != nil {
		return e
	}
	if _, e = os.Lstat(dest); !os.IsNotExist(e) {
		return ErrSource
	}
	if os.Mkdir(dest, 0700) != nil {
		return ErrSource
	}
	temp := dest
	// A fresh exclusive directory is never reused. Partial data has no returned
	// artifact/workspace reference and remains for core-owned reclamation.
	for _, en := range tr.Entries {
		if d.boundary(ctx, "extract-entry") != nil {
			return ErrSource
		}
		if en.Name == "." {
			continue
		}
		p := filepath.Join(temp, en.Name)
		switch en.Mode {
		case "040000", "160000":
			e = os.Mkdir(p, 0700)
		case "120000":
			e = os.Symlink(string(en.Data), p)
		default:
			mode := os.FileMode(0600)
			if en.Mode == "100755" {
				mode = 0700
			}
			e = writeDurable(p, en.Data, mode)
		}
		if e != nil {
			return ErrSource
		}
	}
	// Children precede parents at the durability barrier, including symlink entries.
	for n := len(tr.Entries) - 1; n >= 0; n-- {
		en := tr.Entries[n]
		if ctx.Err() != nil {
			return ErrSource
		}
		if en.Mode == "040000" || en.Mode == "160000" {
			if syncDir(filepath.Join(temp, en.Name)) != nil {
				return ErrSource
			}
		}
	}
	if d.boundary(ctx, "workspace-synced") != nil {
		return ErrSource
	}
	return d.syncParents(ctx, dest)
}
func writeDurable(p string, data []byte, mode os.FileMode) error {
	f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if e != nil {
		return ErrSource
	}
	n, e := f.Write(data)
	if e == nil && n != len(data) {
		e = io.ErrShortWrite
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil || ce != nil {
		return ErrSource
	}
	return nil
}
func syncDir(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return ErrSource
	}
	e = f.Sync()
	ce := f.Close()
	if e != nil || ce != nil {
		return ErrSource
	}
	return nil
}
func (d *Driver) syncParents(ctx context.Context, p string) error {
	for {
		if ctx.Err() != nil || syncDir(p) != nil {
			return ErrSource
		}
		if p == d.root {
			return nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ErrSource
		}
		p = parent
	}
}
