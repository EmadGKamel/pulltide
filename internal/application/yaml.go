package application

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Error contains only trusted field paths and fixed explanations, never values,
// unknown key names, YAML parser excerpts, command text, or underlying errors.
type Error struct{ Path, Code, Message string }

func (e *Error) Error() string               { return e.Path + ": " + e.Message + " (" + e.Code + ")" }
func issue(path, code, message string) error { return &Error{path, code, message} }

var (
	coreNull  = regexp.MustCompile(`^(~|null|Null|NULL|)$`)
	coreBool  = regexp.MustCompile(`^(true|True|TRUE|false|False|FALSE)$`)
	coreInt   = regexp.MustCompile(`^([-+]?[0-9]+|0o[0-7]+|0x[0-9a-fA-F]+)$`)
	coreFloat = regexp.MustCompile(`^([-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?|[-+]?\.(inf|Inf|INF)|\.(nan|NaN|NAN))$`)
)

// Parse returns an independently owned, defaulted model. It neither rewrites
// source bytes nor contacts URLs, opens referenced paths, or executes commands.
func Parse(source []byte) (*Application, error) {
	if !utf8.Valid(source) {
		return nil, issue("$", "unicode", "expected valid Unicode")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil || len(doc.Content) != 1 {
		return nil, issue("$", "yaml", "expected exactly one YAML mapping document")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, issue("$", "document-count", "expected exactly one YAML document")
	}
	value, err := decodeNode(doc.Content[0])
	if err != nil {
		return nil, err
	}
	root := doc.Content[0]
	if root.Kind == yaml.MappingNode {
		hasAPI, hasKind := false, false
		for i := 0; i < len(root.Content); i += 2 {
			hasAPI = hasAPI || root.Content[i].Value == "apiVersion"
			hasKind = hasKind || root.Content[i].Value == "kind"
		}
		if hasAPI && hasKind && (len(root.Content) < 4 || root.Content[0].Value != "apiVersion" || root.Content[2].Value != "kind") {
			return nil, issue("$", "entry-order", "first entries must be apiVersion then kind")
		}
	}
	if err := contract().validate(value, "$"); err != nil {
		return nil, err
	}
	// Structural validation makes conversion safe, with no YAML-to-field coercion.
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, issue("$", "type", "invalid value type")
	}
	app := &Application{Spec: Spec{Source: Source{Path: "."}, Reconcile: Reconcile{30, 300}, Health: Health{TimeoutSeconds: 60, IntervalSeconds: 5}, Rollback: Rollback{true}}}
	if err := json.Unmarshal(encoded, app); err != nil {
		return nil, issue("$", "type", "invalid value type")
	}
	if app.Spec.Runtime.Exec != nil && app.Spec.Runtime.Exec.Env == nil {
		app.Spec.Runtime.Exec.Env = map[string]string{}
	}
	for i := range app.Spec.Health.Checks {
		c := &app.Spec.Health.Checks[i]
		if c.TimeoutSeconds == 0 {
			c.TimeoutSeconds = 5
		}
		if c.TimeoutSeconds > app.Spec.Health.TimeoutSeconds {
			return nil, issue(fmt.Sprintf("$.spec.health.checks[%d].timeoutSeconds", i), "check-timeout", "must not exceed total health timeout after defaults")
		}
	}
	return app, nil
}

func decodeNode(n *yaml.Node) (any, error) {
	fail := func(code, message string) (any, error) { return nil, issue("$", code, message) }
	if n.Kind == yaml.AliasNode {
		return fail("alias", "aliases are not permitted")
	}
	explicit := n.Style&yaml.TaggedStyle != 0
	tag := strings.TrimPrefix(n.LongTag(), "tag:yaml.org,2002:")
	if explicit && !strings.HasPrefix(n.LongTag(), "tag:yaml.org,2002:") {
		return fail("custom-tag", "custom tags are not permitted")
	}
	switch n.Kind {
	case yaml.MappingNode:
		if explicit && tag != "map" {
			return fail("yaml-type", "tag does not match node kind")
		}
		result := map[string]any{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Value == "<<" && key.Kind == yaml.ScalarNode && ((key.Style == 0) || key.LongTag() == "tag:yaml.org,2002:merge") {
				return fail("merge-key", "merge keys are not permitted")
			}
			k, err := decodeNode(key)
			if err != nil {
				return nil, err
			}
			name, ok := k.(string)
			if !ok {
				return fail("mapping-key", "mapping keys must be strings")
			}
			if _, exists := result[name]; exists {
				return fail("duplicate-key", "duplicate mapping key")
			}
			value, err := decodeNode(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			result[name] = value
		}
		return result, nil
	case yaml.SequenceNode:
		if explicit && tag != "seq" {
			return fail("yaml-type", "tag does not match node kind")
		}
		result := make([]any, 0, len(n.Content))
		for _, child := range n.Content {
			v, err := decodeNode(child)
			if err != nil {
				return nil, err
			}
			result = append(result, v)
		}
		return result, nil
	case yaml.ScalarNode:
		if !utf8.ValidString(n.Value) || strings.ContainsRune(n.Value, 0) {
			return fail("unicode", "strings must be valid Unicode without NUL")
		}
		if !explicit {
			tag = "str"
			if n.Style == 0 {
				switch {
				case coreNull.MatchString(n.Value):
					tag = "null"
				case coreBool.MatchString(n.Value):
					tag = "bool"
				case coreInt.MatchString(n.Value):
					tag = "int"
				case coreFloat.MatchString(n.Value):
					tag = "float"
				}
			}
		}
		switch tag {
		case "str":
			return n.Value, nil
		case "null":
			if coreNull.MatchString(n.Value) {
				return nil, nil
			}
		case "bool":
			if coreBool.MatchString(n.Value) {
				return strings.EqualFold(n.Value, "true"), nil
			}
		case "int":
			if coreInt.MatchString(n.Value) {
				spelling := n.Value
				base := 10
				if strings.HasPrefix(spelling, "0o") {
					base = 8
					spelling = spelling[2:]
				} else if strings.HasPrefix(spelling, "0x") {
					base = 16
					spelling = spelling[2:]
				}
				number, ok := new(big.Int).SetString(spelling, base)
				if ok {
					return json.Number(number.String()), nil
				}
			}
		case "float":
			if coreFloat.MatchString(n.Value) {
				number, err := strconv.ParseFloat(n.Value, 64)
				if err == nil && !math.IsInf(number, 0) && !math.IsNaN(number) {
					return number, nil
				}
			}
		default:
			return fail("yaml-type", "unsupported standard tag")
		}
		return fail("tag-value", "invalid or non-finite tagged scalar")
	default:
		return fail("yaml-type", "unsupported YAML node")
	}
}
