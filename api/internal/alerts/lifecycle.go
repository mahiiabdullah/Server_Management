// Package alerts evaluates alert rules, keeps one incident per fingerprint, and
// queues notifications. Evaluation only writes rows; the notifier sends them.
package alerts

import "time"

// Lifecycle (plan section 11.3):
//
//	condition true,  no open alert          → pending (started_at = now)
//	condition true,  pending ≥ for_seconds  → firing, queue "fired"
//	condition true,  firing, not acked, last notification ≥ repeat_after ago → queue "reminder"
//	condition false, pending                → resolved silently (it never fired)
//	condition false twice in a row, firing/acknowledged → resolved, queue "resolved"
//	acknowledged                            → no more reminders

type State string

const (
	Pending      State = "pending"
	Firing       State = "firing"
	Acknowledged State = "acknowledged"
	Resolved     State = "resolved"
)

// Open is the stored state of an open incident.
type Open struct {
	State          State
	StartedAt      time.Time
	LastNotifiedAt *time.Time
	FalseStreak    int
}

// Rule timing parameters.
type Timing struct {
	For         time.Duration
	RepeatAfter time.Duration
}

// Action is what the evaluator must do for one fingerprint.
type Action struct {
	Create      bool   // insert a new pending alert
	NewState    State  // "" = unchanged
	Notify      string // "", "fired", "reminder", "resolved"
	FalseStreak int    // value to store
}

// ResolveAfterFalse is how many consecutive false checks resolve a firing alert.
const ResolveAfterFalse = 2

// Step decides the next action. open is nil when no incident is open.
func Step(open *Open, cond bool, t Timing, now time.Time) Action {
	if open == nil {
		if cond {
			if t.For <= 0 {
				return Action{Create: true, NewState: Firing, Notify: "fired"}
			}
			return Action{Create: true, NewState: Pending}
		}
		return Action{}
	}
	if cond {
		switch open.State {
		case Pending:
			if now.Sub(open.StartedAt) >= t.For {
				return Action{NewState: Firing, Notify: "fired"}
			}
		case Firing:
			if t.RepeatAfter > 0 && open.LastNotifiedAt != nil && now.Sub(*open.LastNotifiedAt) >= t.RepeatAfter {
				return Action{Notify: "reminder"}
			}
		}
		return Action{FalseStreak: 0}
	}
	if open.State == Pending {
		return Action{NewState: Resolved}
	}
	streak := open.FalseStreak + 1
	if streak >= ResolveAfterFalse {
		return Action{NewState: Resolved, Notify: "resolved", FalseStreak: streak}
	}
	return Action{FalseStreak: streak}
}
