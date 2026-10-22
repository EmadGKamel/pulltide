//go:build linux

package state

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

func fixtureStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := initialize(root, "installation-test"); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	s, err := open(root, Ownership{Name: "example", Runtime: "exec", ResourceIdentity: "operator-resource"}, true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMissingRegistryAndState(t *testing.T) {
	root := t.TempDir()
	owner := Ownership{Name: "example", Runtime: "exec", ResourceIdentity: "operator-resource"}
	if _, err := open(root, owner, false); !errors.Is(err, ErrOwnership) {
		t.Fatalf("missing registry: %v", err)
	}
	s := fixtureStore(t)
	root = s.rootPath
	s.Close()
	if err := os.Remove(filepath.Join(root, "example.state")); err != nil {
		t.Fatal(err)
	}
	if _, err := open(root, owner, true); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("missing bound state: %v", err)
	}
}

func TestLockingAndConcurrentUpdates(t *testing.T) {
	s := fixtureStore(t)
	if _, err := open(s.rootPath, s.owner, false); !errors.Is(err, ErrLocked) {
		t.Fatalf("second owner: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.updateMetadataForTest(func(st *State) error { st.DesiredStale = true; return nil }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	st, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.Generation != 17 {
		t.Fatalf("generation %d", st.Generation)
	}
	s.Close()
	reopened, err := open(s.rootPath, s.owner, false)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
}

func TestAtomicFailureBoundaries(t *testing.T) {
	for _, point := range []string{"create", "write", "file-sync", "close", "rename", "directory-sync"} {
		t.Run(point, func(t *testing.T) {
			s := fixtureStore(t)
			before, err := s.Read()
			if err != nil {
				t.Fatal(err)
			}
			s.fault = func(at string) error {
				if at == point {
					return syscall.ENOSPC
				}
				return nil
			}
			err = s.updateMetadataForTest(func(st *State) error { st.DesiredStale = true; return nil })
			if !errors.Is(err, ErrPersistence) {
				t.Fatalf("fault not observed: %v", err)
			}
			if err := s.updateMetadataForTest(func(st *State) error { return nil }); !errors.Is(err, ErrRecoveryRequired) {
				t.Fatalf("write after failure: %v", err)
			}
			root, owner := s.rootPath, s.owner
			s.Close()
			again, err := open(root, owner, false)
			if err != nil {
				t.Fatal(err)
			}
			defer again.Close()
			after, err := again.Read()
			if err != nil {
				t.Fatal(err)
			}
			if point == "directory-sync" {
				if after.Generation != before.Generation+1 {
					t.Fatal("renamed complete record missing")
				}
			} else if after.Generation != before.Generation {
				t.Fatal("prior record lost")
			}
		})
	}
}

func TestCorruptUnsupportedAndSymlinkRecords(t *testing.T) {
	for _, body := range []string{"{", `{"version":99}`, `{"version":1,"version":1}`, `null`} {
		t.Run(body, func(t *testing.T) {
			s := fixtureStore(t)
			root, owner := s.rootPath, s.owner
			s.Close()
			if err := os.WriteFile(filepath.Join(root, "example.state"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := open(root, owner, false); err == nil {
				t.Fatal("accepted corrupt record")
			}
		})
	}
	s := fixtureStore(t)
	root, owner := s.rootPath, s.owner
	s.Close()
	os.Remove(filepath.Join(root, "example.state"))
	os.Symlink("outside", filepath.Join(root, "example.state"))
	if _, err := open(root, owner, false); err == nil {
		t.Fatal("followed state symlink")
	}
}

func (s *Store) updateMetadataForTest(fn func(*State) error) error { return s.update(fn, false) }
