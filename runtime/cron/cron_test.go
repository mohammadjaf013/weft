package cron

import (
	"context"
	"testing"
	"time"
)

func mustParse(t *testing.T, expr string) fieldMatcher {
	t.Helper()
	m, err := parseSchedule(expr)
	if err != nil {
		t.Fatalf("parseSchedule(%q): %v", expr, err)
	}
	return m
}

func TestNextFireTimeDaily(t *testing.T) {
	m := mustParse(t, "0 3 * * *") // daily at 03:00, matches Cron.Cleanup default
	after := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	got := nextFireTime(m, after)
	want := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("next fire = %v, want %v", got, want)
	}
}

func TestNextFireTimeWeekly(t *testing.T) {
	m := mustParse(t, "0 4 * * 0") // weekly, Sunday 04:00, matches Cron.Benchmark default
	// 2026-01-01 is a Thursday
	after := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	got := nextFireTime(m, after)
	if got.Weekday() != time.Sunday || got.Hour() != 4 || got.Minute() != 0 {
		t.Fatalf("next fire = %v, want next Sunday 04:00", got)
	}
	if got.Before(after) {
		t.Fatalf("next fire %v is before %v", got, after)
	}
}

func TestNextFireTimeEveryFiveMinutes(t *testing.T) {
	m := mustParse(t, "*/5 * * * *") // matches Cron.HealthScan default
	after := time.Date(2026, 1, 1, 10, 2, 0, 0, time.UTC)
	got := nextFireTime(m, after)
	want := time.Date(2026, 1, 1, 10, 5, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("next fire = %v, want %v", got, want)
	}
}

func TestParseScheduleRejectsBadField(t *testing.T) {
	if _, err := parseSchedule("60 * * * *"); err == nil {
		t.Fatal("minute=60 should be rejected (0-59)")
	}
	if _, err := parseSchedule("* * * * *  *"); err == nil {
		t.Fatal("6 fields should be rejected")
	}
}

func TestSchedulerRunNowUpdatesStatus(t *testing.T) {
	s := New()
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return fixed }

	ran := 0
	if err := s.Register(Job{
		Name:     "cleanup",
		Schedule: "0 3 * * *",
		Run:      func(ctx context.Context) error { ran++; return nil },
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.RunNow(context.Background(), "cleanup"); err != nil {
		t.Fatal(err)
	}
	if ran != 1 {
		t.Fatalf("ran = %d, want 1", ran)
	}

	list := s.List()
	if len(list) != 1 {
		t.Fatalf("list = %v", list)
	}
	st := list[0]
	if st.LastRun == nil || !st.LastRun.Equal(fixed) {
		t.Fatalf("LastRun = %v, want %v", st.LastRun, fixed)
	}
	if st.NextRun == nil || !st.NextRun.After(fixed) {
		t.Fatalf("NextRun = %v, want after %v", st.NextRun, fixed)
	}
	if st.LastErr != "" {
		t.Fatalf("LastErr = %q, want empty", st.LastErr)
	}
}

func TestSchedulerRunNowRecordsError(t *testing.T) {
	s := New()
	wantErr := "boom"
	if err := s.Register(Job{
		Name:     "benchmark",
		Schedule: "0 4 * * 0",
		Run: func(ctx context.Context) error {
			return errString(wantErr)
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RunNow(context.Background(), "benchmark"); err == nil {
		t.Fatal("expected RunNow to return the job's error")
	}
	list := s.List()
	if len(list) != 1 || list[0].LastErr != wantErr {
		t.Fatalf("list = %+v, want LastErr=%q", list, wantErr)
	}
}

func TestSchedulerRunNowUnknownJob(t *testing.T) {
	s := New()
	if err := s.RunNow(context.Background(), "nope"); err == nil {
		t.Fatal("expected error for unknown job")
	}
}

func TestSchedulerTickRunsDueJobs(t *testing.T) {
	s := New()
	fixed := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC) // matches "0 3 * * *" exactly
	s.now = func() time.Time { return fixed }

	// Register computes NextRun from `now` at registration time using the
	// scheduler's now func — but Register itself runs at construction, before
	// we've "arrived" at 03:00, so seed NextRun in the past to simulate a tick
	// finding a due job.
	ran := make(chan struct{}, 1)
	if err := s.Register(Job{
		Name:     "cleanup",
		Schedule: "0 3 * * *",
		Run:      func(ctx context.Context) error { ran <- struct{}{}; return nil },
	}); err != nil {
		t.Fatal(err)
	}
	past := fixed.Add(-time.Minute)
	s.mu.Lock()
	s.jobs["cleanup"].nextRun = &past
	s.mu.Unlock()

	s.tick(context.Background())
	select {
	case <-ran:
	default:
		t.Fatal("tick did not run the due job")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
