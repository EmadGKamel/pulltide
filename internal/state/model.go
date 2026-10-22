// Package state owns private durable records. It never invokes runtime drivers.
package state

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"pulltide/internal/application"
)

const FormatVersion = 1
const Root = "/var/lib/pulltide"

var (
	ErrOwnership        = errors.New("state ownership verification failed")
	ErrLocked           = errors.New("state ownership lock is held")
	ErrCorrupt          = errors.New("invalid or unsupported state record")
	ErrPersistence      = errors.New("durable state commit failed")
	ErrRecoveryRequired = errors.New("state recovery is required")
)

type Ownership struct {
	Name             string
	Runtime          string
	ResourceIdentity string
	DaemonIdentity   string
}
type Target struct {
	Source                Revision
	ConfigurationIdentity string
}
type Revision struct {
	Driver         string
	SourceIdentity string
	Revision       string
}
type FileReference struct {
	Path   string
	SHA256 string
}

// Inputs is the complete driver-declared immutable file closure, including source
// manifests, descriptors and actual image archives. Core checks bytes, not drivers.
type Instance struct {
	ID                    string
	Target                Target
	Saved                 application.Spec
	Workspace             string
	Inputs                []FileReference
	RequiredRecoveryFiles []string
}
type Active struct {
	State      string
	InstanceID string
}
type Health struct {
	State      string
	ObservedAt *time.Time
}
type Diagnostic struct {
	Category   string
	Stage      string
	CheckIndex *int
}
type FrozenPolicy struct {
	RollbackEnabled         bool
	OperationTimeoutSeconds int
}
type Operation struct {
	ID                  string
	Stage               string
	OperandID           string
	BootID              string
	CheckIndex          *int
	QuiescenceConfirmed bool
	Result              string // Empty is durable intent without a durable result: may have run.
}
type Journal struct {
	Kind               string
	AttemptID          string
	BootID             string
	Stage              string
	Target             *Target
	Policy             *FrozenPolicy
	CandidateID        string
	PriorID            string
	RestorationID      string
	OperandID          string
	SavedHealth        *application.Health
	Operation          *Operation
	Completed          []Operation
	CleanupCompleted   map[string]bool
	MayBeLive          map[string]bool
	HealthWindowResult string
	Staging            []string
}
type State struct {
	Version        int
	Generation     uint64
	InstallationID string
	Ownership      Ownership
	Desired        *Target
	DesiredStale   bool
	Active         Active
	LastGoodID     string
	Health         Health
	Suppressed     *Target
	Phase          string
	Error          *Diagnostic
	Instances      map[string]Instance
	Journal        *Journal
}
type registry struct {
	Version        int
	InstallationID string
	UID            int
	Bindings       map[string]Ownership
}

func NewID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", ErrPersistence
	}
	return hex.EncodeToString(b), nil
}

var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validText(s string) bool { return s != "" && utf8.ValidString(s) && !strings.ContainsRune(s, 0) }
func validPath(s string) bool {
	return validText(s) && !filepath.IsAbs(s) && s != "." && filepath.Clean(s) == s && s != ".." && !strings.HasPrefix(s, "../")
}
func validOwner(o Ownership) bool {
	return namePattern.MatchString(o.Name) && validText(o.ResourceIdentity) && (o.Runtime == "exec" || (o.Runtime == "docker-compose" && validText(o.DaemonIdentity) && o.ResourceIdentity == "pulltide-"+o.Name))
}
func validTarget(t *Target) bool {
	return t != nil && validText(t.Source.Driver) && validText(t.Source.SourceIdentity) && validText(t.Source.Revision) && strings.HasPrefix(t.ConfigurationIdentity, "identity-v1:") && digestPattern.MatchString(strings.TrimPrefix(t.ConfigurationIdentity, "identity-v1:"))
}
func (s *State) Validate() error {
	if s.Version != FormatVersion || s.Generation == 0 || !validText(s.InstallationID) || !validOwner(s.Ownership) || s.Instances == nil {
		return ErrCorrupt
	}
	for _, target := range []*Target{s.Desired, s.Suppressed} {
		if target != nil && !validTarget(target) {
			return ErrCorrupt
		}
	}
	if s.Suppressed != nil && (s.Desired == nil || *s.Desired != *s.Suppressed) {
		return ErrCorrupt
	}
	if !oneOf(s.Phase, "idle", "preparing", "applying", "verifying", "healthy", "rolling-back", "rolled-back", "failed", "recovery-required") {
		return ErrCorrupt
	}
	switch s.Active.State {
	case "known":
		if _, ok := s.Instances[s.Active.InstanceID]; !ok {
			return ErrCorrupt
		}
	case "absent", "unknown":
		if s.Active.InstanceID != "" {
			return ErrCorrupt
		}
	default:
		return ErrCorrupt
	}
	if !oneOf(s.Health.State, "unknown", "healthy", "unhealthy", "not-applicable") {
		return ErrCorrupt
	}
	observed := s.Health.State == "healthy" || s.Health.State == "unhealthy"
	if observed != (s.Health.ObservedAt != nil) || s.Active.State == "unknown" && s.Health.State != "unknown" || s.Active.State == "absent" && s.Health.State != "not-applicable" || s.Active.State == "known" && s.Health.State == "not-applicable" {
		return ErrCorrupt
	}
	if s.Active.State == "known" && s.LastGoodID != s.Active.InstanceID {
		return ErrCorrupt
	}
	if s.LastGoodID != "" {
		if _, ok := s.Instances[s.LastGoodID]; !ok {
			return ErrCorrupt
		}
	}
	if s.Error != nil && (!oneOf(s.Error.Category, "validation", "ownership", "source", "preparation", "runtime", "health", "persistence", "uncertain-operation", "recovery", "shutdown") || s.Error.CheckIndex != nil && *s.Error.CheckIndex < 0 || !oneOf(s.Error.Stage, "startup", "resolve", "prepare", "apply", "verify", "cleanup", "restore", "persist")) {
		return ErrCorrupt
	}
	for id, instance := range s.Instances {
		if id != instance.ID || !validText(id) || !validTarget(&instance.Target) || !validPath(instance.Workspace) || len(instance.Inputs) == 0 {
			return ErrCorrupt
		}
		if instance.Saved.Runtime.Type != s.Ownership.Runtime {
			return ErrCorrupt
		}
		if err := validateSaved(instance.Saved, s.Ownership.Name, instance.Target.ConfigurationIdentity); err != nil {
			return ErrCorrupt
		}
		for _, path := range instance.RequiredRecoveryFiles {
			if !validPath(path) {
				return ErrCorrupt
			}
		}
		for _, ref := range instance.Inputs {
			if !validPath(ref.Path) || !digestPattern.MatchString(ref.SHA256) {
				return ErrCorrupt
			}
		}
	}
	workspaces := map[string]bool{}
	for _, instance := range s.Instances {
		for path := range workspaces {
			if strings.HasPrefix(path, instance.Workspace+"/") || strings.HasPrefix(instance.Workspace, path+"/") {
				return ErrCorrupt
			}
		}
		if workspaces[instance.Workspace] {
			return ErrCorrupt
		}
		workspaces[instance.Workspace] = true
	}
	if s.Journal != nil {
		if err := s.validateJournal(); err != nil {
			return err
		}
	}
	// Keep only live operands. Historical successful generations are not a store feature.
	refs := s.ReferencedInstances()
	for id := range s.Instances {
		if !refs[id] {
			return ErrCorrupt
		}
	}
	return nil
}
func oneOf(value string, choices ...string) bool {
	for _, v := range choices {
		if v == value {
			return true
		}
	}
	return false
}
func (s *State) ReferencedInstances() map[string]bool {
	refs := map[string]bool{}
	for _, id := range []string{s.Active.InstanceID, s.LastGoodID} {
		if id != "" {
			refs[id] = true
		}
	}
	if j := s.Journal; j != nil {
		for _, id := range []string{j.PriorID, j.CandidateID, j.RestorationID, j.OperandID} {
			if id != "" {
				refs[id] = true
			}
		}
	}
	return refs
}
func (s *State) validateJournal() error {
	j := s.Journal
	if !validText(j.AttemptID) || !validText(j.BootID) || j.CleanupCompleted == nil {
		return ErrCorrupt
	}
	switch j.Kind {
	case "preparation":
		if j.Stage != "artifact-intent" || !validTarget(j.Target) || j.CandidateID != "" || j.RestorationID != "" || j.SavedHealth != nil {
			return ErrCorrupt
		}
	case "deployment":
		if !validTarget(j.Target) || j.CandidateID == "" || !oneOf(j.Stage, "prepared", "prior-stop-intent", "candidate-apply-intent", "candidate-verify-intent", "cleanup-intent", "restore-prepare-intent", "restore-apply-intent", "restore-verify-intent") {
			return ErrCorrupt
		}
	case "observation":
		if j.Stage != "startup-verify-intent" || j.Target != nil || j.Policy != nil || j.CandidateID != "" || j.PriorID != "" || j.RestorationID != "" || j.OperandID == "" || j.SavedHealth == nil || s.Active.State != "known" || j.OperandID != s.Active.InstanceID {
			return ErrCorrupt
		}
	default:
		return ErrCorrupt
	}
	if j.Kind != "observation" && (j.Policy == nil || j.Policy.OperationTimeoutSeconds < 1 || j.Policy.OperationTimeoutSeconds > 86400) {
		return ErrCorrupt
	}
	for _, path := range j.Staging {
		if !validPath(path) {
			return ErrCorrupt
		}
	}
	for _, id := range []string{j.CandidateID, j.PriorID, j.RestorationID, j.OperandID} {
		if id != "" {
			if _, ok := s.Instances[id]; !ok {
				return ErrCorrupt
			}
		}
	}
	if j.CandidateID != "" && (j.Policy.RollbackEnabled != s.Instances[j.CandidateID].Saved.Rollback.Enabled || j.Policy.OperationTimeoutSeconds != s.Instances[j.CandidateID].Saved.Reconcile.OperationTimeoutSeconds) {
		return ErrCorrupt
	}
	if j.CandidateID != "" && s.Instances[j.CandidateID].Target != *j.Target {
		return ErrCorrupt
	}
	if j.Kind == "observation" && !equalJSON(j.SavedHealth, s.Instances[j.OperandID].Saved.Health) {
		return ErrCorrupt
	}
	for id, live := range j.MayBeLive {
		if !live || !oneOf(id, j.PriorID, j.CandidateID, j.RestorationID) || j.CleanupCompleted[id] {
			return ErrCorrupt
		}
	}
	for id, done := range j.CleanupCompleted {
		if !done || !oneOf(id, j.PriorID, j.CandidateID, j.RestorationID) || id == "" {
			return ErrCorrupt
		}
	}
	if j.Stage == "prepared" {
		if j.Operation != nil || len(j.Completed) != 0 || j.OperandID != "" || j.HealthWindowResult != "" {
			return ErrCorrupt
		}
		return nil
	}
	if j.Operation == nil && j.Kind != "observation" {
		return ErrCorrupt
	}
	roles := map[string]bool{}
	for _, id := range []string{j.PriorID, j.CandidateID, j.RestorationID} {
		if id != "" {
			if roles[id] {
				return ErrCorrupt
			}
			roles[id] = true
		}
	}
	seen := map[string]bool{}
	for _, op := range append(append([]Operation{}, j.Completed...), operationSlice(j.Operation)...) {
		if !validText(op.ID) || seen[op.ID] || !validText(op.BootID) || !oneOf(op.Result, "", "completed-success", "completed-failure", "completion-unknown") {
			return ErrCorrupt
		}
		seen[op.ID] = true
		if oneOf(op.Stage, "candidate-verify-intent", "restore-verify-intent", "startup-verify-intent") && op.CheckIndex == nil {
			return ErrCorrupt
		}
		if op.CheckIndex != nil && (*op.CheckIndex < 0 || op.OperandID == "" || *op.CheckIndex >= len(s.Instances[op.OperandID].Saved.Health.Checks)) {
			return ErrCorrupt
		}
		if !validOperand(j, op.Stage, op.OperandID) {
			return ErrCorrupt
		}
	}
	for _, op := range j.Completed {
		if !oneOf(op.Result, "completed-success", "completed-failure") && !op.QuiescenceConfirmed {
			return ErrCorrupt
		}
	}
	if j.HealthWindowResult == "completed-success" && !successfulHealthRound(s, j) {
		return ErrCorrupt
	}
	if j.HealthWindowResult != "" && !oneOf(j.HealthWindowResult, "completed-success", "completed-failure") {
		return ErrCorrupt
	}
	live := map[string]bool{}
	cleaned := map[string]bool{}
	if j.PriorID != "" {
		live[j.PriorID] = true
	}
	for _, op := range append(append([]Operation{}, j.Completed...), operationSlice(j.Operation)...) {
		if oneOf(op.Stage, "candidate-apply-intent", "restore-apply-intent") {
			live[op.OperandID] = true
			delete(cleaned, op.OperandID)
		}
		if oneOf(op.Stage, "prior-stop-intent", "cleanup-intent") && op.Result == "completed-success" {
			delete(live, op.OperandID)
			cleaned[op.OperandID] = true
		}
	}
	if !equalJSON(live, nonNilFlags(j.MayBeLive)) || !equalJSON(cleaned, j.CleanupCompleted) {
		return ErrCorrupt
	}
	if j.Operation != nil && (j.Operation.Stage != j.Stage || j.Operation.OperandID != j.OperandID) {
		return ErrCorrupt
	}
	return nil
}
func operationSlice(op *Operation) []Operation {
	if op == nil {
		return nil
	}
	return []Operation{*op}
}
func validOperand(j *Journal, stage, id string) bool {
	switch stage {
	case "artifact-intent":
		return j.Kind == "preparation" && id == ""
	case "prior-stop-intent":
		return j.Kind == "deployment" && id != "" && id == j.PriorID
	case "candidate-apply-intent", "candidate-verify-intent":
		return j.Kind == "deployment" && id != "" && id == j.CandidateID
	case "cleanup-intent":
		return j.Kind == "deployment" && id != "" && oneOf(id, j.PriorID, j.CandidateID, j.RestorationID)
	case "restore-prepare-intent", "restore-apply-intent", "restore-verify-intent":
		return j.Kind == "deployment" && id != "" && id == j.RestorationID
	case "startup-verify-intent":
		return j.Kind == "observation" && id != "" && id == j.OperandID
	}
	return false
}
func (s *State) String() string {
	return fmt.Sprintf("state(version=%d, generation=%d)", s.Version, s.Generation)
}

func nonNilFlags(flags map[string]bool) map[string]bool {
	if flags == nil {
		return map[string]bool{}
	}
	return flags
}

func successfulHealthRound(st *State, j *Journal) bool {
	if j.Operation == nil || !oneOf(j.Stage, "startup-verify-intent", "candidate-verify-intent", "restore-verify-intent") {
		return false
	}
	checks := st.Instances[j.OperandID].Saved.Health.Checks
	ops := append(append([]Operation{}, j.Completed...), *j.Operation)
	if len(checks) == 0 || len(ops) < len(checks) {
		return false
	}
	for i, op := range ops[len(ops)-len(checks):] {
		if op.Stage != j.Stage || op.OperandID != j.OperandID || op.CheckIndex == nil || *op.CheckIndex != i || op.Result != "completed-success" {
			return false
		}
	}
	return true
}
