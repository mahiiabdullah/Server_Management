package access

import "testing"

func TestPolicyApprovalsNeverCountTheAuthor(t *testing.T) {
	p := policy() // admins alice-gh and bob-gh
	approve := func(users ...string) []Review {
		var rs []Review
		for _, u := range users {
			rs = append(rs, Review{User: u, State: "APPROVED", CommitID: "head"})
		}
		return rs
	}
	cases := []struct {
		name, author, body string
		reviews            []Review
		want               int
	}{
		{"another admin approves", "alice-gh", "", approve("bob-gh"), 1},
		{"the author approves their own change", "alice-gh", "", approve("alice-gh"), 0},
		{"a non-admin approves", "alice-gh", "", approve("mallory"), 0},
		{"approval of an older commit", "alice-gh", "", []Review{{User: "bob-gh", State: "APPROVED", CommitID: "old"}}, 0},
		{"bot PR: the requesting admin cannot approve", "bm-access-bot[bot]", "Group change.\n\nRequested by: bob@example.com\n", approve("bob-gh"), 0},
		{"bot PR: another admin approves", "bm-access-bot[bot]", "Requested by: bob@example.com", approve("alice-gh", "bob-gh"), 1},
	}
	for _, c := range cases {
		if n, _ := PolicyApprovals(p, c.reviews, "head", c.author, c.body); n != c.want {
			t.Errorf("%s: %d approvals, want %d", c.name, n, c.want)
		}
	}
}
