package application

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Rules mirror specification 001's closed objects. This is deliberately an
// input validator, not a general JSON Schema interpreter or driver preflight.
type rule struct {
	kind          string
	fields        map[string]*rule
	required      []string
	item          *rule
	firstNonempty bool
	variants      map[string]*rule
	constant      string
	check         func(string) bool
}

func object(fields map[string]*rule, required ...string) *rule {
	return &rule{kind: "object", fields: fields, required: required}
}
func text(check func(string) bool) *rule     { return &rule{kind: "string", check: check} }
func constant(value string) *rule            { return &rule{kind: "string", constant: value} }
func array(item *rule) *rule                 { return &rule{kind: "array", item: item} }
func variant(choices map[string]*rule) *rule { return &rule{variants: choices} }

func contract() *rule {
	str := text(nil)
	duration := &rule{kind: "integer"}
	argv := array(str)
	argv.firstNonempty = true
	path := text(relativePath)
	checkTimeout := duration
	httpCheck := object(map[string]*rule{"type": constant("http"), "url": text(func(s string) bool { return validURL(s, false) }), "timeoutSeconds": checkTimeout}, "type", "url")
	commandCheck := object(map[string]*rule{"type": constant("command"), "command": argv, "timeoutSeconds": checkTimeout}, "type", "command")
	exec := object(map[string]*rule{"apply": argv, "stop": argv, "env": {kind: "env"}}, "apply", "stop")
	compose := object(map[string]*rule{"files": array(path)}, "files")
	runtime := variant(map[string]*rule{
		"exec":           object(map[string]*rule{"type": constant("exec"), "exec": exec}, "type", "exec"),
		"docker-compose": object(map[string]*rule{"type": constant("docker-compose"), "docker-compose": compose}, "type", "docker-compose"),
	})
	spec := object(map[string]*rule{
		"source":    object(map[string]*rule{"type": constant("git"), "url": text(func(s string) bool { return validURL(s, true) }), "ref": text(gitRef), "path": path}, "type", "url", "ref"),
		"reconcile": object(map[string]*rule{"intervalSeconds": duration, "operationTimeoutSeconds": duration}),
		"runtime":   runtime,
		"health":    object(map[string]*rule{"timeoutSeconds": duration, "intervalSeconds": duration, "checks": array(variant(map[string]*rule{"http": httpCheck, "command": commandCheck}))}, "checks"),
		"rollback":  object(map[string]*rule{"enabled": {kind: "boolean"}}),
	}, "source", "runtime", "health")
	return object(map[string]*rule{"apiVersion": constant("pulltide.io/v1alpha1"), "kind": constant("Application"), "metadata": object(map[string]*rule{"name": text(func(s string) bool { return namePattern.MatchString(s) })}, "name"), "spec": spec}, "apiVersion", "kind", "metadata", "spec")
}

var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var ipvFuturePattern = regexp.MustCompile(`^[vV][0-9A-Fa-f]+\.[A-Za-z0-9._~!$&'()*+,;=:-]+$`)
var commitPattern = regexp.MustCompile(`^([0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

func (r *rule) validate(value any, path string) error {
	fail := func(code, message string) error { return issue(path, code, message) }
	if r.variants != nil {
		obj, ok := value.(map[string]any)
		if !ok {
			return fail("type", "expected mapping")
		}
		tag, ok := obj["type"].(string)
		if !ok {
			return issue(path+".type", "type", "required string discriminator")
		}
		selected, ok := r.variants[tag]
		if !ok {
			return issue(path+".type", "discriminator", "unsupported type")
		}
		return selected.validate(value, path)
	}
	switch r.kind {
	case "object", "env":
		obj, ok := value.(map[string]any)
		if !ok {
			return fail("type", "expected mapping")
		}
		for _, key := range r.required {
			if _, ok := obj[key]; !ok {
				return issue(path+"."+key, "required", "required field is missing")
			}
		}
		keys := make([]string, 0, len(obj))
		for key := range obj {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if r.kind == "env" {
				if !envPattern.MatchString(key) {
					return fail("property-name", "invalid environment variable name")
				}
				if err := text(nil).validate(obj[key], path+".*"); err != nil {
					return err
				}
				continue
			}
			field, ok := r.fields[key]
			if !ok {
				return fail("unknown-field", "unsupported field in mapping")
			}
			if err := field.validate(obj[key], path+"."+key); err != nil {
				return err
			}
		}
	case "string":
		s, ok := value.(string)
		if !ok {
			return fail("type", "expected string")
		}
		if strings.ContainsRune(s, 0) {
			return fail("unicode", "NUL is not permitted")
		}
		if r.constant != "" && s != r.constant {
			return fail("constant", "unexpected contract identifier")
		}
		if r.check != nil && !r.check(s) {
			return fail("value", "invalid value")
		}
	case "integer":
		n, ok := value.(json.Number)
		if !ok {
			return fail("type", "expected YAML integer without coercion")
		}
		number, err := strconv.Atoi(n.String())
		if err != nil || number < 1 || number > 86400 {
			return fail("range", "expected integer from 1 through 86400")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fail("type", "expected boolean")
		}
	case "array":
		values, ok := value.([]any)
		if !ok {
			return fail("type", "expected array")
		}
		if len(values) == 0 {
			return fail("min-items", "array must not be empty")
		}
		for i, v := range values {
			if err := r.item.validate(v, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		if r.firstNonempty && values[0].(string) == "" {
			return issue(path+"[0]", "executable", "executable name must not be empty")
		}
	}
	return nil
}

func relativePath(s string) bool {
	if s == "" || strings.HasPrefix(s, "/") {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}
func gitRef(s string) bool {
	if commitPattern.MatchString(s) {
		return true
	}
	if !strings.HasPrefix(s, "refs/heads/") && !strings.HasPrefix(s, "refs/tags/") {
		return false
	}
	if strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") || strings.Contains(s, "..") || strings.Contains(s, "@{") {
		return false
	}
	for _, c := range s {
		if c <= 0x20 || c == 0x7f || strings.ContainsRune("~^:?*[\\", c) {
			return false
		}
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// Validate complete URL syntax without DNS, authentication or filesystem access.
// Underlying net/url errors are intentionally discarded because they echo input.
func validURL(raw string, source bool) bool {
	if raw == "" || strings.Contains(raw, "#") || strings.ContainsFunc(raw, unicode.IsSpace) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || !u.IsAbs() {
		return false
	}
	if !strings.HasPrefix(raw[len(u.Scheme):], "://") {
		return false
	}
	authority := raw[len(u.Scheme)+3:]
	if i := strings.IndexAny(authority, "/?#"); i >= 0 {
		authority = authority[:i]
	}
	if strings.Count(authority, "@") > 1 {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if source {
		if strings.Contains(raw, "?") {
			return false
		}
		if scheme != "https" && scheme != "ssh" && scheme != "file" {
			return false
		}
	} else if scheme != "http" && scheme != "https" {
		return false
	}
	if u.User != nil {
		if !source || scheme != "ssh" || u.User.Username() == "" {
			return false
		}
		if _, password := u.User.Password(); password {
			return false
		}
	}
	if scheme == "file" {
		if !strings.HasPrefix(u.Path, "/") || u.User != nil {
			return false
		}
		if u.Host == "" {
			return true
		}
	}
	if u.Host == "" || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	if strings.HasPrefix(u.Host, "[") {
		// IPv6 literals may carry an RFC 6874 zone identifier.
		address := strings.SplitN(host, "%", 2)[0]
		if net.ParseIP(address) == nil && !ipvFuturePattern.MatchString(host) {
			return false
		}
	} else if strings.Contains(host, ":") || strings.ContainsAny(host, "[]\\") {
		return false
	}
	if strings.HasSuffix(u.Host, ":") {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 0 || n > 65535 {
			return false
		}
	}
	return true
}

// ValidateSource rechecks the source boundary for callers using typed models.
// It uses the same rules as document parsing and never echoes rejected values.
func ValidateSource(s Source) error {
	return contract().fields["spec"].fields["source"].validate(map[string]any{
		"type": s.Type, "url": s.URL, "ref": s.Ref, "path": s.Path,
	}, "spec.source")
}
