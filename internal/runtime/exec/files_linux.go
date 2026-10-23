//go:build linux

package execruntime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var ErrRuntime = errors.New("exec runtime validation failed")

const inventoryName = ".pulltide-exec-recovery.json"
const inventoryLimit = 1024 * 1024

func relative(p string) bool {
	if p == "" || p == "." || filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsRune(p, 0) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}
func checked(f *os.File, directory bool) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0022 == 0 && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 &&
		((directory && info.IsDir() && info.Mode().Perm()&0500 == 0500) || (!directory && info.Mode().IsRegular() && info.Mode().Perm()&0400 != 0 && st.Nlink == 1))
}

// openBeneath never follows a symlink, including in intermediate components.
func openBeneath(root, rel string, directory bool) (*os.File, error) {
	if rel != "." && !relative(rel) {
		return nil, ErrRuntime
	}
	fd, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrRuntime
	}
	current := os.NewFile(uintptr(fd), "exec-private-directory")
	if !checked(current, true) {
		current.Close()
		return nil, ErrRuntime
	}
	if rel == "." {
		if !directory {
			current.Close()
			return nil, ErrRuntime
		}
		return current, nil
	}
	parts := strings.Split(rel, "/")
	for n, part := range parts {
		dir := n < len(parts)-1 || directory
		flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
		if dir {
			flags |= syscall.O_DIRECTORY
		}
		fd, err = syscall.Openat(int(current.Fd()), part, flags, 0)
		current.Close()
		if err != nil {
			return nil, ErrRuntime
		}
		current = os.NewFile(uintptr(fd), "exec-private-input")
		if !checked(current, dir) {
			current.Close()
			return nil, ErrRuntime
		}
	}
	return current, nil
}
func workingDirectory(workspace, selected string) (string, string, error) {
	if selected == "" || filepath.IsAbs(selected) || strings.ContainsRune(selected, 0) {
		return "", "", ErrRuntime
	}
	for _, part := range strings.Split(selected, "/") {
		if part == ".." {
			return "", "", ErrRuntime
		}
	}
	full, err := filepath.EvalSymlinks(filepath.Join(workspace, selected))
	if err != nil {
		return "", "", ErrRuntime
	}
	rel, err := filepath.Rel(workspace, full)
	if err != nil || rel != "." && !relative(rel) {
		return "", "", ErrRuntime
	}
	dir, err := openBeneath(workspace, rel, true)
	if err != nil {
		return "", "", err
	}
	dir.Close()
	return full, rel, nil
}
