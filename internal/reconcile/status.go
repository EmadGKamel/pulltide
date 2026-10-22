package reconcile

import (
	"encoding/json"
	"os"
	"time"

	"pulltide/internal/state"
)

// Explicit DTOs prevent private saved configuration or raw adapter errors from
// accidentally entering the public status stream.
type revisionStatus struct {
	Driver         string `json:"driver"`
	SourceIdentity string `json:"sourceIdentity"`
	Revision       string `json:"revision"`
}
type targetStatus struct {
	Source                revisionStatus `json:"source"`
	ConfigurationIdentity string         `json:"configurationIdentity"`
}
type activeStatus struct {
	State      string        `json:"state"`
	Target     *targetStatus `json:"target"`
	InstanceID *string       `json:"instanceId"`
}
type pendingStatus struct {
	Kind              string        `json:"kind"`
	AttemptID         string        `json:"attemptId"`
	Stage             string        `json:"stage"`
	OperandTarget     *targetStatus `json:"operandTarget"`
	OperandInstanceID *string       `json:"operandInstanceId"`
	CandidateTarget   *targetStatus `json:"candidateTarget"`
}
type errorStatus struct {
	Category   string `json:"category"`
	Stage      string `json:"stage"`
	CheckIndex *int   `json:"checkIndex"`
}
type status struct {
	StatusVersion      string         `json:"statusVersion"`
	Application        string         `json:"application"`
	Phase              string         `json:"phase"`
	Desired            *targetStatus  `json:"desired"`
	DesiredStale       bool           `json:"desiredStale"`
	Active             activeStatus   `json:"active"`
	LastGood           *targetStatus  `json:"lastGood"`
	LastGoodInstanceID *string        `json:"lastGoodInstanceId"`
	SuppressedTarget   *targetStatus  `json:"suppressedTarget"`
	Pending            *pendingStatus `json:"pending"`
	Health             string         `json:"health"`
	HealthObservedAt   *time.Time     `json:"healthObservedAt"`
	Error              *errorStatus   `json:"error"`
}

func target(t *state.Target) *targetStatus {
	if t == nil {
		return nil
	}
	return &targetStatus{revisionStatus{t.Source.Driver, t.Source.SourceIdentity, t.Source.Revision}, t.ConfigurationIdentity}
}
func idPointer(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func (e *Engine) emit() {
	st, err := e.store.Read()
	if err != nil {
		st = e.lastStatus
		if st == nil {
			return
		}
	} else {
		e.lastStatus = st
	}
	event := status{StatusVersion: "pulltide.io/status/v1alpha1", Application: e.app.Metadata.Name, Phase: st.Phase, Desired: target(st.Desired), DesiredStale: st.DesiredStale, Active: activeStatus{State: st.Active.State}, SuppressedTarget: target(st.Suppressed), Health: st.Health.State, HealthObservedAt: st.Health.ObservedAt}
	if st.Active.State == "known" {
		i := st.Instances[st.Active.InstanceID]
		event.Active.Target = target(&i.Target)
		event.Active.InstanceID = idPointer(i.ID)
	}
	if st.LastGoodID != "" {
		i := st.Instances[st.LastGoodID]
		event.LastGood = target(&i.Target)
		event.LastGoodInstanceID = idPointer(i.ID)
	}
	if e.terminal != nil {
		event.Phase = "recovery-required"
		event.Error = &errorStatus{e.terminal.Category, e.terminal.Stage, e.terminal.CheckIndex}
	} else if st.Error != nil {
		event.Error = &errorStatus{st.Error.Category, st.Error.Stage, st.Error.CheckIndex}
	}
	if j := st.Journal; j != nil {
		p := &pendingStatus{Kind: j.Kind, AttemptID: j.AttemptID, Stage: j.Stage, OperandInstanceID: idPointer(j.OperandID), CandidateTarget: target(j.Target)}
		if i, ok := st.Instances[j.OperandID]; ok {
			p.OperandTarget = target(&i.Target)
		}
		event.Pending = p
	}
	if err = json.NewEncoder(e.output).Encode(event); err != nil {
		if !e.statusErr {
			_, _ = os.Stderr.WriteString("pulltide: status emission failed\n")
		}
		e.statusErr = true
	}
}
