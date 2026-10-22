package application

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../../tests/fixtures/contract", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// API-01–04, API-06 (static), API-10 (input), API-11: the exact shared corpus.
func TestContractFixtures(t *testing.T) {
	b, err := os.ReadFile("../../tests/fixtures/contract/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Cases []struct {
			File   string
			Expect string
		}
	}
	if err := json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, c := range manifest.Cases {
		t.Run(c.File, func(t *testing.T) {
			input := fixture(t, c.File)
			original := bytes.Clone(input)
			app, err := Parse(input)
			if (err == nil) != (c.Expect == "valid") {
				t.Fatalf("expected %s; got %v", c.Expect, err)
			}
			if !bytes.Equal(input, original) {
				t.Fatal("source document mutated")
			}
			if err == nil && app.APIVersion != "pulltide.io/v1alpha1" {
				t.Fatal("API changed")
			}
		})
	}
}

// API-04/05: default before comparison, while preserving source bytes and order.
func TestDefaultsAndOrder(t *testing.T) {
	a, err := Parse(fixture(t, "valid/exec-minimal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Spec.Source.Path != "." || a.Spec.Reconcile.IntervalSeconds != 30 || a.Spec.Reconcile.OperationTimeoutSeconds != 300 || !a.Spec.Rollback.Enabled || a.Spec.Runtime.Exec.Env == nil || a.Spec.Health.TimeoutSeconds != 60 || a.Spec.Health.IntervalSeconds != 5 || a.Spec.Health.Checks[0].TimeoutSeconds != 5 {
		t.Fatal("incorrect defaults")
	}
	b, err := Parse(fixture(t, "valid/compose-ordered-files-checks.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(b.Spec.Runtime.Compose.Files, ",") != "compose.yaml,compose.override.yaml,compose.yaml" || b.Spec.Health.Checks[1].Type != "command" {
		t.Fatal("array order changed")
	}
}

func TestCoreNumericValues(t *testing.T) {
	for _, c := range []struct {
		Name string
		Want int
	}{{"core-decimal-leading-zero", 10}, {"core-octal", 8}, {"core-hex", 16}} {
		a, err := Parse(fixture(t, "valid/"+c.Name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if a.Spec.Reconcile.IntervalSeconds != c.Want {
			t.Fatal("wrong numeric value")
		}
	}
}

func TestMalformedAndSecretSafe(t *testing.T) {
	for _, input := range []string{"", "[", "a: [", "apiVersion: [SECRET", "apiVersion: pulltide.io/v1alpha1\nkind: Application\nSECRET: value", string([]byte{0xff})} {
		for i := 0; i < 3; i++ {
			_, err := Parse([]byte(input))
			if err == nil {
				t.Fatal("accepted malformed input")
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatal("diagnostic leaked input")
			}
		}
	}
}

func TestURLSemantics(t *testing.T) {
	base := string(fixture(t, "valid/exec-minimal.yaml"))
	for _, bad := range []string{"https://example.com:invalid/repo", "https://[invalid]/repo", "https://example.com/%zz", "https://example.com:99999/repo", "ssh://a@b@host/repo"} {
		// Fixture URL spellings are deliberately not assumed here.
		lines := strings.Split(base, "\n")
		for i, l := range lines {
			if strings.HasPrefix(l, "    url:") {
				lines[i] = "    url: " + bad
				break
			}
		}
		if _, err := Parse([]byte(strings.Join(lines, "\n"))); err == nil {
			t.Fatal("accepted malformed URL")
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("apiVersion: pulltide.io/v1alpha1\nkind: Application\n"))
	f.Add([]byte("a: &a [*a]"))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = Parse(b) })
}

// API-03: exercise every fixed object boundary, rather than relying solely on
// one representative invalid fixture per type.
func TestClosedObjectsAndNulls(t *testing.T) {
	paths := [][]string{
		{}, {"metadata"}, {"spec"}, {"spec", "source"}, {"spec", "reconcile"},
		{"spec", "runtime"}, {"spec", "runtime", "exec"}, {"spec", "health"}, {"spec", "rollback"},
	}
	for _, path := range paths {
		for _, operation := range []string{"unknown", "null"} {
			t.Run(strings.Join(path, ".")+operation, func(t *testing.T) {
				a, err := Parse(fixture(t, "valid/exec-explicit-defaults.yaml"))
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(a)
				if err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				if err := json.Unmarshal(encoded, &doc); err != nil {
					t.Fatal(err)
				}
				current := doc
				for _, key := range path {
					current = current[key].(map[string]any)
				}
				if operation == "unknown" {
					current["unsupported"] = true
				} else {
					if len(path) == 0 {
						encoded = []byte("null")
					} else {
						parent := doc
						for _, key := range path[:len(path)-1] {
							parent = parent[key].(map[string]any)
						}
						parent[path[len(path)-1]] = nil
					}
				}
				if !(operation == "null" && len(path) == 0) {
					encoded, err = json.Marshal(doc)
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, err := Parse(encoded); err == nil {
					t.Fatal("accepted invalid object")
				}
			})
		}
	}
}

func TestCoreScalarResolution(t *testing.T) {
	for _, c := range []struct {
		Text string
		Want any
	}{
		{"yes", "yes"}, {"on", "on"}, {"2026-10-22", "2026-10-22"}, {"TRUE", true},
		{"\"true\"", "true"}, {"!!str true", "true"}, {"0010", json.Number("10")},
		{"0o10", json.Number("8")}, {"0x10", json.Number("16")}, {"5.0", float64(5)},
		{"1e1", float64(10)}, {"!!float \"10\"", float64(10)},
	} {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(c.Text), &node); err != nil {
			t.Fatal(err)
		}
		got, err := decodeNode(node.Content[0])
		if err != nil {
			t.Fatal(err)
		}
		if got != c.Want {
			t.Fatalf("wrong scalar value/type for %s", c.Text)
		}
	}
}

func TestErrorsDeterministicAndSanitized(t *testing.T) {
	input := []byte("apiVersion: pulltide.io/v1alpha1\nkind: Application\nmetadata: {name: app}\nspec: {unknown: SECRET}\nSECRET_KEY: SECRET_VALUE\n")
	var previous string
	for i := 0; i < 20; i++ {
		_, err := Parse(input)
		if err == nil {
			t.Fatal("accepted invalid input")
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Fatal("diagnostic leaked input")
		}
		if i > 0 && err.Error() != previous {
			t.Fatal("nondeterministic error")
		}
		previous = err.Error()
	}
}
