//go:build linux

package state

// Test-only access to isolated roots: production still uses the canonical root.
func InitializeForTest(root string) error { return initialize(root, "test-installation") }
func OpenForTest(root string, owner Ownership, enroll bool) (*Store, error) {
	return open(root, owner, enroll)
}
func FaultForTest(s *Store, fault func(string) error) { s.fault = fault }
