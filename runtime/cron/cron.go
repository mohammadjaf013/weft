// Package cron runs a small set of named, standard-5-field-cron-scheduled
// jobs in-process — not the OS's cron/systemd timers, so the same binary
// behaves identically on any host. Weft only ever needs three fixed jobs
// (cleanup, benchmark, health_scan), so this is a minimal next-fire-time
// scheduler rather than a general-purpose cron library.
package cron

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrUnknownJob is wrapped into RunNow's error when name isn't registered, so
// callers (e.g. the REST handler) can distinguish "no such job" (404) from
// "the job ran and its own Run() returned an error" (job did run; the error
// is that job's failure, not a routing problem).
var ErrUnknownJob = errors.New("unknown cron job")

// Job is one named, cron-scheduled task.
type Job struct {
	Name     string
	Schedule string
	Run      func(ctx context.Context) error

	matcher fieldMatcher
}

// Status is a snapshot of a job for reporting (GET /cron, weft cron list).
type Status struct {
	Name     string     `json:"name"`
	Schedule string     `json:"schedule"`
	LastRun  *time.Time `json:"last_run,omitempty"`
	NextRun  *time.Time `json:"next_run,omitempty"`
	LastErr  string     `json:"last_error,omitempty"`
}

// Scheduler owns a fixed set of named jobs and runs each one when its cron
// schedule is due, or immediately on RunNow.
type Scheduler struct {
	mu   sync.Mutex
	jobs map[string]*jobState
	// now is overridable for tests.
	now func() time.Time
}

type jobState struct {
	job     Job
	lastRun *time.Time
	nextRun *time.Time
	lastErr string
}

func New() *Scheduler {
	return &Scheduler{jobs: map[string]*jobState{}, now: time.Now}
}

// Register adds a job. schedule is a standard 5-field cron expression
// (minute hour day-of-month month day-of-week); "*", "*/N", a literal
// number, or a comma-separated list of numbers are supported per field.
func (s *Scheduler) Register(j Job) error {
	m, err := parseSchedule(j.Schedule)
	if err != nil {
		return fmt.Errorf("cron: job %s: %w", j.Name, err)
	}
	j.matcher = m
	s.mu.Lock()
	defer s.mu.Unlock()
	next := nextFireTime(m, s.now())
	s.jobs[j.Name] = &jobState{job: j, nextRun: &next}
	return nil
}

// Run polls once per interval and runs any job whose NextRun has passed.
// Blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	now := s.now()
	var due []string
	s.mu.Lock()
	for name, js := range s.jobs {
		if js.nextRun != nil && !now.Before(*js.nextRun) {
			due = append(due, name)
		}
	}
	s.mu.Unlock()
	sort.Strings(due) // deterministic order when several fire in the same tick
	for _, name := range due {
		if err := s.RunNow(ctx, name); err != nil {
			log.Printf("cron: job %s: %v", name, err)
		}
	}
}

// RunNow runs a job immediately (regardless of schedule) and reschedules its
// NextRun from the current time. Used by both the periodic tick and the
// manual POST /cron/{job}/run trigger, so both paths share one code path and
// one LastRun/NextRun bookkeeping.
func (s *Scheduler) RunNow(ctx context.Context, name string) error {
	s.mu.Lock()
	js, ok := s.jobs[name]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownJob, name)
	}

	runErr := js.job.Run(ctx)

	now := s.now()
	next := nextFireTime(js.job.matcher, now)
	s.mu.Lock()
	js.lastRun = &now
	js.nextRun = &next
	if runErr != nil {
		js.lastErr = runErr.Error()
	} else {
		js.lastErr = ""
	}
	s.mu.Unlock()
	return runErr
}

// List returns every registered job's current status, sorted by name.
func (s *Scheduler) List() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Status, 0, len(s.jobs))
	for _, js := range s.jobs {
		out = append(out, Status{
			Name:     js.job.Name,
			Schedule: js.job.Schedule,
			LastRun:  js.lastRun,
			NextRun:  js.nextRun,
			LastErr:  js.lastErr,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// --- minimal 5-field cron expression parsing ---

type fieldMatcher struct {
	minute, hour, dom, month, dow func(int) bool
}

func parseSchedule(expr string) (fieldMatcher, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fieldMatcher{}, fmt.Errorf("expected 5 fields (min hour dom month dow), got %d in %q", len(fields), expr)
	}
	var m fieldMatcher
	var err error
	if m.minute, err = parseField(fields[0], 0, 59); err != nil {
		return m, fmt.Errorf("minute: %w", err)
	}
	if m.hour, err = parseField(fields[1], 0, 23); err != nil {
		return m, fmt.Errorf("hour: %w", err)
	}
	if m.dom, err = parseField(fields[2], 1, 31); err != nil {
		return m, fmt.Errorf("day-of-month: %w", err)
	}
	if m.month, err = parseField(fields[3], 1, 12); err != nil {
		return m, fmt.Errorf("month: %w", err)
	}
	if m.dow, err = parseField(fields[4], 0, 7); err != nil { // 0 and 7 both = Sunday
		return m, fmt.Errorf("day-of-week: %w", err)
	}
	return m, nil
}

// parseField supports "*", "*/N", a literal number, and comma-separated
// combinations of those (e.g. "0,30", "*/15").
func parseField(f string, min, max int) (func(int) bool, error) {
	var matchers []func(int) bool
	for _, part := range strings.Split(f, ",") {
		switch {
		case part == "*":
			matchers = append(matchers, func(int) bool { return true })
		case strings.HasPrefix(part, "*/"):
			n, err := strconv.Atoi(part[2:])
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("invalid step %q", part)
			}
			matchers = append(matchers, func(v int) bool { return (v-min)%n == 0 })
		default:
			n, err := strconv.Atoi(part)
			if err != nil || n < min || n > max {
				return nil, fmt.Errorf("invalid value %q (want %d-%d)", part, min, max)
			}
			matchers = append(matchers, func(v int) bool { return v == n })
		}
	}
	return func(v int) bool {
		for _, m := range matchers {
			if m(v) {
				return true
			}
		}
		return false
	}, nil
}

// maxLookaheadMinutes bounds nextFireTime's search so a schedule that (by
// construction, e.g. Feb 30) can never match doesn't loop forever — one
// non-leap year of minutes is far more than any real schedule needs.
const maxLookaheadMinutes = 366 * 24 * 60

// nextFireTime returns the first minute-aligned instant strictly after
// `after` that satisfies every field of m. Weekday 0 and 7 both mean Sunday.
func nextFireTime(m fieldMatcher, after time.Time) time.Time {
	t := after.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < maxLookaheadMinutes; i++ {
		dow := int(t.Weekday())
		if m.minute(t.Minute()) && m.hour(t.Hour()) && m.dom(t.Day()) && m.month(int(t.Month())) && (m.dow(dow) || (dow == 0 && m.dow(7))) {
			return t
		}
		t = t.Add(time.Minute)
	}
	// unreachable for any schedule produced by parseSchedule's supported
	// syntax, but return a far-future time rather than the zero value so a
	// caller doesn't treat it as "already due".
	return after.Add(maxLookaheadMinutes * time.Minute)
}
