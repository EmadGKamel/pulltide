// Package application parses and validates v1alpha1 input without executing it.
package application

// Application is the validated, defaulted public Application input contract.
// Status and deployment state deliberately have no representation here.
type Application struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
}
type Metadata struct {
	Name string `json:"name"`
}
type Spec struct {
	Source    Source    `json:"source"`
	Reconcile Reconcile `json:"reconcile"`
	Runtime   Runtime   `json:"runtime"`
	Health    Health    `json:"health"`
	Rollback  Rollback  `json:"rollback"`
}
type Source struct {
	Type string `json:"type"`
	URL  string `json:"url"`
	Ref  string `json:"ref"`
	Path string `json:"path"`
}
type Reconcile struct {
	IntervalSeconds         int `json:"intervalSeconds"`
	OperationTimeoutSeconds int `json:"operationTimeoutSeconds"`
}
type Runtime struct {
	Type    string   `json:"type"`
	Exec    *Exec    `json:"exec,omitempty"`
	Compose *Compose `json:"docker-compose,omitempty"`
}
type Exec struct {
	Apply []string          `json:"apply"`
	Stop  []string          `json:"stop"`
	Env   map[string]string `json:"env"`
}
type Compose struct {
	Files []string `json:"files"`
}
type Health struct {
	TimeoutSeconds  int     `json:"timeoutSeconds"`
	IntervalSeconds int     `json:"intervalSeconds"`
	Checks          []Check `json:"checks"`
}
type Check struct {
	Type           string   `json:"type"`
	URL            string   `json:"url,omitempty"`
	Command        []string `json:"command,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
}
type Rollback struct {
	Enabled bool `json:"enabled"`
}
