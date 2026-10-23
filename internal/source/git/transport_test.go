package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The SSH fixture implements local upload-pack transport. No network, SSH server,
// credentials, or dependency downloads are used.
func sshFixture(t *testing.T, p string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "ssh")
	args := filepath.Join(dir, "args")
	put(t, script, `#!/bin/sh
case " $* " in *" -G "*) exit 1;; esac
printf '%s\n' "$@" > "$PULLTIDE_GIT_TEST_ARGS"
case "$PULLTIDE_GIT_TEST_MODE" in
 denied) printf 'FAKE_CREDENTIAL_SECRET\n' >&2; exit 255;;
 timeout) exec sleep 30;;
 interrupted) git-upload-pack "$PULLTIDE_GIT_TEST_REPO" | head -c 32; exit 1;;
esac
exec git-upload-pack "$PULLTIDE_GIT_TEST_REPO"
`, 0700)
	t.Setenv("GIT_SSH_COMMAND", script)
	t.Setenv("PULLTIDE_GIT_TEST_REPO", p)
	t.Setenv("PULLTIDE_GIT_TEST_ARGS", args)
	t.Setenv("PULLTIDE_GIT_TEST_MODE", "")
	return "ssh://operator@example.invalid/source", args
}
func TestSSHAuthenticationTimeoutInterruptionAndRetry(t *testing.T) {
	p, commit := repository(t, "sha1")
	url, args := sshFixture(t, p)
	d := driver(t)
	s := source(p, "refs/heads/main")
	s.URL = url
	first, err := d.Resolve(context.Background(), s)
	if err != nil || first.Revision != commit {
		t.Fatal("initial SSH transport", err)
	}
	selection, err := os.ReadFile(args)
	if err != nil || !strings.Contains(string(selection), "operator@example.invalid") {
		t.Fatal("SSH username was not selected", err)
	}
	put(t, filepath.Join(p, "app/run"), "new branch", 0755)
	command(t, p, "add", ".")
	command(t, p, "commit", "-m", "new")
	next := command(t, p, "rev-parse", "HEAD")
	for _, mode := range []string{"denied", "timeout", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("PULLTIDE_GIT_TEST_MODE", mode)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			r, e := d.Resolve(ctx, s)
			if e == nil || r.Revision != "" {
				t.Fatal("failed transport returned cached branch")
			}
			if strings.Contains(e.Error(), "FAKE_CREDENTIAL_SECRET") || strings.Contains(e.Error(), url) {
				t.Fatal("transport diagnostics exposed secret or URL")
			}
		})
	}
	t.Setenv("PULLTIDE_GIT_TEST_MODE", "")
	r, err := d.Resolve(context.Background(), s)
	if err != nil || r.Revision != next {
		t.Fatal("successful poll after transport failure", err)
	}
}
func TestColdExactCommitAndSourceIdentityAcrossRestart(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			p, commit := repository(t, format)
			d := driver(t)
			s := source(p, strings.ToUpper(commit))
			first, err := d.Resolve(context.Background(), s)
			if err != nil || first.Revision != commit {
				t.Fatal("cold pinned retrieval", err)
			}
			reopened := &Driver{root: d.root}
			again, err := reopened.Resolve(context.Background(), s)
			if err != nil || again != first {
				t.Fatal("restart identity changed", err)
			}
			alias := s
			alias.URL += "/."
			other, err := reopened.Resolve(context.Background(), alias)
			if err != nil || other.Revision != commit || other.SourceIdentity == first.SourceIdentity {
				t.Fatal("URL identities collapsed", err)
			}
			s.Ref = strings.Repeat("f", len(commit))
			if _, err = d.Resolve(context.Background(), s); err == nil {
				t.Fatal("absent commit fell back to branch")
			}
		})
	}
}
func TestHooksFiltersAndCacheWorkFilesCannotChangeBytes(t *testing.T) {
	p, _ := repository(t, "sha1")
	d := driver(t)
	s := source(p, "refs/heads/main")
	r, err := d.Resolve(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(d.root, "git-cache", strings.TrimPrefix(r.SourceIdentity, "sha256:"), "repository")
	marker := filepath.Join(t.TempDir(), "hook-ran")
	put(t, filepath.Join(cache, "hooks/post-checkout"), "#!/bin/sh\ntouch '"+marker+"'\n", 0700)
	put(t, filepath.Join(cache, "app/run"), "modified cache work file", 0700)
	put(t, filepath.Join(cache, "untracked"), "untracked cache bytes", 0600)
	command(t, cache, "config", "core.autocrlf", "true")
	command(t, cache, "config", "filter.test.smudge", "false")
	put(t, filepath.Join(p, ".gitattributes"), "app/run filter=test text eol=lf\n", 0600)
	command(t, p, "add", ".gitattributes")
	command(t, p, "commit", "-m", "attributes")
	i := prepare(t, d, s, "staging/filtered")
	b, err := os.ReadFile(filepath.Join(d.root, i.Workspace, "app/run"))
	if err != nil || string(b) != "exact\r\nbytes\x00" {
		t.Fatal("Git configuration changed blob bytes", err)
	}
	for _, name := range []string{filepath.Join(d.root, i.Workspace, "untracked"), marker} {
		if _, err = os.Stat(name); !os.IsNotExist(err) {
			t.Fatal("hook or untracked file entered materialization", name, err)
		}
	}
}

func TestCacheAlternatesCannotSupplyAnotherSource(t *testing.T) {
	p, commit := repository(t, "sha1")
	d := driver(t)
	b := source(p, commit)
	if _, err := d.Resolve(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	a := b
	a.URL += "/."
	cacheA := filepath.Join(d.root, "git-cache", strings.TrimPrefix(identity(a.URL), "sha256:"), "repository")
	cacheB := filepath.Join(d.root, "git-cache", strings.TrimPrefix(identity(b.URL), "sha256:"), "repository")
	command(t, d.root, "init", "--bare", "--template=", cacheA)
	put(t, filepath.Join(cacheA, "objects/info/alternates"), filepath.Join(cacheB, "objects")+"\n", 0600)
	if err := os.Rename(p, p+"-offline"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Rename(p+"-offline", p); err != nil {
			t.Error(err)
		}
	})
	if _, err := d.Resolve(context.Background(), a); err == nil {
		t.Fatal("another source's alternate objects became offline eligible")
	}
}
