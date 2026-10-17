package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Errorf("unexpected hash format: %s", h)
	}
	if ok, err := VerifyPassword("correct horse battery staple", h); err != nil || !ok {
		t.Errorf("right password rejected: ok=%v err=%v", ok, err)
	}
	if ok, _ := VerifyPassword("wrong", h); ok {
		t.Error("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse battery staple")
	if h == h2 {
		t.Error("two hashes of the same password are identical: salt not random")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA", "$argon2id$v=19$m=x$a$b"} {
		if _, err := VerifyPassword("x", bad); err == nil {
			t.Errorf("malformed hash %q accepted", bad)
		}
	}
}

func TestRoleOrdering(t *testing.T) {
	cases := []struct {
		have, need Role
		want       bool
	}{
		{RoleAdmin, RoleOperator, true},
		{RoleOperator, RoleOperator, true},
		{RoleViewer, RoleOperator, false},
		{RoleOperator, RoleAdmin, false},
		{Role("root"), RoleViewer, false},
	}
	for _, c := range cases {
		if got := c.have.AtLeast(c.need); got != c.want {
			t.Errorf("%s.AtLeast(%s) = %v, want %v", c.have, c.need, got, c.want)
		}
	}
}

func TestCSRFTokenBoundToSession(t *testing.T) {
	s := &Service{csrfSecret: []byte(strings.Repeat("k", 32))}
	a := &User{tokenHash: hashToken("session-a")}
	b := &User{tokenHash: hashToken("session-b")}
	if !s.CheckCSRF(a, s.CSRFToken(a)) {
		t.Error("own token rejected")
	}
	if s.CheckCSRF(a, s.CSRFToken(b)) {
		t.Error("token from another session accepted")
	}
	if s.CheckCSRF(a, "") || s.CheckCSRF(nil, s.CSRFToken(a)) {
		t.Error("empty token or missing user accepted")
	}
}
