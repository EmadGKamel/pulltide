//go:build linux

package reconcile

import (
	"os"
	"pulltide/internal/state"
	"strings"
)

func newBoot(st *state.State) bool {
	if st.Journal == nil {
		return false
	}
	previous := st.Journal.BootID
	if st.Journal.Operation != nil {
		previous = st.Journal.Operation.BootID
	}
	current, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return err == nil && strings.TrimSpace(string(current)) != "" && previous != "" && strings.TrimSpace(string(current)) != previous
}
