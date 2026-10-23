package git

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"pulltide/internal/reconcile"
	"pulltide/internal/state"
)

func stage(t *testing.T, d *Driver, p, name string) reconcile.Snapshot {
	t.Helper()
	s := source(p, "refs/heads/main")
	r, err := d.Resolve(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	sp := spec(s)
	identity, err := state.ConfigurationIdentity(sp)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := d.Stage(context.Background(), state.Target{Source: r, ConfigurationIdentity: identity}, sp, "staging/"+name, actions)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// This is a runtime discovery double with a tiny test-only reference format.
// The Git adapter never parses this format or infers runtime dependencies.
func discover(ctx context.Context, s reconcile.Snapshot, name string, active, seen map[string]bool) ([]string, error) {
	if active[name] {
		return nil, ErrInvalid
	}
	if seen[name] {
		return nil, nil
	}
	active[name] = true
	defer delete(active, name)
	data, err := s.ReadFile(ctx, name)
	if err != nil {
		return nil, err
	}
	inputs := []string{name}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		kind, ref, ok := strings.Cut(line, " ")
		if !ok {
			return nil, ErrInvalid
		}
		ref = path.Join(path.Dir(name), ref)
		if kind == "include" {
			more, e := discover(ctx, s, ref, active, seen)
			if e != nil {
				return nil, e
			}
			inputs = append(inputs, more...)
		} else {
			inputs = append(inputs, ref)
		}
	}
	seen[name] = true
	return inputs, nil
}
func TestDiscoveryCompleteSiblingClosure(t *testing.T) {
	for _, kind := range []string{"safe", "lfs", "gitlink", "cycle", "missing", "escape"} {
		t.Run(kind, func(t *testing.T) {
			p, commit := repository(t, "sha1")
			put(t, filepath.Join(p, "app/definition"), "include ../sibling/definition\n", 0600)
			put(t, filepath.Join(p, "sibling/definition"), "input env\ncontext context\n", 0600)
			put(t, filepath.Join(p, "sibling/env"), "KEY=value\n", 0600)
			put(t, filepath.Join(p, "sibling/context/file"), "build bytes", 0600)
			put(t, filepath.Join(p, "sibling/context/.dockerignore"), "ignored\n", 0600)
			put(t, filepath.Join(p, "outside"), "not discovered", 0600)
			command(t, p, "add", ".")
			switch kind {
			case "lfs":
				put(t, filepath.Join(p, "sibling/context/ignored"), "version https://git-lfs.github.com/spec/v1\noid sha256:"+strings.Repeat("a", 64)+"\nsize 42\n", 0600)
				command(t, p, "add", ".")
			case "gitlink":
				command(t, p, "update-index", "--add", "--cacheinfo", "160000,"+commit+",sibling/context/ignored")
			case "cycle":
				put(t, filepath.Join(p, "sibling/definition"), "include ../app/definition\n", 0600)
				command(t, p, "add", ".")
			case "missing":
				put(t, filepath.Join(p, "sibling/definition"), "input missing\n", 0600)
				command(t, p, "add", ".")
			case "escape":
				put(t, filepath.Join(p, "sibling/definition"), "input ../../outside\n", 0600)
				command(t, p, "add", ".")
			}
			command(t, p, "commit", "-m", "discovery graph")
			d := driver(t)
			snap := stage(t, d, p, "closure")
			inputs, err := discover(context.Background(), snap, "app/definition", map[string]bool{}, map[string]bool{})
			if kind == "cycle" {
				if err == nil {
					t.Fatal("reference cycle accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			called := false
			i, err := d.Publish(context.Background(), snap, inputs, func(c context.Context, f func(context.Context) reconcile.Result) error {
				called = true
				return actions(c, f)
			})
			if kind != "safe" {
				if err == nil || called || i.Workspace != "" {
					t.Fatal("invalid closure reached publication", kind, err, called)
				}
				return
			}
			if err != nil || !called || len(i.Inputs) != 2 {
				t.Fatal("closure publication", err, called)
			}
			if err = d.ValidateInputs(context.Background(), i, []string{"sibling/context/file", "sibling/env"}); err != nil {
				t.Fatal(err)
			}
			if err = d.ValidateInputs(context.Background(), i, []string{"outside"}); err == nil {
				t.Fatal("consumed undiscovered input")
			}
			if _, err = snap.ReadFile(context.Background(), "outside"); err == nil {
				t.Fatal("published view exposed undiscovered input")
			}
			files, err := snap.ReadDir(context.Background(), "sibling/context")
			if err != nil || len(files) != 2 {
				t.Fatal("directory discovery", files, err)
			}
		})
	}
}
func TestSnapshotSurvivesConcurrentSourceChanges(t *testing.T) {
	p, commit := repository(t, "sha1")
	d := driver(t)
	snap := stage(t, d, p, "stable")
	data, err := snap.ReadFile(context.Background(), "app/run")
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 'X'
	// Fetch a moved branch concurrently with reads of the old snapshot.
	put(t, filepath.Join(p, "app/run"), "new remote bytes", 0755)
	command(t, p, "add", ".")
	command(t, p, "commit", "-m", "concurrent change")
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, e := d.Resolve(context.Background(), source(p, "refs/heads/main"))
		errs <- e
	}()
	for n := 0; n < 8; n++ {
		b, e := snap.ReadFile(context.Background(), "app/run")
		if e != nil || string(b) != "exact\r\nbytes\x00" {
			t.Fatal("snapshot changed", e)
		}
	}
	wg.Wait()
	if err = <-errs; err != nil {
		t.Fatal(err)
	}
	i, err := d.Publish(context.Background(), snap, []string{"app"}, actions)
	if err != nil || i.Target.Source.Revision != commit {
		t.Fatal("published moving revision", err)
	}
	b, err := os.ReadFile(filepath.Join(d.root, i.Workspace, "app/run"))
	if err != nil || string(b) != "exact\r\nbytes\x00" {
		t.Fatal("publication changed bytes", err)
	}
	// A restart consumes retained files, not the old in-memory handle or cache.
	command(t, p, "update-ref", "-d", "refs/heads/main")
	if err = os.RemoveAll(filepath.Join(d.root, "git-cache")); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(p, p+"-offline"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e := os.Rename(p+"-offline", p); e != nil {
			t.Error(e)
		}
	})
	// A recording executable proves restoration does not invoke Git at all.
	gitDir := t.TempDir()
	marker := filepath.Join(gitDir, "invoked")
	put(t, filepath.Join(gitDir, "git"), "#!/bin/sh\nprintf invoked > '"+marker+"'\nexit 99\n", 0700)
	t.Setenv("PATH", gitDir)
	reopened := &Driver{root: d.root}
	i.Workspace = "workspaces/offline"
	if err = reopened.RestoreSource(context.Background(), i, actions); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("offline restoration invoked Git", err)
	}
	b, err = os.ReadFile(filepath.Join(d.root, i.Workspace, "app/run"))
	if err != nil || string(b) != "exact\r\nbytes\x00" {
		t.Fatal("offline restore changed bytes", err)
	}
}
func tamper(t *testing.T, name string) {
	t.Helper()
	if err := os.Chmod(name, 0600); err != nil {
		t.Fatal(err)
	}
	put(t, name, "CORRUPT", 0600)
}
func TestStagingChangedBeforePublication(t *testing.T) {
	for _, when := range []string{"before", "during", "missing"} {
		t.Run(when, func(t *testing.T) {
			p, _ := repository(t, "sha1")
			d := driver(t)
			snap := stage(t, d, p, "changed")
			file := filepath.Join(d.root, "staging/changed/candidate.snapshot")
			if when == "before" {
				tamper(t, file)
			}
			if when == "missing" {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
			if when == "during" {
				d.fault = func(point string) error {
					if point == "publication-start" {
						tamper(t, file)
					}
					return nil
				}
			}
			called := false
			i, err := d.Publish(context.Background(), snap, nil, func(c context.Context, f func(context.Context) reconcile.Result) error {
				called = true
				return actions(c, f)
			})
			if err == nil || i.Workspace != "" || len(i.Inputs) != 0 {
				t.Fatal("changed staging became eligible")
			}
			if when != "during" && called {
				t.Fatal("known corrupt staging reached callback")
			}
		})
	}
}
func TestRetainedManifestAndArtifactCorruption(t *testing.T) {
	for _, name := range []string{"source.artifact", "source.manifest"} {
		for _, failure := range []string{"corrupt", "missing", "symlink"} {
			t.Run(name+"/"+failure, func(t *testing.T) {
				p, _ := repository(t, "sha1")
				d := driver(t)
				i := prepare(t, d, source(p, "refs/heads/main"), "staging/retained")
				file := filepath.Join(d.root, "staging/retained", name)
				switch failure {
				case "corrupt":
					tamper(t, file)
				case "missing":
					if err := os.Remove(file); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					if err := os.Remove(file); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(filepath.Join(p, "app/run"), file); err != nil {
						t.Fatal(err)
					}
				}
				if d.ValidateInputs(context.Background(), i, nil) == nil {
					t.Fatal("bad retained input validated")
				}
				i.Workspace = "workspaces/new"
				called := false
				err := d.RestoreSource(context.Background(), i, func(c context.Context, f func(context.Context) reconcile.Result) error {
					called = true
					return actions(c, f)
				})
				if err == nil || called {
					t.Fatal("bad retained input reached restoration")
				}
			})
		}
	}
}
func TestNamespaceConfinement(t *testing.T) {
	p, _ := repository(t, "sha1")
	d := driver(t)
	i := prepare(t, d, source(p, "refs/heads/main"), "staging/live")
	ctx := context.Background()
	for _, workspace := range []string{i.Workspace, i.Workspace + "/nested", "workspaces", "workspaces/new/nested", "workspaces/../other", "workspaces/./new"} {
		called := false
		copy := i
		copy.Workspace = workspace
		if err := d.RestoreSource(ctx, copy, func(c context.Context, f func(context.Context) reconcile.Result) error {
			called = true
			return actions(c, f)
		}); err == nil || called {
			t.Fatal("unsafe workspace", workspace)
		}
	}
	if err := d.mkdir("workspaces"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(d.root, i.Workspace), filepath.Join(d.root, "workspaces/alias")); err != nil {
		t.Fatal(err)
	}
	copy := i
	copy.Workspace = "workspaces/alias"
	if d.RestoreSource(ctx, copy, actions) == nil {
		t.Fatal("workspace symlink accepted")
	}
	for _, staging := range []string{"staging/live/workspace/new", "staging/live/../new", "staging/./new"} {
		called := false
		_, err := d.Stage(ctx, i.Target, i.Saved, staging, func(c context.Context, f func(context.Context) reconcile.Result) error {
			called = true
			return actions(c, f)
		})
		if err == nil || called {
			t.Fatal("unsafe staging namespace", staging)
		}
	}
}

func TestPublishedSnapshotPreservesSymlinkReachability(t *testing.T) {
	p, _ := repository(t, "sha1")
	put(t, filepath.Join(p, "sibling/run"), "reachable sibling", 0755)
	if err := os.Symlink("../sibling", filepath.Join(p, "app/link")); err != nil {
		t.Fatal(err)
	}
	command(t, p, "add", ".")
	command(t, p, "commit", "-m", "reachable sibling")
	d := driver(t)
	snap := stage(t, d, p, "reachable")
	i, err := d.Publish(context.Background(), snap, nil, actions)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := snap.ReadFile(context.Background(), "app/link/run")
	if err != nil || string(bytes) != "reachable sibling" {
		t.Fatal("published closure lost reachable sibling", err)
	}
	if err = d.ValidateInputs(context.Background(), i, []string{"app/link/run"}); err != nil {
		t.Fatal("retained closure lost reachable sibling", err)
	}
}

func TestSnapshotEntryMetadata(t *testing.T) {
	p, _ := repository(t, "sha1")
	if err := os.Symlink("run", filepath.Join(p, "app/link")); err != nil {
		t.Fatal(err)
	}
	command(t, p, "add", ".")
	command(t, p, "commit", "-m", "metadata")
	d := driver(t)
	snap := stage(t, d, p, "metadata")
	entries, err := snap.ReadDir(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]reconcile.SourceEntry{}
	for _, entry := range entries {
		modes[entry.Name] = entry
	}
	if modes["run"].Mode&0111 == 0 || !modes["run"].Mode.IsRegular() {
		t.Fatal("executable metadata lost")
	}
	if modes["link"].Mode&os.ModeSymlink == 0 || modes["link"].LinkTarget != "run" {
		t.Fatal("symlink metadata lost")
	}
}

func TestConcurrentPublicationHasOneEligibleResult(t *testing.T) {
	p, _ := repository(t, "sha1")
	d := driver(t)
	snap := stage(t, d, p, "concurrent")
	type result struct {
		i   state.Instance
		err error
	}
	results := make(chan result, 2)
	for n := 0; n < 2; n++ {
		go func() { i, err := d.Publish(context.Background(), snap, nil, actions); results <- result{i, err} }()
	}
	successes := 0
	for n := 0; n < 2; n++ {
		r := <-results
		if r.err == nil {
			successes++
			if d.ValidateInputs(context.Background(), r.i, nil) != nil {
				t.Fatal("published incomplete input")
			}
		} else if r.i.Workspace != "" {
			t.Fatal("failed concurrent publication returned eligible input")
		}
	}
	if successes != 1 {
		t.Fatal("publication was not exclusive", successes)
	}
}

func TestSymlinkResolutionMatchesMaterializedFilesystem(t *testing.T) {
	p, _ := repository(t, "sha1")
	put(t, filepath.Join(p, "target/file"), "safe target", 0600)
	if err := os.Symlink("target", filepath.Join(p, "alias")); err != nil {
		t.Fatal(err)
	}
	// Repeated traversal of one link in a finite path is not a symlink cycle.
	if err := os.Symlink("../alias/../alias/file", filepath.Join(p, "app/link")); err != nil {
		t.Fatal(err)
	}
	command(t, p, "add", ".")
	command(t, p, "commit", "-m", "finite repeated symlink")
	d := driver(t)
	i := prepare(t, d, source(p, "refs/heads/main"), "staging/repeated")
	data, err := os.ReadFile(filepath.Join(d.root, i.Workspace, "app/link"))
	if err != nil || string(data) != "safe target" {
		t.Fatal("safe finite symlink failed", err)
	}
}

func TestSymlinkTraversalLimitFailsBeforeStaging(t *testing.T) {
	p, _ := repository(t, "sha1")
	for n := 0; n < 40; n++ {
		if err := os.Symlink("chain"+strconv.Itoa(n+1), filepath.Join(p, "app/chain"+strconv.Itoa(n))); err != nil {
			t.Fatal(err)
		}
	}
	put(t, filepath.Join(p, "app/chain40"), "last", 0600)
	if err := os.Symlink("chain0", filepath.Join(p, "app/long")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Join(p, "app/long")); err == nil {
		t.Fatal("fixture should exceed Linux symlink lookup limit")
	}
	command(t, p, "add", ".")
	command(t, p, "commit", "-m", "link lookup limit")
	d := driver(t)
	s := source(p, "refs/heads/main")
	r, err := d.Resolve(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = d.Stage(context.Background(), state.Target{Source: r}, spec(s), "staging/too-long", func(c context.Context, f func(context.Context) reconcile.Result) error {
		called = true
		return actions(c, f)
	})
	if err == nil || called {
		t.Fatal("unreadable link chain reached staging")
	}
}
