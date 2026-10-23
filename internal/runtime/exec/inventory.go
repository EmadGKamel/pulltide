package execruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"unicode/utf8"
)

func inventory(ctx context.Context, workspace, selected string, expected []string, fault func(string) error) ([]string, error) {
	return readInventory(ctx, workspace, selected, expected, true, fault)
}
func readInventory(ctx context.Context, workspace, selected string, expected []string, synchronize bool, fault func(string) error) ([]string, error) {
	_, selected, err := workingDirectory(workspace, selected)
	if err != nil {
		return nil, err
	}
	inventoryPath := filepath.Join(selected, inventoryName)
	file, err := openBeneath(workspace, inventoryPath, false)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, inventoryLimit+1))
	file.Close()
	if err != nil || len(data) > inventoryLimit || !utf8.Valid(data) || !escapedUnicode(data) {
		return nil, ErrRuntime
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrRuntime
	}
	seen := map[string]bool{}
	var files []string
	for decoder.More() {
		key, e := decoder.Token()
		if e != nil {
			return nil, ErrRuntime
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return nil, ErrRuntime
		}
		seen[name] = true
		switch name {
		case "version":
			var version json.RawMessage
			if decoder.Decode(&version) != nil || string(version) != "1" {
				return nil, ErrRuntime
			}
		case "files":
			if decoder.Decode(&files) != nil || files == nil {
				return nil, ErrRuntime
			}
		default:
			return nil, ErrRuntime
		}
	}
	token, err = decoder.Token()
	var trailing any
	if err != nil || token != json.Delim('}') || !seen["version"] || !seen["files"] || decoder.Decode(&trailing) != io.EOF {
		return nil, ErrRuntime
	}
	refs := []string{inventoryPath}
	names := map[string]bool{inventoryPath: true}
	for _, name := range files {
		if !relative(name) || names[name] {
			return nil, ErrRuntime
		}
		names[name] = true
		refs = append(refs, name)
	}
	sort.Strings(refs)
	if expected != nil {
		copy := append([]string{}, expected...)
		sort.Strings(copy)
		if !reflect.DeepEqual(copy, refs) {
			return nil, ErrRuntime
		}
	}
	dirs := map[string]bool{".": true}
	for _, name := range refs {
		if ctx.Err() != nil {
			return nil, ErrRuntime
		}
		f, e := openBeneath(workspace, name, false)
		if e != nil {
			return nil, e
		}
		if synchronize {
			if fault != nil {
				e = fault("file-sync")
			}
			if e == nil {
				e = f.Sync()
			}
		}
		ce := f.Close()
		if e != nil || ce != nil {
			return nil, ErrRuntime
		}
		for parent := filepath.Dir(name); parent != "."; parent = filepath.Dir(parent) {
			dirs[parent] = true
		}
	}
	if synchronize {
		paths := make([]string, 0, len(dirs))
		for name := range dirs {
			paths = append(paths, name)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(paths)))
		for _, name := range paths {
			if ctx.Err() != nil {
				return nil, ErrRuntime
			}
			dir, e := openBeneath(workspace, name, true)
			if e != nil {
				return nil, e
			}
			if fault != nil {
				e = fault("directory-sync")
			}
			if e == nil {
				e = dir.Sync()
			}
			ce := dir.Close()
			if e != nil || ce != nil {
				return nil, ErrRuntime
			}
		}
	}
	if ctx.Err() != nil {
		return nil, ErrRuntime
	}
	return refs, nil
}

// encoding/json replaces lone UTF-16 surrogates; do not let malformed path text
// silently select a different on-disk filename.
func escapedUnicode(data []byte) bool {
	for n := 0; n < len(data); n++ {
		if data[n] != '\\' {
			continue
		}
		if n+1 >= len(data) {
			return false
		}
		if data[n+1] != 'u' {
			n++
			continue
		}
		if n+6 > len(data) {
			return false
		}
		value, e := strconv.ParseUint(string(data[n+2:n+6]), 16, 16)
		if e != nil {
			return false
		}
		n += 5
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if n+7 > len(data) || data[n+1] != '\\' || data[n+2] != 'u' {
				return false
			}
			low, e := strconv.ParseUint(string(data[n+3:n+7]), 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			n += 6
		}
	}
	return true
}
