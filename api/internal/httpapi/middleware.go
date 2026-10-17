package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"bm-monitor/api/internal/auth"
)

const sessionCookie = "bm_session"
const csrfCookie = "bm_csrf"

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

type ridKey struct{}

func requestIDFrom(ctx context.Context) string { s, _ := ctx.Value(ridKey{}).(string); return s }

// requestID accepts a well-formed X-Request-Id from Nginx or generates one,
// and attaches a logger that carries it.
func (a *API) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !validRequestID.MatchString(id) {
			b := make([]byte, 12)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), ridKey{}, id)
		ctx = context.WithValue(ctx, loggerKey{}, a.log.With("request_id", id))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }
func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// accessLog writes one structured line per request. Health probes log at debug level.
func (a *API) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		level := slog.LevelInfo
		if strings.HasSuffix(r.URL.Path, "/healthz") || strings.HasSuffix(r.URL.Path, "/readyz") {
			level = slog.LevelDebug
		}
		if rec.status >= 500 {
			level = slog.LevelError
		}
		attrs := []any{"method", r.Method, "path", r.URL.Path, "status", rec.status, "bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(), "ip", clientIP(r).String()}
		if u := auth.UserFrom(r.Context()); u != nil {
			attrs = append(attrs, "user_id", u.ID)
		}
		loggerFrom(r.Context()).Log(r.Context(), level, "http request", attrs...)
	})
}

func (a *API) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				loggerFrom(r.Context()).Error("panic", "value", fmt.Sprint(v), "stack", string(debug.Stack()))
				writeError(w, r, errInternal)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders applies API-level headers. Nginx adds HSTS, CSP and frame headers.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// session attaches the signed-in user, if any. It never rejects a request.
func (a *API) session(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err == nil && c.Value != "" {
			u, err := a.auth.Authenticate(r.Context(), c.Value)
			switch {
			case err == nil:
				r = r.WithContext(auth.WithUser(r.Context(), u))
			case err != auth.ErrNoSession:
				writeError(w, r, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isUnsafe(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

// csrf protects every state-changing request made with a session: the Origin
// (when the browser sends one) must be PUBLIC_URL, and X-CSRF-Token must match.
// Requests without a session are left to requireRole, which rejects them.
func (a *API) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isUnsafe(r.Method) {
			if origin := r.Header.Get("Origin"); origin != "" && origin != a.publicOrigin {
				writeError(w, r, &APIError{Status: http.StatusForbidden, Code: "bad_origin", Message: "Cross-origin requests are not allowed."})
				return
			}
			// Login is exempt from the token (a stale session must not block signing in);
			// it is still covered by the Origin check above and SameSite cookies.
			if u := auth.UserFrom(r.Context()); u != nil && r.URL.Path != "/api/v1/auth/login" &&
				!a.auth.CheckCSRF(u, r.Header.Get("X-CSRF-Token")) {
				writeError(w, r, errCSRF)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireRole rejects requests without a session (401) or with a lower role (403).
func requireRole(min auth.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := auth.UserFrom(r.Context())
			if u == nil {
				writeError(w, r, errUnauthenticated)
				return
			}
			if !u.Role.AtLeast(min) {
				writeError(w, r, errForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// timeout bounds every handler's context.
func timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// clientIP is the address Nginx reports in X-Forwarded-For when the direct peer
// is a trusted proxy (private network); otherwise the direct peer.
func clientIP(r *http.Request) netip.Addr {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	addr := peer.Addr().Unmap()
	if addr.IsPrivate() || addr.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if fwd, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
				return fwd.Unmap()
			}
		}
	}
	return addr
}

// ipLimiter is a per-key token bucket, for login and registration abuse protection.
type ipLimiter struct {
	mu      sync.Mutex
	every   time.Duration
	burst   int
	buckets map[string]*limiterEntry
}

type limiterEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

func newIPLimiter(every time.Duration, burst int) *ipLimiter {
	return &ipLimiter{every: every, burst: burst, buckets: map[string]*limiterEntry{}}
}

func (l *ipLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	e, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) > 10000 { // forget idle keys so memory stays bounded
			for k, v := range l.buckets {
				if now.Sub(v.seen) > time.Duration(l.burst)*l.every {
					delete(l.buckets, k)
				}
			}
		}
		e = &limiterEntry{lim: rate.NewLimiter(rate.Every(l.every), l.burst)}
		l.buckets[key] = e
	}
	e.seen = now
	return e.lim.Allow()
}

func (l *ipLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(clientIP(r).String()) {
			w.Header().Set("Retry-After", fmt.Sprint(int(l.every.Seconds())))
			writeError(w, r, &APIError{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "Too many attempts. Wait a few minutes and try again."})
			return
		}
		next.ServeHTTP(w, r)
	})
}
