package httpapi

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"bm-monitor/api/internal/alerts"
	"bm-monitor/api/internal/audit"
	"bm-monitor/api/internal/auth"
	"bm-monitor/api/internal/cost"
	"bm-monitor/api/internal/inventory"
	"bm-monitor/api/internal/metrics"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// mapError converts domain errors into API errors. Unknown errors stay as they
// are so writeError logs them and answers a generic 500.
func mapError(err error) error {
	var (
		fe *inventory.FieldError
		ce *inventory.ConflictError
		ve *cost.ValidationError
	)
	switch {
	case errors.Is(err, inventory.ErrNotFound), errors.Is(err, cost.ErrNotFound), errors.Is(err, alerts.ErrNotFound):
		return errNotFound
	case errors.As(err, &fe):
		return errValidation(map[string]string{fe.Field: fe.Message})
	case errors.As(err, &ce):
		return &APIError{Status: http.StatusConflict, Code: "conflict", Message: ce.Message, Fields: map[string]string{ce.Field: ce.Message}}
	case errors.As(err, &ve):
		return errValidation(map[string]string{ve.Field: ve.Message})
	case errors.Is(err, metrics.ErrUnavailable):
		return &APIError{Status: http.StatusServiceUnavailable, Code: "metrics_unavailable", Message: "Prometheus is not reachable, so live metrics are unavailable right now."}
	case errors.Is(err, alerts.ErrSystemRule):
		return &APIError{Status: http.StatusConflict, Code: "system_rule", Message: err.Error()}
	case errors.Is(err, alerts.ErrNotAckable):
		return &APIError{Status: http.StatusConflict, Code: "not_ackable", Message: err.Error()}
	}
	return err
}

func fail(w http.ResponseWriter, r *http.Request, err error) { writeError(w, r, mapError(err)) }

// event pre-fills an audit event with who, from where, and the request id.
func (a *API) event(r *http.Request) audit.Event {
	ev := audit.Event{IP: clientIP(r), RequestID: requestIDFrom(r.Context())}
	if u := auth.UserFrom(r.Context()); u != nil {
		ev.ActorID = u.ID
	}
	return ev
}

// idParam returns a UUID path parameter; anything else is a 404.
func idParam(r *http.Request, name string) (string, bool) {
	id := strings.ToLower(chi.URLParam(r, name))
	return id, uuidRe.MatchString(id)
}

type list[T any] struct {
	Items    []T `json:"items"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
}

// paging reads ?page and ?page_size (default 25, max 100).
func paging(r *http.Request) (int, int, error) {
	page, size := 1, 25
	fields := map[string]string{}
	if v := r.URL.Query().Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			fields["page"] = "Must be a positive integer."
		}
		page = n
	}
	if v := r.URL.Query().Get("page_size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			fields["page_size"] = "Must be between 1 and 100."
		}
		size = n
	}
	if len(fields) > 0 {
		return 0, 0, errValidation(fields)
	}
	return page, size, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseDate accepts RFC 3339 or YYYY-MM-DD (midnight in loc).
func parseDate(s string, loc *time.Location) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

func ptr[T any](v T) *T { return &v }
