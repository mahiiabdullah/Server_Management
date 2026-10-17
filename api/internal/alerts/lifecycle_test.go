package alerts

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)

func TestLifecycle(t *testing.T) {
	timing := Timing{For: 5 * time.Minute, RepeatAfter: 4 * time.Hour}
	ts := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

	// Nothing open, condition false: nothing happens.
	if a := Step(nil, false, timing, t0); a.Create || a.NewState != "" {
		t.Fatalf("false with nothing open: %+v", a)
	}
	// Condition becomes true: pending, no notification.
	a := Step(nil, true, timing, t0)
	if !a.Create || a.NewState != Pending || a.Notify != "" {
		t.Fatalf("first true: %+v", a)
	}
	open := &Open{State: Pending, StartedAt: t0}
	// Still true but before for_seconds: stays pending.
	if a := Step(open, true, timing, ts(4)); a.NewState != "" || a.Notify != "" {
		t.Fatalf("pending too early: %+v", a)
	}
	// After for_seconds: fires once.
	a = Step(open, true, timing, ts(5))
	if a.NewState != Firing || a.Notify != "fired" {
		t.Fatalf("should fire: %+v", a)
	}
	notified := ts(5)
	open = &Open{State: Firing, StartedAt: t0, LastNotifiedAt: &notified}
	// Still firing within repeat window: no duplicate notification.
	if a := Step(open, true, timing, ts(60)); a.Notify != "" {
		t.Fatalf("duplicate notification: %+v", a)
	}
	// After repeat_after: one reminder.
	if a := Step(open, true, timing, ts(5+240)); a.Notify != "reminder" {
		t.Fatalf("want reminder: %+v", a)
	}
	// Acknowledged: no reminders.
	acked := &Open{State: Acknowledged, StartedAt: t0, LastNotifiedAt: &notified}
	if a := Step(acked, true, timing, ts(5+480)); a.Notify != "" {
		t.Fatalf("reminder after ack: %+v", a)
	}
	// One false check is not enough to resolve (flapping guard).
	a = Step(open, false, timing, ts(300))
	if a.NewState != "" || a.FalseStreak != 1 {
		t.Fatalf("single false check: %+v", a)
	}
	// Second false in a row resolves and notifies.
	open.FalseStreak = 1
	a = Step(open, false, timing, ts(301))
	if a.NewState != Resolved || a.Notify != "resolved" {
		t.Fatalf("should resolve: %+v", a)
	}
	// A true check in between resets the streak.
	open.FalseStreak = 1
	if a := Step(open, true, timing, ts(302)); a.FalseStreak != 0 {
		t.Fatalf("streak not reset: %+v", a)
	}
}

func TestPendingThatClearsNeverNotifies(t *testing.T) {
	open := &Open{State: Pending, StartedAt: t0}
	a := Step(open, false, Timing{For: 5 * time.Minute}, t0.Add(time.Minute))
	if a.NewState != Resolved || a.Notify != "" {
		t.Fatalf("pending flap should resolve silently: %+v", a)
	}
}

func TestZeroForFiresImmediately(t *testing.T) {
	a := Step(nil, true, Timing{}, t0)
	if !a.Create || a.NewState != Firing || a.Notify != "fired" {
		t.Fatalf("for=0: %+v", a)
	}
}
