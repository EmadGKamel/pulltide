package state

import (
	"os"
	"strings"
	"time"
)

func validateTransition(old, next *State) error {
	if old.Version != next.Version || old.InstallationID != next.InstallationID || old.Ownership != next.Ownership {
		return ErrOwnership
	}
	for id, instance := range old.Instances {
		if newInstance, exists := next.Instances[id]; exists && !equalInstanceInputs(instance, newInstance) {
			return ErrCorrupt
		}
	}
	if old.Journal != nil && next.Journal != nil {
		a, b := old.Journal, next.Journal
		if a.AttemptID != b.AttemptID || !equalJSON(a.Policy, b.Policy) || !equalJSON(a.Target, b.Target) {
			return ErrCorrupt
		}
	}
	return nil
}
func bootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", ErrRecoveryRequired
	}
	id := strings.TrimSpace(string(b))
	if !validText(id) {
		return "", ErrRecoveryRequired
	}
	return id, nil
}
func operation(stage, operand string, index *int) (*Operation, error) {
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	boot, err := bootID()
	if err != nil {
		return nil, err
	}
	var checkIndex *int
	if index != nil {
		value := *index
		checkIndex = &value
	}
	return &Operation{ID: id, Stage: stage, OperandID: operand, BootID: boot, CheckIndex: checkIndex}, nil
}
func (s *Store) ObserveDesired(target Target) error {
	if !validTarget(&target) {
		return ErrCorrupt
	}
	return s.update(func(st *State) error {
		if st.Journal != nil {
			return ErrRecoveryRequired
		}
		if st.Desired == nil || *st.Desired != target {
			st.Suppressed = nil
		}
		st.Desired = &target
		st.DesiredStale = false
		return nil
	}, false)
}
func (s *Store) MarkDesiredStale() error {
	return s.update(func(st *State) error { st.DesiredStale = true; return nil }, false)
}
func (s *Store) BeginPreparation(policy FrozenPolicy, staging []string) error {
	op, err := operation("artifact-intent", "", nil)
	if err != nil {
		return err
	}
	id, err := NewID()
	if err != nil {
		return err
	}
	return s.update(func(st *State) error {
		if st.Journal != nil || !validTarget(st.Desired) || st.Suppressed != nil && *st.Suppressed == *st.Desired || st.Active.State == "unknown" || st.Active.State == "known" && st.Health.State != "healthy" {
			return ErrRecoveryRequired
		}
		target := *st.Desired
		prior := ""
		if st.Active.State == "known" {
			prior = st.Active.InstanceID
		}
		st.Journal = &Journal{Kind: "preparation", AttemptID: id, BootID: op.BootID, Stage: op.Stage, Target: &target, Policy: &policy, PriorID: prior, Operation: op, CleanupCompleted: map[string]bool{}, Staging: append([]string{}, staging...)}
		st.Journal.MayBeLive = map[string]bool{}
		if prior != "" {
			st.Journal.MayBeLive[prior] = true
		}
		s.recoveryStarted = false
		s.recoveryFailed = false
		s.reopenedJournal = false
		st.Phase = "preparing"
		return nil
	}, true)
}
func (s *Store) BeginObservation() error {
	boot, bootErr := bootID()
	if bootErr != nil {
		return bootErr
	}
	id, err := NewID()
	if err != nil {
		return err
	}
	return s.update(func(st *State) error {
		if s.observationStarted || st.Journal != nil || st.Active.State != "known" {
			return ErrRecoveryRequired
		}
		health := st.Instances[st.Active.InstanceID].Saved.Health
		st.Journal = &Journal{Kind: "observation", AttemptID: id, BootID: boot, Stage: "startup-verify-intent", OperandID: st.Active.InstanceID, SavedHealth: &health, CleanupCompleted: map[string]bool{}}
		s.observationStarted = true
		st.Health = Health{State: "unknown"}
		st.Phase = "verifying"
		return nil
	}, false)
}

// RecordIntent is the durable boundary before an external action. This package
// never invokes that action. A previous unknown result cannot be bypassed.
func (s *Store) RecordIntent(stage, operand string, checkIndex *int) error {
	op, err := operation(stage, operand, checkIndex)
	if err != nil {
		return err
	}
	return s.update(func(st *State) error {
		j := st.Journal
		if s.recoveryFailed {
			return ErrRecoveryRequired
		}
		if s.reopenedJournal && !s.recoveryStarted {
			return ErrRecoveryRequired
		}
		if s.reopenedJournal && oneOf(stage, "restore-apply-intent", "restore-verify-intent") && s.freshRestorationID != operand {
			return ErrRecoveryRequired
		}
		if s.recoveryStarted && oneOf(stage, "candidate-apply-intent", "candidate-verify-intent") {
			return ErrRecoveryRequired
		}
		if oneOf(stage, "cleanup-intent", "restore-prepare-intent", "restore-apply-intent", "restore-verify-intent") && !s.recoveryStarted {
			return ErrRecoveryRequired
		}
		if j != nil && oneOf(stage, "cleanup-intent", "prior-stop-intent") && j.CleanupCompleted[operand] {
			return ErrRecoveryRequired
		}
		if j == nil || !validOperand(j, stage, operand) {
			return ErrCorrupt
		}
		if j.Operation != nil {
			if j.Operation.Result != "completed-success" && !(j.Operation.QuiescenceConfirmed && (s.recoveryStarted || oneOf(j.Stage, "startup-verify-intent", "candidate-verify-intent", "restore-verify-intent"))) {
				return ErrRecoveryRequired
			}
			j.Completed = append(j.Completed, *j.Operation)
		}
		if oneOf(stage, "startup-verify-intent", "candidate-verify-intent", "restore-verify-intent") && j.HealthWindowResult != "" {
			return ErrRecoveryRequired
		}
		if !allowedStage(j.Stage, stage) {
			return ErrCorrupt
		}
		if stage == "candidate-apply-intent" && j.PriorID != "" && !j.CleanupCompleted[j.PriorID] {
			return ErrRecoveryRequired
		}
		if stage == "restore-apply-intent" {
			for _, id := range []string{j.PriorID, j.CandidateID} {
				if id != "" && j.MayBeLive[id] {
					return ErrRecoveryRequired
				}
			}
		}
		if oneOf(stage, "candidate-apply-intent", "restore-apply-intent") {
			if j.MayBeLive == nil {
				j.MayBeLive = map[string]bool{}
			}
			j.MayBeLive[operand] = true
		}
		j.Stage = stage
		j.OperandID = operand
		j.Operation = op
		j.HealthWindowResult = ""
		if oneOf(stage, "prior-stop-intent", "candidate-apply-intent", "candidate-verify-intent", "cleanup-intent", "restore-apply-intent", "restore-verify-intent") {
			st.Active = Active{State: "unknown"}
			st.Health = Health{State: "unknown"}
		}
		switch stage {
		case "candidate-verify-intent", "startup-verify-intent":
			st.Phase = "verifying"
		case "prior-stop-intent", "candidate-apply-intent":
			st.Phase = "applying"
		case "cleanup-intent", "restore-prepare-intent", "restore-apply-intent", "restore-verify-intent":
			st.Phase = "rolling-back"
		}
		return nil
	}, true)
}
func allowedStage(from, to string) bool {
	switch from {
	case "artifact-intent":
		return to == "artifact-intent"
	case "prepared":
		return oneOf(to, "prior-stop-intent", "candidate-apply-intent")
	case "prior-stop-intent":
		return oneOf(to, "prior-stop-intent", "candidate-apply-intent", "cleanup-intent", "restore-prepare-intent")
	case "candidate-apply-intent":
		return oneOf(to, "candidate-verify-intent", "cleanup-intent")
	case "candidate-verify-intent":
		return oneOf(to, "candidate-verify-intent", "cleanup-intent")
	case "cleanup-intent":
		return oneOf(to, "cleanup-intent", "restore-prepare-intent")
	case "restore-prepare-intent":
		return oneOf(to, "restore-prepare-intent", "restore-apply-intent", "cleanup-intent")
	case "restore-apply-intent":
		return oneOf(to, "restore-verify-intent", "cleanup-intent")
	case "restore-verify-intent":
		return oneOf(to, "restore-verify-intent", "cleanup-intent")
	case "startup-verify-intent":
		return to == "startup-verify-intent"
	}
	return false
}

// RecordResult accepts the future driver's conclusive termination evidence. The
// bool is an explicit caller assertion, not evidence derived from file/lock state.
func (s *Store) RecordResult(result string, driverConfirmedStopped bool) error {
	return s.RecordCheckpoint(result, driverConfirmedStopped, nil)
}

// RecordCheckpoint may additionally record driver-declared generated recovery
// files, relative to the operand workspace. They must already exist; contents
// remain mutable, and the whole workspace is synchronized on success.
func (s *Store) RecordCheckpoint(result string, driverConfirmedStopped bool, recoveryFiles []string) error {
	if !oneOf(result, "completed-success", "completed-failure", "completion-unknown") {
		return ErrCorrupt
	}
	if result != "completion-unknown" && !driverConfirmedStopped {
		return ErrRecoveryRequired
	}
	return s.update(func(st *State) error {
		j := st.Journal
		if j == nil || j.Operation == nil || j.Operation.Result != "" {
			return ErrCorrupt
		}
		if s.recoveryStarted && result != "completed-success" && !oneOf(j.Stage, "restore-verify-intent") {
			s.recoveryFailed = true
		}
		if recoveryFiles != nil {
			if result != "completed-success" || j.OperandID == "" {
				return ErrCorrupt
			}
			instance := st.Instances[j.OperandID]
			instance.RequiredRecoveryFiles = append([]string{}, recoveryFiles...)
			st.Instances[j.OperandID] = instance
		}
		j.Operation.Result = result
		j.Operation.QuiescenceConfirmed = driverConfirmedStopped
		if result == "completed-success" && oneOf(j.Stage, "cleanup-intent", "prior-stop-intent") {
			instance := st.Instances[j.OperandID]
			instance.RequiredRecoveryFiles = nil
			st.Instances[j.OperandID] = instance
			j.CleanupCompleted[j.OperandID] = true
			delete(j.MayBeLive, j.OperandID)
		}
		if result != "completed-success" {
			st.Phase = "recovery-required"
			category, stage := "runtime", "apply"
			if j.Kind == "preparation" {
				category, stage = "preparation", "prepare"
			}
			if oneOf(j.Stage, "startup-verify-intent", "candidate-verify-intent", "restore-verify-intent") {
				category, stage = "health", "verify"
				if result == "completed-failure" {
					st.Phase = "verifying"
				}
			}
			if j.Stage == "cleanup-intent" {
				stage = "cleanup"
			}
			if result == "completion-unknown" {
				category = "uncertain-operation"
			}
			st.Error = &Diagnostic{Category: category, Stage: stage}
		}
		return nil
	}, result == "completed-success")
}

// Prepared attaches durable driver-produced inputs and a fresh deployment
// instance. It cannot promote a partial preparatory artifact.
func (s *Store) Prepared(instance Instance) (string, error) {
	id, err := NewID()
	if err != nil {
		return "", err
	}
	instance.ID = id
	err = s.update(func(st *State) error {
		j := st.Journal
		if j == nil || j.Kind != "preparation" || j.Operation == nil || j.Operation.Result != "completed-success" || instance.Target != *j.Target {
			return ErrRecoveryRequired
		}
		if j.Policy.RollbackEnabled != instance.Saved.Rollback.Enabled || j.Policy.OperationTimeoutSeconds != instance.Saved.Reconcile.OperationTimeoutSeconds {
			return ErrCorrupt
		}
		st.Instances[id] = instance
		j.Kind = "deployment"
		j.Stage = "prepared"
		j.CandidateID = id
		j.OperandID = ""
		j.Operation = nil
		j.Completed = nil
		return nil
	}, true)
	if err != nil {
		return "", err
	}
	return id, nil
}

// AttachRestoration only binds saved last-good inputs to a fresh workspace. It
// performs no restoration and cannot discard a possibly live former operand.
func (s *Store) AttachRestoration(workspace string) (string, error) {
	id, err := NewID()
	if err != nil {
		return "", err
	}
	err = s.update(func(st *State) error {
		j := st.Journal
		if s.recoveryFailed || !s.recoveryStarted {
			return ErrRecoveryRequired
		}
		if j == nil || j.Kind != "deployment" || j.Policy == nil || !j.Policy.RollbackEnabled || st.LastGoodID == "" {
			return ErrRecoveryRequired
		}
		if j.RestorationID != "" && j.MayBeLive[j.RestorationID] {
			return ErrRecoveryRequired
		}
		if j.Operation != nil && !oneOf(j.Operation.Result, "completed-success", "completed-failure") && !j.Operation.QuiescenceConfirmed {
			return ErrRecoveryRequired
		}
		prior := st.Instances[st.LastGoodID]
		prior.ID = id
		prior.Workspace = workspace
		prior.RequiredRecoveryFiles = nil
		st.Instances[id] = prior
		if j.RestorationID != "" {
			delete(j.CleanupCompleted, j.RestorationID)
			delete(j.MayBeLive, j.RestorationID)
		}
		if j.Operation != nil {
			j.Completed = append(j.Completed, *j.Operation)
		}
		oldID := j.RestorationID
		history := j.Completed[:0]
		for _, op := range j.Completed {
			if op.OperandID != oldID {
				history = append(history, op)
			}
		}
		j.Completed = history
		j.RestorationID = id
		s.freshRestorationID = id
		op, err := operation("restore-prepare-intent", id, nil)
		if err != nil {
			return err
		}
		j.Operation = op
		j.Stage = op.Stage
		j.OperandID = id
		j.HealthWindowResult = ""
		st.Phase = "rolling-back"
		pruneInstances(st)
		return nil
	}, true)
	if err != nil {
		return "", err
	}
	return id, nil
}

// CommitVerified atomically publishes success and clears the journal. Callers
// cannot promote a prepared/apply checkpoint without verified completion.
func (s *Store) CommitVerified() error {
	return s.update(func(st *State) error {
		j := st.Journal
		if j == nil || j.Operation == nil || j.Operation.Result != "completed-success" || j.HealthWindowResult != "completed-success" {
			return ErrRecoveryRequired
		}
		var id string
		switch j.Stage {
		case "candidate-verify-intent":
			if s.reopenedJournal || s.recoveryStarted {
				return ErrRecoveryRequired
			}
			id = j.CandidateID
			st.Phase = "healthy"
		case "restore-verify-intent":
			if s.reopenedJournal && s.freshRestorationID != j.RestorationID {
				return ErrRecoveryRequired
			}
			id = j.RestorationID
			st.Phase = "rolled-back"
			target := *j.Target
			st.Suppressed = &target
		default:
			return ErrCorrupt
		}
		st.Active = Active{State: "known", InstanceID: id}
		st.LastGoodID = id
		s.observationStarted = true
		now := time.Now().UTC()
		st.Health = Health{State: "healthy", ObservedAt: &now}
		st.Error = nil
		s.reopenedJournal = false
		s.recoveryStarted = false
		s.recoveryFailed = false
		s.freshRestorationID = ""
		st.Journal = nil
		pruneInstances(st)
		return nil
	}, true)
}
func (s *Store) CommitObservation(healthy bool) error {
	return s.update(func(st *State) error {
		j := st.Journal
		if s.reopenedJournal {
			return ErrRecoveryRequired
		}
		if j == nil || j.Kind != "observation" || j.Operation == nil || !oneOf(j.Operation.Result, "completed-success", "completed-failure") {
			return ErrRecoveryRequired
		}
		if (healthy && j.HealthWindowResult != "completed-success") || (!healthy && j.HealthWindowResult != "completed-failure") {
			return ErrCorrupt
		}
		now := time.Now().UTC()
		st.Health = Health{State: "healthy", ObservedAt: &now}
		st.Phase = "healthy"
		if !healthy {
			st.Health.State = "unhealthy"
			st.Phase = "recovery-required"
			st.Error = &Diagnostic{Category: "health", Stage: "verify"}
		}
		st.Journal = nil
		return nil
	}, healthy)
}
func pruneInstances(st *State) {
	refs := st.ReferencedInstances()
	for id := range st.Instances {
		if !refs[id] {
			delete(st.Instances, id)
		}
	}
}

// BeginRecovery records caller-provided driver quiescence evidence, without
// claiming it follows from a lock, process restart, PID absence or the journal.
// It never executes recovery and permits at most one recovery session per handle.
func (s *Store) BeginRecovery(driverConfirmedStopped bool) error {
	if !driverConfirmedStopped {
		return ErrRecoveryRequired
	}
	return s.update(func(st *State) error {
		j := st.Journal
		if s.recoveryStarted || j == nil || j.Kind != "deployment" || j.Stage == "prepared" {
			return ErrRecoveryRequired
		}
		if j.Operation != nil {
			j.Operation.QuiescenceConfirmed = true
		}
		st.Phase = "rolling-back"
		s.recoveryStarted = true
		return nil
	}, false)
}

// RecordHealthWindow records the health runner's whole-window result separately
// from individual command/HTTP completion. Success requires a full ordered round.
func (s *Store) RecordHealthWindow(healthy, checksConfirmedStopped bool) error {
	if !checksConfirmedStopped {
		return ErrRecoveryRequired
	}
	return s.update(func(st *State) error {
		j := st.Journal
		if j == nil || j.Operation == nil || !oneOf(j.Stage, "startup-verify-intent", "candidate-verify-intent", "restore-verify-intent") || !oneOf(j.Operation.Result, "completed-success", "completed-failure") {
			return ErrRecoveryRequired
		}
		if healthy {
			if !successfulHealthRound(st, j) {
				return ErrCorrupt
			}
			j.HealthWindowResult = "completed-success"
		} else {
			j.HealthWindowResult = "completed-failure"
			if s.recoveryStarted {
				s.recoveryFailed = true
			}
		}
		return nil
	}, healthy)
}

// AbandonUnmutated never suppresses a preparation or prepared candidate.
func (s *Store) AbandonUnmutated(driverConfirmedStopped bool) error {
	return s.update(func(st *State) error {
		j := st.Journal
		if j == nil || !(j.Kind == "preparation" || j.Stage == "prepared") {
			return ErrCorrupt
		}
		if j.Operation != nil && !oneOf(j.Operation.Result, "completed-success", "completed-failure") && !driverConfirmedStopped {
			return ErrRecoveryRequired
		}
		st.Journal = nil
		s.reopenedJournal = false
		st.Phase = "failed"
		pruneInstances(st)
		return nil
	}, false)
}

// CommitAbsent consumes external absence evidence; file persistence is not proof
// of absence. Disabled rollback/no-last-good cleanup preserves historical inputs.
func (s *Store) CommitAbsent(driverConfirmedAbsent bool) error {
	if !driverConfirmedAbsent {
		return ErrRecoveryRequired
	}
	return s.update(func(st *State) error {
		j := st.Journal
		if j == nil || j.Kind != "deployment" || j.Policy == nil || j.Stage == "prepared" || j.Policy.RollbackEnabled && st.LastGoodID != "" {
			return ErrRecoveryRequired
		}
		if j.Operation == nil || j.Operation.Result != "completed-success" {
			return ErrRecoveryRequired
		}
		for _, live := range j.MayBeLive {
			if live {
				return ErrRecoveryRequired
			}
		}
		target := *j.Target
		st.Suppressed = &target
		st.Active = Active{State: "absent"}
		st.Health = Health{State: "not-applicable"}
		st.Phase = "failed"
		s.reopenedJournal = false
		s.recoveryStarted = false
		s.recoveryFailed = false
		s.freshRestorationID = ""
		st.Journal = nil
		pruneInstances(st)
		return nil
	}, false)
}

// AbandonObservation clears only a conclusively ended old window, preserving
// active and last-good. A fresh window must then be journaled before checks.
func (s *Store) AbandonObservation(driverConfirmedStopped bool) error {
	return s.update(func(st *State) error {
		j := st.Journal
		if !s.reopenedJournal {
			return ErrRecoveryRequired
		}
		if j == nil || j.Kind != "observation" {
			return ErrCorrupt
		}
		if j.Operation != nil && !oneOf(j.Operation.Result, "completed-success", "completed-failure") && !driverConfirmedStopped {
			return ErrRecoveryRequired
		}
		st.Journal = nil
		st.Health = Health{State: "unknown"}
		s.reopenedJournal = false
		return nil
	}, false)
}

func equalInstanceInputs(a, b Instance) bool {
	a.RequiredRecoveryFiles = nil
	b.RequiredRecoveryFiles = nil
	return equalJSON(a, b)
}
