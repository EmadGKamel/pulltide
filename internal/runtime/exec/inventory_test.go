//go:build linux

package execruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func privateDir(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	if os.Chmod(p, 0700) != nil {
		t.Fatal("private fixture")
	}
	return p
}
func write(t *testing.T, p, s string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(s), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestRecoveryInventory(t *testing.T) {
	for _, tc := range []struct {
		name, json string
		valid      bool
	}{
		{"empty", `{"version":1,"files":[]}`, true}, {"file", `{"version":1,"files":["generated/marker"]}`, true},
		{"missing-version", `{"files":[]}`, false}, {"null", `{"version":1,"files":null}`, false}, {"missing-files", `{"version":1}`, false},
		{"bad-version", `{"version":2,"files":[]}`, false}, {"float-version", `{"version":1.0,"files":[]}`, false},
		{"duplicate-key", `{"version":1,"version":1,"files":[]}`, false}, {"duplicate-file", `{"version":1,"files":["generated/marker","generated/marker"]}`, false},
		{"traversal", `{"version":1,"files":["../outside"]}`, false}, {"absolute", `{"version":1,"files":["/tmp/file"]}`, false},
		{"dot", `{"version":1,"files":["./generated/marker"]}`, false}, {"missing", `{"version":1,"files":["missing"]}`, false},
		{"self", `{"version":1,"files":["app/.pulltide-exec-recovery.json"]}`, false}, {"unknown", `{"version":1,"files":[],"secret":"fake"}`, false},
		{"trailing", `{"version":1,"files":[]} {}`, false},
		{"nul", `{"version":1,"files":["a\u0000b"]}`, false},
		{"surrogate", `{"version":1,"files":["\ud800"]}`, false},
		{"noncanonical", `{"version":1,"files":["generated//marker"]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateDir(t)
			write(t, filepath.Join(root, "generated/marker"), "saved")
			write(t, filepath.Join(root, "app", inventoryName), tc.json)
			refs, e := inventory(context.Background(), root, "app", nil, nil)
			if (e == nil) != tc.valid {
				t.Fatal("inventory acceptance", e)
			}
			if tc.valid && len(refs) == 0 {
				t.Fatal("inventory itself must be retained")
			}
		})
	}
}
func TestInventoryUnsafeFilesAndDurability(t *testing.T) {
	for _, kind := range []string{"missing", "symlink", "directory", "hardlink", "writable", "sync", "directory-sync"} {
		t.Run(kind, func(t *testing.T) {
			root := privateDir(t)
			write(t, filepath.Join(root, "app", inventoryName), `{"version":1,"files":["marker"]}`)
			write(t, filepath.Join(root, "marker"), "saved")
			switch kind {
			case "missing":
				if e := os.Remove(filepath.Join(root, inventoryName)); !os.IsNotExist(e) {
					t.Fatal(e)
				}
				if e := os.Remove(filepath.Join(root, "app", inventoryName)); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e := os.Symlink("../marker", filepath.Join(root, "app/alias")); e != nil {
					t.Fatal(e)
				}
				write(t, filepath.Join(root, "app", inventoryName), `{"version":1,"files":["app/alias"]}`)
			case "directory":
				write(t, filepath.Join(root, "app", inventoryName), `{"version":1,"files":["app"]}`)
			case "hardlink":
				if e := os.Link(filepath.Join(root, "marker"), filepath.Join(root, "alias")); e != nil {
					t.Fatal(e)
				}
			case "writable":
				if e := os.Chmod(filepath.Join(root, "marker"), 0666); e != nil {
					t.Fatal(e)
				}
			}
			var fault func(string) error
			if kind == "sync" {
				fault = func(string) error { return ErrRuntime }
			}
			if kind == "directory-sync" {
				fault = func(point string) error {
					if point == "directory-sync" {
						return ErrRuntime
					}
					return nil
				}
			}
			if refs, e := inventory(context.Background(), root, "app", nil, fault); e == nil || len(refs) != 0 {
				t.Fatal("unsafe inventory published references")
			}
		})
	}
	root := privateDir(t)
	write(t, filepath.Join(root, inventoryName), strings.Repeat(" ", inventoryLimit+1))
	if _, e := inventory(context.Background(), root, ".", nil, nil); e == nil {
		t.Fatal("oversized inventory")
	}
}
