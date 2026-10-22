package state

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestVersionedSerialization(t *testing.T) {
	st := &State{Version: 1, Generation: 1, InstallationID: "installation", Ownership: Ownership{Name: "app", Runtime: "exec", ResourceIdentity: "external"}, Active: Active{State: "absent"}, Health: Health{State: "not-applicable"}, Phase: "idle", Instances: map[string]Instance{}}
	data, err := encode(st)
	if err != nil {
		t.Fatal(err)
	}
	var decoded State
	if err := decode(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("installation"), []byte("instellotion"), 1)
	if err := decode(data, &decoded); err == nil {
		t.Fatal("accepted changed payload checksum")
	}
	st.Version = 99
	data, _ = encode(st)
	if decode(data, &decoded) != nil {
		t.Fatal("unexpected framing error")
	}
	if decoded.Validate() == nil {
		t.Fatal("accepted unknown state version")
	}
	for _, raw := range []string{`{"Version":1,"Version":1}`, `{"unknown":true}`, `{"Version":1} {"Version":1}`} {
		if strictJSON([]byte(raw), &envelope{}) == nil {
			t.Fatal("accepted duplicate/unknown/trailing JSON")
		}
	}
}
func TestCanonicalIdentityBytes(t *testing.T) {
	var out bytes.Buffer
	canonical(&out, map[string]any{"z": "<\n\t\"\\", "a": []any{true, json.Number("10"), "مرحبا😀"}})
	want := `{"a":[true,10,"مرحبا😀"],"z":"<\u000a\u0009\"\\"}`
	if out.String() != want {
		t.Fatalf("canonical encoding differs: %q", out.String())
	}
}

func TestUnicodeIsNeverSilentlyReplaced(t *testing.T) {
	for _, value := range []string{`{"value":"\ud800"}`, `{"value":"\udc00"}`, `{"value":"\ud800x"}`} {
		var decoded map[string]string
		if strictJSON([]byte(value), &decoded) == nil {
			t.Fatal("accepted invalid Unicode scalar")
		}
	}
	var decoded map[string]string
	if strictJSON([]byte(`{"value":"\ud83d\ude00"}`), &decoded) != nil || decoded["value"] != "😀" {
		t.Fatal("rejected valid Unicode pair")
	}
}
