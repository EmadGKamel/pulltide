package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"unicode/utf8"

	"pulltide/internal/application"
)

type envelope struct {
	Version int
	Payload json.RawMessage
	SHA256  string
}

func encode(value any) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, ErrCorrupt
	}
	sum := sha256.Sum256(payload)
	return json.Marshal(envelope{FormatVersion, payload, hex.EncodeToString(sum[:])})
}
func decode(data []byte, value any) error {
	var record envelope
	if err := strictJSON(data, &record); err != nil {
		return ErrCorrupt
	}
	if record.Version != FormatVersion || !digestPattern.MatchString(record.SHA256) {
		return ErrCorrupt
	}
	sum := sha256.Sum256(record.Payload)
	if hex.EncodeToString(sum[:]) != record.SHA256 {
		return ErrCorrupt
	}
	return strictJSON(record.Payload, value)
}
func strictJSON(data []byte, value any) error {
	if !utf8.Valid(data) || !validEscapedUnicode(data) {
		return ErrCorrupt
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := checkJSON(d); err != nil {
		return ErrCorrupt
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrCorrupt
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return ErrCorrupt
	}
	return nil
}
func checkJSON(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return ErrCorrupt
			}
			seen[name] = true
			if err := checkJSON(d); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return ErrCorrupt
		}
	case json.Delim('['):
		for d.More() {
			if err := checkJSON(d); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return ErrCorrupt
		}
	case json.Delim('}'), json.Delim(']'):
		return ErrCorrupt
	}
	return nil
}
func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func copyState(s *State) (*State, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, ErrCorrupt
	}
	var result State
	if err := strictJSON(b, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ConfigurationIdentity implements 001's existing identity-v1 definition; it
// hashes normalized spec only, without JSON's optional HTML/control shorthand.
func ConfigurationIdentity(spec application.Spec) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", ErrCorrupt
	}
	var normalized any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&normalized); err != nil {
		return "", ErrCorrupt
	}
	var out bytes.Buffer
	canonical(&out, normalized)
	sum := sha256.Sum256(out.Bytes())
	return "identity-v1:" + hex.EncodeToString(sum[:]), nil
}
func canonical(out *bytes.Buffer, v any) {
	switch x := v.(type) {
	case map[string]any:
		out.WriteByte('{')
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			canonical(out, k)
			out.WriteByte(':')
			canonical(out, x[k])
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				out.WriteByte(',')
			}
			canonical(out, item)
		}
		out.WriteByte(']')
	case string:
		out.WriteByte('"')
		for _, r := range x {
			switch {
			case r == '"' || r == '\\':
				out.WriteByte('\\')
				out.WriteRune(r)
			case r < 32:
				out.WriteString(`\u00`)
				const digits = "0123456789abcdef"
				out.WriteByte(digits[byte(r)>>4])
				out.WriteByte(digits[byte(r)&15])
			default:
				out.WriteRune(r)
			}
		}
		out.WriteByte('"')
	case json.Number:
		out.WriteString(string(x))
	case bool:
		out.WriteString(strconv.FormatBool(x))
	case nil:
		out.WriteString("null")
	}
}
func validateSaved(spec application.Spec, name, identity string) error {
	app := application.Application{APIVersion: "pulltide.io/v1alpha1", Kind: "Application", Metadata: application.Metadata{Name: name}, Spec: spec}
	data, err := json.Marshal(app)
	if err != nil {
		return ErrCorrupt
	}
	parsed, err := application.Parse(data)
	if err != nil || !equalJSON(parsed.Spec, spec) {
		return ErrCorrupt
	}
	actual, err := ConfigurationIdentity(spec)
	if err != nil || identity != actual {
		return ErrCorrupt
	}
	return nil
}

// encoding/json otherwise replaces lone escaped surrogate code units silently.
func validEscapedUnicode(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		if i+1 >= len(data) {
			return false
		}
		if data[i+1] != 'u' {
			i++
			continue
		}
		if i+6 > len(data) {
			return false
		}
		value, err := strconv.ParseUint(string(data[i+2:i+6]), 16, 16)
		if err != nil {
			return false
		}
		i += 5
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+7 > len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
