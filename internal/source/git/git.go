//go:build linux

// Package git implements the Git source boundary, never runtime operations.
package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"pulltide/internal/application"
	"pulltide/internal/reconcile"
	"pulltide/internal/state"
)

// Diagnostics deliberately never wrap process output, URLs, paths, or host errors.
var ErrSource = errors.New("git source operation failed")
var ErrInvalid = errors.New("git source input validation failed")
var ErrRetained = errors.New("git retained source verification failed")

// Driver uses the canonical private store. The caller holds Application ownership.
// Context deadlines supplied by core bound resolution and recovery operations.
// No method releases or garbage-collects retained artifacts.
type Driver struct {
	root  string
	fault func(string) error
}

func New() *Driver { return &Driver{root: state.Root} }

var _ reconcile.Source = (*Driver)(nil)
var oidPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

func identity(url string) string { h := sha256.Sum256([]byte(url)); return fmt.Sprintf("sha256:%x", h) }
func (d *Driver) boundary(ctx context.Context, point string) error {
	if ctx.Err() != nil {
		return ErrSource
	}
	if d.fault != nil && d.fault(point) != nil {
		return ErrSource
	}
	return nil
}

func (d *Driver) Resolve(ctx context.Context, s application.Source) (state.Revision, error) {
	fail := func() (state.Revision, error) { return state.Revision{}, ErrSource }
	if application.ValidateSource(s) != nil {
		return state.Revision{}, ErrInvalid
	}
	id := identity(s.URL)
	base := "git-cache/" + strings.TrimPrefix(id, "sha256:")
	if d.mkdir(base) != nil {
		return fail()
	}
	lock, e := d.lock(ctx, base+"/lock")
	if e != nil {
		return fail()
	}
	defer unlock(lock)
	repo, e := d.path(base+"/repository", false)
	if e != nil {
		return fail()
	}
	ref := s.Ref
	pinned := oidPattern.MatchString(strings.ToLower(ref))
	if pinned {
		ref = strings.ToLower(ref)
	}
	if pinned {
		if _, e = d.readTree(ctx, repo, ref); e == nil {
			return state.Revision{Driver: "git", SourceIdentity: id, Revision: ref}, nil
		}
	}
	if _, e = os.Lstat(repo); os.IsNotExist(e) {
		format := "sha1"
		if pinned {
			if len(ref) == 64 {
				format = "sha256"
			}
		} else {
			out, e := gitCommand(ctx, "", "ls-remote", "--refs", "--", s.URL, ref)
			if e != nil {
				return fail()
			}
			fields := strings.Fields(string(out))
			if len(fields) != 2 || fields[1] != ref || !oidPattern.MatchString(fields[0]) {
				return fail()
			}
			if len(fields[0]) == 64 {
				format = "sha256"
			}
		}
		if _, e = gitCommand(ctx, "", "init", "--bare", "--template=", "--object-format="+format, repo); e != nil {
			return fail()
		}
	}
	if e = d.checkCache(ctx, repo); e != nil {
		return fail()
	}
	if _, e = gitCommand(ctx, repo, "fetch", "--no-tags", "--no-recurse-submodules", "--force", "--", s.URL, ref); e != nil {
		return fail()
	}
	out, e := gitCommand(ctx, repo, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if e != nil {
		return fail()
	}
	commit := strings.TrimSpace(string(out))
	if !oidPattern.MatchString(commit) || pinned && commit != ref {
		return fail()
	}
	if _, e = d.readTree(ctx, repo, commit); e != nil {
		return fail()
	}
	if d.boundary(ctx, "fetch-verified") != nil {
		return fail()
	}
	return state.Revision{Driver: "git", SourceIdentity: id, Revision: commit}, nil
}

// Git is used only as an object transport/reader. No checkout, hooks or filters.
// Host credential/SSH facilities remain available; raw diagnostics are discarded.
func gitCommand(ctx context.Context, repo string, args ...string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ErrSource
	}
	fixed := []string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.allow=never", "-c", "protocol.file.allow=always", "-c", "protocol.https.allow=always", "-c", "protocol.ssh.allow=always", "-c", "fetch.fsckObjects=true", "-c", "transfer.fsckObjects=true", "-c", "fetch.recurseSubmodules=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false"}
	if repo != "" {
		fixed = append(fixed, "--git-dir="+repo)
	}
	c := exec.CommandContext(ctx, "git", append(fixed, args...)...)
	for _, v := range os.Environ() {
		key := strings.SplitN(v, "=", 2)[0]
		if !strings.HasPrefix(key, "GIT_") || key == "GIT_SSH" || key == "GIT_SSH_COMMAND" || key == "GIT_ASKPASS" || key == "GIT_CONFIG_GLOBAL" || key == "GIT_CONFIG_SYSTEM" {
			c.Env = append(c.Env, v)
		}
	}
	c.Env = append(c.Env, "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "GIT_CONFIG_NOSYSTEM=0")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.WaitDelay = time.Second
	var out bytes.Buffer
	c.Stdout = &out
	if e := c.Run(); e != nil {
		return nil, ErrSource
	}
	if ctx.Err() != nil {
		return nil, ErrSource
	}
	return out.Bytes(), nil
}
func (d *Driver) lock(ctx context.Context, rel string) (*os.File, error) {
	p, e := d.path(rel, false)
	if e != nil {
		return nil, ErrSource
	}
	fd, e := syscall.Open(p, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return nil, ErrSource
	}
	f := os.NewFile(uintptr(fd), "git-cache-lock")
	for {
		e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			return f, nil
		}
		if e != syscall.EWOULDBLOCK {
			f.Close()
			return nil, ErrSource
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ErrSource
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func unlock(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }
func (d *Driver) checkCache(ctx context.Context, repo string) error {
	return filepath.WalkDir(repo, func(p string, ent os.DirEntry, e error) error {
		if e != nil || ctx.Err() != nil {
			return ErrSource
		}
		relative, err := filepath.Rel(repo, p)
		if err != nil {
			return ErrSource
		}
		// Full source-scoped caches must not borrow objects or lazily fetch from
		// alternate/promisor stores. These files are never produced by this adapter.
		if relative == "objects/info/alternates" || relative == "objects/info/http-alternates" || relative == "info/grafts" || relative == "shallow" || strings.HasSuffix(relative, ".promisor") {
			return ErrSource
		}
		if ent.Type()&os.ModeSymlink != 0 || !ent.IsDir() && !ent.Type().IsRegular() {
			return ErrSource
		}
		return nil
	})
}
func safeRelative(s string) bool {
	if s == "" || filepath.IsAbs(s) || strings.ContainsRune(s, 0) {
		return false
	}
	for _, p := range strings.Split(s, "/") {
		if p == ".." || strings.EqualFold(p, ".git") {
			return false
		}
	}
	return true
}

// The canonical store and its parents are trusted, private, same-account paths.
// Refuse symlink traversal in every adapter-owned directory or retained reference.
func (d *Driver) path(rel string, mustExist bool) (string, error) {
	if !safeRelative(rel) {
		return "", ErrInvalid
	}
	p := d.root
	info, e := os.Lstat(p)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return "", ErrSource
	}
	for _, part := range strings.Split(filepath.Clean(rel), "/") {
		p = filepath.Join(p, part)
		info, e = os.Lstat(p)
		if os.IsNotExist(e) && !mustExist {
			continue
		}
		if e != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", ErrSource
		}
	}
	return p, nil
}
func (d *Driver) mkdir(rel string) error {
	p, e := d.path(rel, false)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(p, 0700); e != nil {
		return ErrSource
	}
	return nil
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
