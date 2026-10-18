package alerts

import (
	"strings"
	"testing"
	"time"
)

func TestLinksSignAndVerify(t *testing.T) {
	l := NewLinks([]byte("0123456789abcdef0123456789abcdef"))
	now := time.Now()
	tok := l.Sign("user-1", LinkPause1d, now.Add(time.Hour))
	if uid, action, err := l.Verify(tok, now); err != nil || uid != "user-1" || action != LinkPause1d {
		t.Fatalf("verify = %q %q %v", uid, action, err)
	}
	if _, _, err := l.Verify(tok, now.Add(2*time.Hour)); err != ErrBadLink {
		t.Error("expired link accepted")
	}
	other := NewLinks([]byte("another secret, another key......"))
	if _, _, err := other.Verify(tok, now); err != ErrBadLink {
		t.Error("link signed with another key accepted")
	}
	// Changing the action inside the payload breaks the signature.
	forged := l.Sign("user-1", LinkPause7d, now.Add(time.Hour))
	p1, _, _ := strings.Cut(forged, ".")
	_, s0, _ := strings.Cut(tok, ".")
	if _, _, err := l.Verify(p1+"."+s0, now); err != ErrBadLink {
		t.Error("payload swapped under another signature accepted")
	}
}

func TestPersonEmailSaysWhyAndHowToPause(t *testing.T) {
	n := &Notifier{links: NewLinks([]byte("0123456789abcdef0123456789abcdef")), publicURL: "https://monitor.example", now: time.Now}
	uid, reason, server := "u1", "puku-bot, puku-web-chat", "bm1"
	m := n.render(pending{event: "fired", userID: &uid, reason: &reason, ruleName: "High CPU", severity: "warning",
		msg: "bm1 CPU at 97%", serverName: &server, startedAt: time.Now()})
	for _, want := range []string{"You get this because you are in: puku-bot, puku-web-chat", "https://monitor.example/alert-link?t=", "for 7 days:"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, m.Body)
		}
	}
	// Channel emails have no personal footer.
	if c := n.render(pending{event: "fired", ruleName: "x", severity: "warning", startedAt: time.Now()}); strings.Contains(c.Body, "Pause") {
		t.Error("channel email has pause links")
	}
}
