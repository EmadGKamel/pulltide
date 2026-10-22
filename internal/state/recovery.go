package state

// RecoveryInspection describes 007 restart obligations, not an executable plan
// or permission to mutate. It does not prove termination, validate resources,
// run a check, or promote a deployment. Driver evidence remains mandatory.
type RecoveryInspection struct {
	Action                    string
	RequiresQuiescence        bool
	RequiresInputVerification bool
}

func InspectRecovery(s *State) (RecoveryInspection, error) {
	if err := s.Validate(); err != nil {
		return RecoveryInspection{}, err
	}
	r := RecoveryInspection{RequiresInputVerification: true}
	j := s.Journal
	if j == nil {
		switch s.Active.State {
		case "known":
			r.Action = "startup-observation"
		case "absent":
			r.Action = "eligibility"
		default:
			r.Action = "blocked"
		}
		return r, nil
	}
	if j.Stage == "prepared" {
		r.Action = "abandon-prepared"
		return r, nil
	}
	result := ""
	if j.Operation != nil {
		result = j.Operation.Result
	}
	r.RequiresQuiescence = result == "" || result == "completion-unknown"
	switch j.Stage {
	case "artifact-intent":
		r.Action = "abandon-preparation"
	case "startup-verify-intent":
		r.Action = "fresh-startup-observation"
	case "prior-stop-intent":
		if result == "completed-success" {
			r.Action = "skip-prior-stop"
		} else {
			r.Action = "finish-prior-stop"
		}
	case "candidate-apply-intent", "candidate-verify-intent":
		r.Action = "recover-candidate"
	case "cleanup-intent":
		if result == "completed-success" {
			r.Action = "skip-completed-cleanup"
		} else {
			r.Action = "retry-exact-cleanup"
		}
	case "restore-prepare-intent":
		r.Action = "prepare-fresh-restoration"
	case "restore-apply-intent", "restore-verify-intent":
		r.Action = "cleanup-restoration-then-fresh-restore"
	default:
		return RecoveryInspection{}, ErrCorrupt
	}
	return r, nil
}
