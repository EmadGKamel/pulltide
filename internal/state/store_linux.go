//go:build linux

package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

type Store struct {
	mu                 sync.Mutex
	rootPath           string
	root               *os.File
	lock               *os.File
	owner              Ownership
	state              *State
	closed             bool
	poisoned           bool
	fault              func(string) error
	writeRecord        func(*os.File, []byte) (int, error)
	recoveryStarted    bool
	recoveryFailed     bool
	reopenedJournal    bool
	freshRestorationID string
	observationStarted bool
}

// Initialize requires explicit operator confirmation, including first install.
// It refuses an existing registry or nonempty private store, never repairs loss.
// No command exposes this API and it neither inspects nor adopts runtime resources.
func Initialize(operatorConfirmedNoPriorMutatorsOrResources bool) error {
	if !operatorConfirmedNoPriorMutatorsOrResources {
		return ErrOwnership
	}
	id, err := NewID()
	if err != nil {
		return err
	}
	return initialize(Root, id)
}

// Open only loads an existing binding. Resource identity must come from verified
// driver/operator context; matching strings alone do not inspect a runtime.
func Open(owner Ownership) (*Store, error) { return open(Root, owner, false) }

// Enroll is only for a verified vacant resource, never adoption. A crash between
// state and binding publication fails closed and needs explicit operator repair.
func Enroll(owner Ownership, resourceVerifiedAbsent bool) (*Store, error) {
	if !resourceVerifiedAbsent {
		return nil, ErrOwnership
	}
	return open(Root, owner, true)
}

func initialize(root, id string) error {
	if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
		return ErrPersistence
	}
	dir, err := openRoot(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	host, err := takeLock(dir, ".artifact.lock")
	if err != nil {
		return err
	}
	defer unlock(host)
	entries, err := os.ReadDir(root)
	if err != nil {
		return ErrPersistence
	}
	for _, entry := range entries {
		if entry.Name() != ".artifact.lock" {
			return ErrOwnership
		}
	}
	s := &Store{root: dir}
	r := registry{FormatVersion, id, os.Geteuid(), map[string]Ownership{}}
	data, err := encode(r)
	if err != nil {
		return err
	}
	if err := s.replace("registry", data); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(root))
	if err != nil {
		return ErrPersistence
	}
	defer parent.Close()
	if err := parent.Sync(); err != nil {
		return ErrPersistence
	}
	return nil
}
func open(root string, owner Ownership, enroll bool) (*Store, error) {
	if !validOwner(owner) {
		return nil, ErrOwnership
	}
	dir, err := openRoot(root)
	if err != nil {
		return nil, err
	}
	s := &Store{rootPath: root, root: dir, owner: owner}
	if _, err := s.readRegistry(); err != nil {
		dir.Close()
		return nil, err
	}
	lock, err := takeLock(dir, owner.Name+".lock")
	if err != nil {
		dir.Close()
		return nil, err
	}
	s.lock = lock
	fail := func(err error) (*Store, error) { s.Close(); return nil, err }
	host, err := takeLock(dir, ".artifact.lock")
	if err != nil {
		return fail(err)
	}
	defer unlock(host)
	r, err := s.readRegistry()
	if err != nil {
		return fail(err)
	}
	binding, exists := r.Bindings[owner.Name]
	if exists && binding != owner {
		return fail(ErrOwnership)
	}
	data, readErr := readPrivate(dir, owner.Name+".state")
	if !exists {
		if !enroll {
			return fail(ErrOwnership)
		}
		if readErr == nil || !errors.Is(readErr, os.ErrNotExist) {
			return fail(ErrRecoveryRequired)
		}
		for _, existing := range r.Bindings {
			if existing.Runtime == owner.Runtime && existing.DaemonIdentity == owner.DaemonIdentity && existing.ResourceIdentity == owner.ResourceIdentity {
				return fail(ErrOwnership)
			}
		}
		s.state = &State{Version: FormatVersion, Generation: 1, InstallationID: r.InstallationID, Ownership: owner, DesiredStale: true, Active: Active{State: "absent"}, Health: Health{State: "not-applicable"}, Phase: "idle", Instances: map[string]Instance{}}
		if err := s.state.Validate(); err != nil {
			return fail(err)
		}
		encoded, _ := encode(s.state)
		if err := s.replace(owner.Name+".state", encoded); err != nil {
			return fail(err)
		}

		r.Bindings[owner.Name] = owner
		encoded, _ = encode(r)
		if err := s.replace("registry", encoded); err != nil {
			return fail(err)
		}
	} else {
		if readErr != nil {
			return fail(ErrRecoveryRequired)
		}
		var st State
		if err := decode(data, &st); err != nil {
			return fail(err)
		}
		if st.Ownership != owner || st.InstallationID != r.InstallationID {
			return fail(ErrOwnership)
		}
		if err := st.Validate(); err != nil {
			return fail(err)
		}
		if err := s.verifyInputs(&st, false, nil); err != nil {
			return fail(err)
		}
		s.reopenedJournal = st.Journal != nil
		if st.Active.State == "known" {
			st.Health = Health{State: "unknown"}
		}
		st.DesiredStale = true
		s.state = &st
	}
	return s, nil
}
func (s *Store) readRegistry() (*registry, error) {
	data, err := readPrivate(s.root, "registry")
	if err != nil {
		return nil, ErrOwnership
	}
	var r registry
	if err := decode(data, &r); err != nil {
		return nil, err
	}
	if r.Version != FormatVersion || r.UID != os.Geteuid() || !validText(r.InstallationID) || r.Bindings == nil {
		return nil, ErrOwnership
	}
	resources := map[string]bool{}
	for name, binding := range r.Bindings {
		key := binding.Runtime + "\x00" + binding.DaemonIdentity + "\x00" + binding.ResourceIdentity
		if resources[key] {
			return nil, ErrOwnership
		}
		resources[key] = true
		if binding.Name != name || !validOwner(binding) {
			return nil, ErrOwnership
		}
	}
	return &r, nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	unlock(s.lock)
	if s.root != nil {
		if err := s.root.Close(); err != nil {
			return ErrPersistence
		}
	}
	return nil
}
func (s *Store) Read() (*State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.poisoned {
		return nil, ErrRecoveryRequired
	}
	return copyState(s.state)
}

// update owns copy-on-write, validation and the host-wide reference publication
// lock. A failed durability barrier poisons the handle until close/reopen.
func (s *Store) update(change func(*State) error, synchronize bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.poisoned {
		return ErrRecoveryRequired
	}
	host, err := takeLock(s.root, ".artifact.lock")
	if err != nil {
		return err
	}
	defer unlock(host)
	r, err := s.readRegistry()
	if err != nil {
		return err
	}
	if r.InstallationID != s.state.InstallationID || r.Bindings[s.owner.Name] != s.owner {
		return ErrOwnership
	}
	next, err := copyState(s.state)
	if err != nil {
		return err
	}
	if err := change(next); err != nil {
		return err
	}
	next.Generation = s.state.Generation + 1
	next, err = copyState(next)
	if err != nil {
		return err
	}
	if err := validateTransition(s.state, next); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if err := s.verifyInputs(next, synchronize, workspaceBarriers(s.state, next)); err != nil {
		s.poisoned = true
		s.persistFailureEvidence()
		return err
	}
	data, err := encode(next)
	if err != nil {
		return err
	}
	if err := s.replace(s.owner.Name+".state", data); err != nil {
		s.poisoned = true
		s.persistFailureEvidence()
		return err
	}
	s.state = next
	return nil
}

func (s *Store) inject(point string) error {
	if s.fault != nil {
		if err := s.fault(point); err != nil {
			return ErrPersistence
		}
	}
	return nil
}
func (s *Store) replace(name string, data []byte) error {
	if err := s.inject("create"); err != nil {
		return err
	}
	id, err := NewID()
	if err != nil {
		return err
	}
	temp := ".pending-" + id
	fd, err := syscall.Openat(int(s.root.Fd()), temp, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return ErrPersistence
	}
	file := os.NewFile(uintptr(fd), temp)
	defer func() { file.Close(); syscall.Unlinkat(int(s.root.Fd()), temp) }()
	if err := s.inject("write"); err != nil {
		return err
	}
	var n int
	if s.writeRecord != nil {
		n, err = s.writeRecord(file, data)
	} else {
		n, err = file.Write(data)
	}
	if err != nil || n != len(data) {
		return ErrPersistence
	}
	if err := s.inject("file-sync"); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return ErrPersistence
	}
	if err := s.inject("close"); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return ErrPersistence
	}
	if err := s.inject("rename"); err != nil {
		return err
	}
	if err := syscall.Renameat(int(s.root.Fd()), temp, int(s.root.Fd()), name); err != nil {
		return ErrPersistence
	}
	if err := s.inject("directory-sync"); err != nil {
		return err
	}
	if err := s.root.Sync(); err != nil {
		return ErrPersistence
	}
	return nil
}
func openRoot(root string) (*os.File, error) {
	current, err := os.Open("/")
	if err != nil {
		return nil, ErrOwnership
	}
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(root), "/"), "/") {
		fd, err := syscall.Openat(int(current.Fd()), part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		current.Close()
		if err != nil {
			return nil, ErrOwnership
		}
		current = os.NewFile(uintptr(fd), part)
	}
	if !private(current, true) {
		current.Close()
		return nil, ErrOwnership
	}
	return current, nil
}
func private(f *os.File, directory bool) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0077 == 0 && ((directory && info.IsDir()) || (!directory && info.Mode().IsRegular()))
}
func takeLock(root *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(root.Fd()), name, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, ErrOwnership
	}
	f := os.NewFile(uintptr(fd), name)
	if !private(f, false) {
		f.Close()
		return nil, ErrOwnership
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, ErrLocked
		}
		return nil, ErrOwnership
	}
	if err := f.Sync(); err != nil {
		unlock(f)
		return nil, ErrPersistence
	}
	if err := root.Sync(); err != nil {
		unlock(f)
		return nil, ErrPersistence
	}
	return f, nil
}
func unlock(f *os.File) {
	if f != nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}
func readPrivate(root *os.File, name string) ([]byte, error) {
	fd, err := syscall.Openat(int(root.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	if !private(f, false) {
		return nil, ErrOwnership
	}
	return io.ReadAll(f)
}
func openInput(root *os.File, path string, directory bool) (*os.File, error) {
	if !validPath(path) {
		return nil, ErrCorrupt
	}
	fd, err := syscall.Openat(int(root.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrPersistence
	}
	current := os.NewFile(uintptr(fd), "private-input")
	parts := strings.Split(path, "/")
	for i, part := range parts {
		flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
		if i < len(parts)-1 || directory {
			flags |= syscall.O_DIRECTORY
		}
		next, err := syscall.Openat(int(current.Fd()), part, flags, 0)
		current.Close()
		if err != nil {
			if err == syscall.ENOENT {
				return nil, errors.Join(ErrRecoveryRequired, os.ErrNotExist)
			}
			return nil, ErrRecoveryRequired
		}
		current = os.NewFile(uintptr(next), "private-input")
	}
	info, err := current.Stat()
	if err != nil || (!directory && !info.Mode().IsRegular()) {
		current.Close()
		return nil, ErrRecoveryRequired
	}
	return current, nil
}
func (s *Store) verifyInputs(st *State, synchronize bool, workspaces map[string]bool) error {
	for _, instance := range st.Instances {
		workspace, err := openInput(s.root, instance.Workspace, true)
		reserved := st.Journal != nil && st.Journal.Kind == "deployment" && st.Journal.RestorationID == instance.ID && st.Journal.Stage == "restore-prepare-intent" && st.Journal.Operation != nil && st.Journal.Operation.Result != "completed-success"
		if err != nil && !(reserved && errors.Is(err, os.ErrNotExist)) {
			return ErrRecoveryRequired
		}
		if reserved && workspace == nil && errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		if workspace != nil && synchronize && workspaces[instance.ID] {
			err = s.syncTree(workspace)
		}
		if workspace != nil {
			workspace.Close()
		}
		if workspace != nil && synchronize && workspaces[instance.ID] && err == nil {
			err = s.syncParents(instance.Workspace)
		}
		if err != nil {
			return err
		}
		checkRecoveryFiles := true
		if j := st.Journal; j != nil && j.Operation != nil && operationCovers(*j.Operation, instance.ID) && j.Operation.Result != "completed-success" {
			checkRecoveryFiles = false
		}
		for _, path := range instance.RequiredRecoveryFiles {
			if !checkRecoveryFiles {
				continue
			}
			file, err := openInput(s.root, filepath.Join(instance.Workspace, path), false)
			if err != nil {
				return ErrRecoveryRequired
			}
			file.Close()
		}
		for _, ref := range instance.Inputs {
			f, err := openInput(s.root, ref.Path, false)
			if err != nil {
				return ErrRecoveryRequired
			}
			hash := sha256.New()
			_, err = io.Copy(hash, f)
			if err == nil && synchronize {
				err = f.Sync()
			}
			f.Close()
			if synchronize && err == nil {
				err = s.syncParents(ref.Path)
			}
			if err != nil || hex.EncodeToString(hash.Sum(nil)) != ref.SHA256 {
				return ErrRecoveryRequired
			}
		}
	}
	if synchronize {
		if err := s.root.Sync(); err != nil {
			return ErrPersistence
		}
	}
	return nil
}
func (s *Store) syncTree(dir *os.File) error {
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return ErrPersistence
	}
	for _, name := range names {
		fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if err == syscall.ELOOP {
			continue
		}
		if err != nil {
			return ErrPersistence
		}
		file := os.NewFile(uintptr(fd), "workspace-entry")
		info, err := file.Stat()
		if err == nil && info.IsDir() {
			err = s.syncTree(file)
		} else if err == nil && info.Mode().IsRegular() {
			err = s.inject("workspace-file-sync")
			if err == nil {
				err = file.Sync()
			}
		} else if err == nil {
			err = ErrPersistence
		}
		file.Close()
		if err != nil {
			return ErrPersistence
		}
	}
	if err := s.inject("workspace-directory-sync"); err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		return ErrPersistence
	}
	return nil
}

func workspaceBarriers(old, next *State) map[string]bool {
	ids := map[string]bool{}
	for id := range next.Instances {
		if _, ok := old.Instances[id]; !ok {
			ids[id] = true
		}
	}
	if j := next.Journal; j != nil && j.Operation != nil && j.Operation.Result == "completed-success" {
		prior := old.Journal
		if prior == nil || prior.Operation == nil || prior.Operation.ID != j.Operation.ID || prior.Operation.Result != "completed-success" {
			for _, id := range cleanupOperands(*j.Operation) {
				ids[id] = true
			}
		}
	}
	if next.LastGoodID != old.LastGoodID && next.LastGoodID != "" {
		ids[next.LastGoodID] = true
	}
	return ids
}
func (s *Store) syncParents(path string) error {
	parent := filepath.Dir(path)
	for parent != "." {
		dir, err := openInput(s.root, parent, true)
		if err != nil {
			return err
		}
		err = dir.Sync()
		dir.Close()
		if err != nil {
			return ErrPersistence
		}
		parent = filepath.Dir(parent)
	}
	if err := s.root.Sync(); err != nil {
		return ErrPersistence
	}
	return nil
}

// Best effort only, while holding both locks. Read the visible complete record
// after a possibly successful rename; never overwrite it with stale cached data.
// This accepts no usable checkpoint and cannot unblock the poisoned handle.
func (s *Store) persistFailureEvidence() {
	if s.state == nil || s.state.Journal == nil {
		return
	}
	data, err := readPrivate(s.root, s.owner.Name+".state")
	if err != nil {
		return
	}
	var current State
	if decode(data, &current) != nil || current.Validate() != nil {
		return
	}
	if current.Journal == nil && s.state.Journal != nil {
		generation := current.Generation
		prior, copyErr := copyState(s.state)
		if copyErr != nil {
			return
		}
		current = *prior
		current.Generation = generation
	}
	current.Phase = "recovery-required"
	current.Error = &Diagnostic{Category: "persistence", Stage: "persist"}
	if current.Journal != nil && current.Journal.Operation != nil && current.Journal.Operation.Result == "" {
		current.Journal.Operation.Result = "completion-unknown"
		current.Journal.Operation.QuiescenceConfirmed = false
		current.Journal.HealthWindowResult = ""
	}
	if current.Active.State == "known" {
		current.Health = Health{State: "unknown"}
	}
	current.Generation++
	if current.Validate() != nil {
		return
	}
	encoded, err := encode(current)
	if err == nil {
		_ = s.replace(s.owner.Name+".state", encoded)
	}
}
