package cli

import (
	"bytes"
	"testing"
)

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		Args []string
		Want int
	}{
		{[]string{"validate", "../../tests/fixtures/contract/valid/exec-minimal.yaml"}, 0},
		{[]string{"validate", "../../tests/fixtures/contract/invalid/missing-name.yaml"}, 1},
		{[]string{"validate", "missing-file"}, 1},
		{nil, 2}, {[]string{"validate"}, 2}, {[]string{"run"}, 2},
	} {
		var out, stderr bytes.Buffer
		if got := Run(c.Args, &out, &stderr); got != c.Want {
			t.Fatalf("exit %d, expected %d", got, c.Want)
		}
		if c.Want != 0 && stderr.Len() == 0 {
			t.Fatal("missing error")
		}
	}
}
