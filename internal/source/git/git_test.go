package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"pulltide/internal/application"
	"pulltide/internal/reconcile"
	"pulltide/internal/state"
	"strings"
	"testing"
)

func command(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %s %v", args, b, e)
	}
	return strings.TrimSpace(string(b))
}
func repository(t *testing.T, format string) (string, string) {
	t.Helper()
	p := t.TempDir()
	command(t, p, "init", "--object-format="+format, "-b", "main")
	put(t, filepath.Join(p, "app/run"), "exact\r\nbytes\x00", 0755)
	command(t, p, "add", ".")
	command(t, p, "commit", "-m", "initial")
	return p, command(t, p, "rev-parse", "HEAD")
}
func put(t *testing.T, p, s string, m os.FileMode) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(s), m); e != nil {
		t.Fatal(e)
	}
}
func source(p, ref string) application.Source {
	return application.Source{Type: "git", URL: "file://" + p, Ref: ref, Path: "app"}
}
func spec(s application.Source) application.Spec {
	return application.Spec{Source: s, Reconcile: application.Reconcile{IntervalSeconds: 60, OperationTimeoutSeconds: 30}, Runtime: application.Runtime{Type: "exec", Exec: &application.Exec{Apply: []string{"true"}, Stop: []string{"true"}, Env: map[string]string{}}}, Health: application.Health{TimeoutSeconds: 30, IntervalSeconds: 1, Checks: []application.Check{{Type: "command", Command: []string{"true"}, TimeoutSeconds: 1}}}, Rollback: application.Rollback{Enabled: true}}
}
func actions(ctx context.Context, f func(context.Context) reconcile.Result) error {
	if f(ctx).Outcome != reconcile.Success {
		return ErrSource
	}
	return nil
}
func materialize(d *Driver, ctx context.Context, target state.Target, spec application.Spec, stage string, run reconcile.Artifacts) (state.Instance, error) {
	snapshot, err := d.Stage(ctx, target, spec, stage, run)
	if err != nil {
		return state.Instance{}, err
	}
	return d.Publish(ctx, snapshot, nil, run)
}
func restore(d *Driver, ctx context.Context, i state.Instance, workspace string, run reconcile.Artifacts) error {
	i.Workspace = workspace
	return d.RestoreSource(ctx, i, run)
}
func prepare(t *testing.T, d *Driver, s application.Source, stage string) state.Instance {
	t.Helper()
	r, e := d.Resolve(context.Background(), s)
	if e != nil {
		t.Fatalf("resolve: %v", e)
	}
	sp := spec(s)
	id, e := state.ConfigurationIdentity(sp)
	if e != nil {
		t.Fatal(e)
	}
	i, e := materialize(d, context.Background(), state.Target{Source: r, ConfigurationIdentity: id}, sp, stage, actions)
	if e != nil {
		t.Fatalf("materialize: %v", e)
	}
	return i
}
func TestResolveMaterializeAndOffline(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			p, commit := repository(t, format)
			root := t.TempDir()
			if e := os.Chmod(root, 0700); e != nil {
				t.Fatal(e)
			}
			d := &Driver{root: root}
			s := source(p, "refs/heads/main")
			i := prepare(t, d, s, "staging/first")
			if i.Target.Source.Revision != commit {
				t.Fatal(i.Target)
			}
			b, e := os.ReadFile(filepath.Join(root, i.Workspace, "app/run"))
			if e != nil || string(b) != "exact\r\nbytes\x00" {
				t.Fatalf("bytes %q %v", b, e)
			}
			info, _ := os.Stat(filepath.Join(root, i.Workspace, "app/run"))
			if info.Mode()&0111 == 0 {
				t.Fatal("lost execute bit")
			}
			if _, e = os.Stat(filepath.Join(root, i.Workspace, ".git")); !os.IsNotExist(e) {
				t.Fatal("git administrative data")
			}
			put(t, filepath.Join(root, i.Workspace, "app/run"), "runtime mutation", 0600)
			if e = os.Rename(p, p+"-offline"); e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { os.Rename(p+"-offline", p) })
			s.Ref = strings.ToUpper(commit)
			r, e := d.Resolve(context.Background(), s)
			if e != nil || r != i.Target.Source {
				t.Fatalf("offline %v %v", r, e)
			}
			s.Ref = "refs/heads/main"
			if _, e = d.Resolve(context.Background(), s); e == nil {
				t.Fatal("cached branch used")
			}
			if e = d.ValidateInputs(context.Background(), i, []string{"app"}); e != nil {
				t.Fatal(e)
			}
			if e = restore(d, context.Background(), i, "workspaces/restored", actions); e != nil {
				t.Fatal(e)
			}
			b, e = os.ReadFile(filepath.Join(root, "workspaces/restored/app/run"))
			if e != nil || string(b) != "exact\r\nbytes\x00" {
				t.Fatal("restore was not original")
			}
		})
	}
}

func driver(t *testing.T) *Driver {
	t.Helper()
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	return &Driver{root: root}
}
func TestMovingRefsAndPeeling(t *testing.T) {
	p, first := repository(t, "sha1")
	d := driver(t)
	ctx := context.Background()
	command(t, p, "tag", "light")
	command(t, p, "tag", "-a", "annotated", "-m", "tag")
	command(t, p, "tag", "-a", "chain", "annotated", "-m", "chain")
	for _, ref := range []string{"refs/heads/main", "refs/tags/light", "refs/tags/annotated", "refs/tags/chain"} {
		r, e := d.Resolve(ctx, source(p, ref))
		if e != nil || r.Revision != first {
			t.Fatalf("%s: %v %v", ref, r, e)
		}
	}
	put(t, filepath.Join(p, "app/run"), "new", 0755)
	command(t, p, "add", ".")
	command(t, p, "commit", "-m", "new")
	second := command(t, p, "rev-parse", "HEAD")
	command(t, p, "tag", "-f", "light")
	command(t, p, "tag", "-f", "-a", "annotated", "-m", "moved")
	for _, ref := range []string{"refs/heads/main", "refs/tags/light", "refs/tags/annotated"} {
		r, e := d.Resolve(ctx, source(p, ref))
		if e != nil || r.Revision != second {
			t.Fatalf("moved %s: %v %v", ref, r, e)
		}
	}
	command(t, p, "reset", "--hard", first)
	r, e := d.Resolve(ctx, source(p, "refs/heads/main"))
	if e != nil || r.Revision != first {
		t.Fatal("force push", e)
	}
	command(t, p, "tag", "-d", "light")
	if _, e = d.Resolve(ctx, source(p, "refs/tags/light")); e == nil {
		t.Fatal("deleted tag reused")
	}
	command(t, p, "update-ref", "-d", "refs/heads/main")
	if _, e = d.Resolve(ctx, source(p, "refs/heads/main")); e == nil {
		t.Fatal("deleted branch reused")
	}
	for _, object := range []string{first + ":app/run", first + "^{tree}"} {
		oid := command(t, p, "rev-parse", object)
		command(t, p, "tag", "-f", "bad", oid)
		if _, e = d.Resolve(ctx, source(p, "refs/tags/bad")); e == nil {
			t.Fatal("noncommit tag")
		}
	}
	command(t, p, "update-ref", "refs/heads/main", first)
	if _, e = d.Resolve(ctx, source(p, "refs/heads/main")); e != nil {
		t.Fatal("retry", e)
	}
}
func TestInputConfinement(t *testing.T) {
	for _, tc := range []struct{ name, target string }{{"escape", "../../outside"}, {"absolute", "/etc/passwd"}, {"dangling", "missing"}, {"cycle", "link"}, {"directory-cycle", "."}} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := repository(t, "sha1")
			if e := os.Symlink(tc.target, filepath.Join(p, "app/link")); e != nil {
				t.Fatal(e)
			}
			command(t, p, "add", ".")
			command(t, p, "commit", "-m", "link")
			d := driver(t)
			s := source(p, "refs/heads/main")
			r, e := d.Resolve(context.Background(), s)
			if e != nil {
				t.Fatal(e)
			}
			i, e := materialize(d, context.Background(), state.Target{Source: r}, spec(s), "staging/bad", actions)
			if e == nil || i.Workspace != "" {
				t.Fatal("unsafe content accepted")
			}
		})
	}
	p, _ := repository(t, "sha1")
	if e := os.Symlink("run", filepath.Join(p, "app/link")); e != nil {
		t.Fatal(e)
	}
	command(t, p, "add", ".")
	command(t, p, "commit", "-m", "safe link")
	d := driver(t)
	i := prepare(t, d, source(p, "refs/heads/main"), "staging/safe")
	link, e := os.Readlink(filepath.Join(d.root, i.Workspace, "app/link"))
	if e != nil || link != "run" {
		t.Fatal(link, e)
	}
	for _, path := range []string{"../app", "/app", "missing", "app/run"} {
		s := source(p, "refs/heads/main")
		s.Path = path
		r, _ := d.Resolve(context.Background(), source(p, "refs/heads/main"))
		if _, e = materialize(d, context.Background(), state.Target{Source: r}, spec(s), "staging/invalid", actions); e == nil {
			t.Fatal("bad path", path)
		}
	}
}
func TestUnsupportedClosure(t *testing.T) {
	pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("a", 64) + "\nsize 12\n"
	for _, kind := range []string{"lfs", "gitlink"} {
		t.Run(kind, func(t *testing.T) {
			p, commit := repository(t, "sha1")
			if kind == "lfs" {
				put(t, filepath.Join(p, "sibling/input"), pointer, 0600)
				command(t, p, "add", ".")
			} else {
				command(t, p, "update-index", "--add", "--cacheinfo", "160000,"+commit+",sibling/input")
			}
			command(t, p, "commit", "-m", "unsupported sibling")
			d := driver(t)
			i := prepare(t, d, source(p, "refs/heads/main"), "staging/allowed")
			if e := d.ValidateInputs(context.Background(), i, []string{"sibling"}); e == nil {
				t.Fatal("full input closure not checked")
			}
			if e := os.Symlink("../sibling", filepath.Join(p, "app/link")); e != nil {
				t.Fatal(e)
			}
			command(t, p, "add", "app/link")
			command(t, p, "commit", "-m", "reachable sibling")
			s := source(p, "refs/heads/main")
			r, e := d.Resolve(context.Background(), s)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = materialize(d, context.Background(), state.Target{Source: r}, spec(s), "staging/rejected", actions); e == nil {
				t.Fatal("symlink closure not checked")
			}
		})
	}
}
func TestInterruptedPublicationAndCorruption(t *testing.T) {
	for _, point := range []string{"stage-created", "snapshot-synced", "source.artifact-synced", "source.artifact-renamed", "source.manifest-synced", "source.manifest-renamed", "extract-entry", "workspace-synced", "published"} {
		t.Run(point, func(t *testing.T) {
			p, _ := repository(t, "sha1")
			d := driver(t)
			s := source(p, "refs/heads/main")
			r, e := d.Resolve(context.Background(), s)
			if e != nil {
				t.Fatal(e)
			}
			d.fault = func(p string) error {
				if p == point {
					return ErrSource
				}
				return nil
			}
			i, e := materialize(d, context.Background(), state.Target{Source: r}, spec(s), "staging/interrupted", actions)
			if e == nil || i.Workspace != "" || len(i.Inputs) != 0 {
				t.Fatal("partial publication eligible")
			}
			d = &Driver{root: d.root}
			prepare(t, d, s, "staging/retry")
		})
	}
	p, _ := repository(t, "sha1")
	d := driver(t)
	i := prepare(t, d, source(p, "refs/heads/main"), "staging/good")
	if err := os.Chmod(filepath.Join(d.root, i.Inputs[0].Path), 0600); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(d.root, i.Inputs[0].Path), "corrupt", 0600)
	if e := d.ValidateInputs(context.Background(), i, nil); e == nil {
		t.Fatal("corrupt retained accepted")
	}
	called := false
	e := restore(d, context.Background(), i, "workspaces/restore", func(c context.Context, f func(context.Context) reconcile.Result) error {
		called = true
		return actions(c, f)
	})
	if e == nil || called {
		t.Fatal("corrupt recovery invoked action")
	}
}
func TestCacheScopeIntegrityAndIndependentRetention(t *testing.T) {
	p, commit := repository(t, "sha1")
	d := driver(t)
	s := source(p, "refs/heads/main")
	i := prepare(t, d, s, "staging/retained")
	repo := filepath.Join(d.root, "git-cache", strings.TrimPrefix(identity(s.URL), "sha256:"), "repository")
	blob := command(t, p, "rev-parse", "HEAD:app/run")
	object := filepath.Join(repo, "objects", blob[:2], blob[2:])
	if _, err := os.Stat(object); os.IsNotExist(err) {
		// Strict fetch verification can retain a pack even for a tiny repository.
		// Locate this blob's verified pack rather than assuming loose-object storage.
		indexes, err := filepath.Glob(filepath.Join(repo, "objects", "pack", "*.idx"))
		if err != nil {
			t.Fatal(err)
		}
		object = ""
		for _, index := range indexes {
			listing := command(t, repo, "verify-pack", "-v", index)
			for _, line := range strings.Split(listing, "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 2 && fields[0] == blob && fields[1] == "blob" {
					object = strings.TrimSuffix(index, ".idx") + ".pack"
				}
			}
		}
		if object == "" {
			t.Fatal("cached blob is in neither loose storage nor a verified pack")
		}
	} else if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(object); err != nil {
		t.Fatal(err)
	}
	check := exec.Command("git", "--git-dir="+repo, "cat-file", "-e", blob)
	if err := check.Run(); err == nil {
		t.Fatal("corruption setup left cached blob readable")
	}
	if e := os.Rename(p, p+"-offline"); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.Rename(p+"-offline", p) })
	s.Ref = commit
	if _, e := d.Resolve(context.Background(), s); e == nil {
		t.Fatal("incomplete cache accepted")
	}
	other := s
	other.URL += "/"
	if _, e := d.Resolve(context.Background(), other); e == nil {
		t.Fatal("cross-source offline cache")
	}
	if e := os.RemoveAll(filepath.Join(d.root, "git-cache")); e != nil {
		t.Fatal(e)
	}
	if e := restore(d, context.Background(), i, "workspaces/recovered", actions); e != nil {
		t.Fatal(e)
	}
}
func TestConcurrentCacheAndExclusiveMaterialization(t *testing.T) {
	p, commit := repository(t, "sha1")
	d := driver(t)
	s := source(p, "refs/heads/main")
	errs := make(chan error, 8)
	for n := 0; n < 8; n++ {
		go func() {
			other := &Driver{root: d.root}
			r, e := other.Resolve(context.Background(), s)
			if e == nil && r.Revision != commit {
				e = ErrSource
			}
			errs <- e
		}()
	}
	for n := 0; n < 8; n++ {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
	i := prepare(t, d, s, "staging/unique")
	put(t, filepath.Join(d.root, i.Workspace, "app/run"), "live", 0600)
	if _, e := materialize(d, context.Background(), i.Target, i.Saved, "staging/unique", actions); e == nil {
		t.Fatal("reused staging")
	}
	if e := restore(d, context.Background(), i, i.Workspace, actions); e == nil {
		t.Fatal("overwrote live workspace")
	}
	b, _ := os.ReadFile(filepath.Join(d.root, i.Workspace, "app/run"))
	if string(b) != "live" {
		t.Fatal("live bytes modified")
	}
}
func TestSanitizedFailuresAndCancellation(t *testing.T) {
	d := driver(t)
	for _, u := range []string{"https://user:TOPSECRET@example.invalid/repo", "ssh://user:TOPSECRET@example.invalid/repo", "file://relative", "relative"} {
		s := source("unused", "refs/heads/main")
		s.URL = u
		_, e := d.Resolve(context.Background(), s)
		if e == nil || strings.Contains(e.Error(), "TOPSECRET") {
			t.Fatal("URL diagnostic", e)
		}
	}
	p, _ := repository(t, "sha1")
	s := source(p, "refs/heads/main")
	if _, e := d.Resolve(context.Background(), s); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := d.Resolve(ctx, s); e == nil {
		t.Fatal("cancelled resolution succeeded")
	}
}

func TestValidationBeforeCallbackAndWorkspaceOverlap(t *testing.T) {
	for _, target := range []string{"run/../run", "run/.", "run/"} {
		t.Run(target, func(t *testing.T) {
			p, _ := repository(t, "sha1")
			if err := os.Symlink(target, filepath.Join(p, "app/link")); err != nil {
				t.Fatal(err)
			}
			command(t, p, "add", ".")
			command(t, p, "commit", "-m", "invalid path component")
			d := driver(t)
			s := source(p, "refs/heads/main")
			revision, err := d.Resolve(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			_, err = materialize(d, context.Background(), state.Target{Source: revision}, spec(s), "staging/invalid", func(ctx context.Context, f func(context.Context) reconcile.Result) error {
				called = true
				return actions(ctx, f)
			})
			if err == nil || called {
				t.Fatalf("invalid symlink reached callback: accepted=%v callback=%v", err == nil, called)
			}
		})
	}
	p, _ := repository(t, "sha1")
	d := driver(t)
	i := prepare(t, d, source(p, "refs/heads/main"), "staging/live")
	called := false
	err := restore(d, context.Background(), i, i.Workspace+"/nested", func(ctx context.Context, f func(context.Context) reconcile.Result) error {
		called = true
		return actions(ctx, f)
	})
	if err == nil || called {
		t.Fatal("overlapping restoration reached callback")
	}
}
