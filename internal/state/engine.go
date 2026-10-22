package state

// RecordCycleFailure records only a sanitized core diagnostic. It never changes
// ownership, runtime evidence, saved operands, or the last-good deployment.
func (s *Store) RecordCycleFailure(d Diagnostic, blocked bool) error {
	return s.update(func(st *State) error {
		st.Error = &d
		st.Phase = "failed"
		if blocked || st.Journal != nil || st.Active.State == "unknown" {
			st.Phase = "recovery-required"
		}
		return nil
	}, false)
}

// RecordHealthyNoop clears a source/preparation cycle error only for the exact
// verified active target. Suppressed outcomes retain their error and phase.
func (s *Store) RecordHealthyNoop() error {
	return s.update(func(st *State) error {
		if st.Journal != nil || st.Active.State != "known" || st.Health.State != "healthy" || st.Desired == nil || st.Instances[st.Active.InstanceID].Target != *st.Desired {
			return ErrRecoveryRequired
		}
		if st.Suppressed == nil || *st.Suppressed != *st.Desired {
			st.Error = nil
			st.Phase = "healthy"
		}
		return nil
	}, false)
}
