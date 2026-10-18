package access

import (
	"os"
	"strings"
	"testing"
)

func TestSetPolicyGroupKeepsTheRestOfTheFile(t *testing.T) {
	data, err := os.ReadFile("../../../access/policy.yaml") // the real file in this repository
	if err != nil {
		t.Skip("repository policy not found:", err)
	}
	out, err := SetPolicyGroup(data, "puku-bot", []string{"bm3"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePolicy(out)
	if err != nil || len(p.Groups["puku-bot"]) != 1 {
		t.Fatalf("parse: %v %+v", err, p)
	}
	// Only the groups line changed: every other line, blank lines and comments included, is kept.
	before, after := strings.Split(string(data), "\n"), strings.Split(string(out), "\n")
	if len(after) != len(before)+1 {
		t.Fatalf("line count %d → %d:\n%s", len(before), len(after), out)
	}
	changed := 0
	for i, j := 0, 0; i < len(before); i, j = i+1, j+1 {
		if before[i] != after[j] {
			changed++
			j++ // the group entry inserted after "groups:"
		}
	}
	if changed != 1 || !strings.Contains(string(out), "groups:\n  puku-bot: [bm3]\n") {
		t.Errorf("unexpected diff (%d changed lines):\n%s", changed, out)
	}

	// Change, empty, remove; unknown servers and names YAML would misread are handled.
	out2, err := SetPolicyGroup(out, "puku-bot", []string{})
	if err != nil || !strings.Contains(string(out2), "  puku-bot: []\n") {
		t.Fatalf("empty group: %v\n%s", err, out2)
	}
	out3, err := SetPolicyGroup(out2, "puku-bot", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p3, _ := ParsePolicy(out3); len(p3.Groups) != 0 || string(out3) != string(data) {
		t.Errorf("removing the only group did not restore the file:\n%s", out3)
	}
	if _, err := SetPolicyGroup(data, "x", []string{"bm9"}); err == nil {
		t.Error("unknown server accepted")
	}
	if o, err := SetPolicyGroup(data, "123", []string{"bm3"}); err != nil {
		t.Errorf("numeric name: %v", err)
	} else if p, _ := ParsePolicy(o); len(p.Groups["123"]) != 1 {
		t.Errorf("numeric name not kept as a string:\n%s", o)
	}
}

func TestSetPolicyGroupAddsTheSectionWhenMissing(t *testing.T) {
	data := []byte("servers:\n  bm3: {host: h}\n\nmax_days: 3\nmax_sudo_days: 1\nadmins: [{github: a, email: a@b.io}]\n")
	out, err := SetPolicyGroup(data, "ops", []string{"bm3"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), string(data)) || !strings.HasSuffix(string(out), "groups:\n  ops: [bm3]\n") {
		t.Errorf("appended section:\n%s", out)
	}
}
