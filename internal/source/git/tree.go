package git

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

type entry struct {
	Name, Mode string
	Data       []byte
}
type tree struct {
	Version                          int
	Driver, SourceIdentity, Revision string
	Entries                          []entry
}

func (d *Driver) readTree(ctx context.Context, repo, commit string) (*tree, error) {
	if !oidPattern.MatchString(commit) || d.checkCache(ctx, repo) != nil {
		return nil, ErrSource
	}
	if _, e := gitCommand(ctx, repo, "fsck", "--full", "--strict", "--no-reflogs", commit); e != nil {
		return nil, ErrSource
	}
	out, e := gitCommand(ctx, repo, "cat-file", "-t", commit)
	if e != nil || string(out) != "commit\n" {
		return nil, ErrSource
	}
	out, e = gitCommand(ctx, repo, "ls-tree", "-r", "-t", "-z", "--full-tree", commit)
	if e != nil {
		return nil, ErrSource
	}
	t := &tree{Version: 1, Driver: "git", Revision: commit, Entries: []entry{{Name: ".", Mode: "040000"}}}
	for _, record := range strings.Split(string(out), "\x00") {
		if record == "" {
			continue
		}
		header, name, ok := strings.Cut(record, "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 || !safeRelative(name) || path.Clean(name) != name {
			return nil, ErrInvalid
		}
		en := entry{Name: name, Mode: fields[0]}
		switch en.Mode {
		case "040000", "160000":
		case "100644", "100755", "120000":
			data, e := gitCommand(ctx, repo, "cat-file", "blob", fields[2])
			if e != nil {
				return nil, ErrSource
			}
			object := append([]byte("blob "+strconv.Itoa(len(data))+"\x00"), data...)
			var sum string
			if len(commit) == 40 {
				sum = fmt.Sprintf("%x", sha1.Sum(object))
			} else {
				sum = fmt.Sprintf("%x", sha256.Sum256(object))
			}
			if sum != fields[2] {
				return nil, ErrSource
			}
			en.Data = data
		default:
			return nil, ErrInvalid
		}
		t.Entries = append(t.Entries, en)
	}
	return t, nil
}
func (t *tree) index() (map[string]entry, error) {
	if t.Version != 1 || t.Driver != "git" || !oidPattern.MatchString(t.Revision) {
		return nil, ErrRetained
	}
	m := map[string]entry{}
	for _, e := range t.Entries {
		if !safeRelative(e.Name) || path.Clean(e.Name) != e.Name {
			return nil, ErrInvalid
		}
		if _, ok := m[e.Name]; ok {
			return nil, ErrInvalid
		}
		switch e.Mode {
		case "040000", "160000":
			if len(e.Data) != 0 {
				return nil, ErrInvalid
			}
		case "100644", "100755", "120000":
		default:
			return nil, ErrInvalid
		}
		m[e.Name] = e
	}
	if m["."].Mode != "040000" {
		return nil, ErrInvalid
	}
	for name := range m {
		if name != "." && m[path.Dir(name)].Mode != "040000" {
			return nil, ErrInvalid
		}
	}
	return m, nil
}

// resolve implements repository-relative symlink resolution without touching the
// host filesystem. Dot-dot is interpreted after preceding symlinks, not before.
func resolve(m map[string]entry, name string, seen map[string]bool) (string, error) {
	hops := 0
	return resolvePath(m, name, seen, &hops)
}
func resolvePath(m map[string]entry, name string, seen map[string]bool, hops *int) (string, error) {
	if strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) {
		return "", ErrInvalid
	}
	current := "."
	parts := strings.Split(name, "/")
	for n, p := range parts {
		if m[current].Mode != "040000" {
			return "", ErrInvalid
		}
		if p == "" || p == "." {
			continue
		}
		if p == ".." {
			if current == "." {
				return "", ErrInvalid
			}
			current = path.Dir(current)
			continue
		}
		current = path.Join(current, p)
		e, ok := m[current]
		if !ok {
			return "", ErrInvalid
		}
		if e.Mode == "120000" {
			// Linux permits at most 40 link traversals for one filesystem lookup.
			// Track active targets separately from suffix traversal: a repeated link
			// in a finite path (alias/../alias/file) is not itself a cycle.
			*hops += 1
			if *hops > 40 || seen[current] || len(e.Data) == 0 || strings.HasPrefix(string(e.Data), "/") {
				return "", ErrInvalid
			}
			seen[current] = true
			target, err := resolvePath(m, path.Dir(current)+"/"+string(e.Data), seen, hops)
			delete(seen, current)
			if err != nil {
				return "", err
			}
			if n+1 < len(parts) {
				return resolvePath(m, target+"/"+strings.Join(parts[n+1:], "/"), seen, hops)
			}
			return target, nil
		}
	}
	return current, nil
}
func lfs(data []byte) bool {
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSuffix(lines[0], "\r") != "version https://git-lfs.github.com/spec/v1" {
		return false
	}
	oid, size := false, false
	for _, line := range lines[1:] {
		oid = oid || strings.HasPrefix(line, "oid sha256:")
		size = size || strings.HasPrefix(line, "size ")
	}
	return oid && size
}
func (t *tree) validate(ctx context.Context, selected string, inputs []string) error {
	_, err := t.closure(ctx, selected, inputs)
	return err
}
func (t *tree) closure(ctx context.Context, selected string, inputs []string) ([]string, error) {
	m, e := t.index()
	if e != nil {
		return nil, e
	}
	for name, en := range m {
		if ctx.Err() != nil {
			return nil, ErrSource
		}
		if en.Mode == "120000" {
			if _, e = resolve(m, name, map[string]bool{}); e != nil {
				return nil, e
			}
		}
	}
	if !safeRelative(selected) {
		return nil, ErrInvalid
	}
	s, e := resolve(m, selected, map[string]bool{})
	if e != nil || m[s].Mode != "040000" {
		return nil, ErrInvalid
	}
	children := map[string][]string{}
	for name := range m {
		if name != "." {
			children[path.Dir(name)] = append(children[path.Dir(name)], name)
		}
	}
	done, active := map[string]bool{}, map[string]bool{}
	roots := map[string]bool{s: true}
	var visit func(string) error
	visit = func(name string) error {
		if ctx.Err() != nil {
			return ErrSource
		}
		resolved, e := resolve(m, name, map[string]bool{})
		if e != nil {
			return e
		}
		if resolved != name {
			roots[resolved] = true
		}
		if active[resolved] {
			return ErrInvalid
		}
		if done[resolved] {
			return nil
		}
		active[resolved] = true
		en := m[resolved]
		if en.Mode == "160000" || lfs(en.Data) {
			return ErrInvalid
		}
		if en.Mode == "040000" {
			for _, child := range children[resolved] {
				if e = visit(child); e != nil {
					return e
				}
			}
		}
		delete(active, resolved)
		done[resolved] = true
		return nil
	}
	for _, input := range append([]string{selected}, inputs...) {
		if input == "" {
			return nil, ErrInvalid
		}
		root, err := resolve(m, input, map[string]bool{})
		if err != nil {
			return nil, err
		}
		roots[root] = true
		if e = visit(input); e != nil {
			return nil, e
		}
	}
	result := make([]string, 0, len(roots))
	for root := range roots {
		result = append(result, root)
	}
	sort.Strings(result)
	return result, nil
}
