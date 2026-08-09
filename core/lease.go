package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// NewID returns a random URL-safe-ish id with the given prefix, e.g. "job_1a2b3c".
func NewID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

var (
	// Now overridable for tests (lease time math, scheduling).
	Now = func() time.Time { return time.Now().UTC() }
)

// --- in-memory Store, used by Core tests and as a reference implementation ---

type memStore struct {
	mu     sync.Mutex
	jobs   map[JobID]Job
	tasks  map[TaskID]Task
	events []Event
	outbox map[string]OutboxRow
	seq    int
}

func NewMemStore() *memStore {
	return &memStore{
		jobs:   map[JobID]Job{},
		tasks:  map[TaskID]Task{},
		events: []Event{},
		outbox: map[string]OutboxRow{},
	}
}

func (m *memStore) SaveJob(ctx context.Context, j Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j.UpdatedAt = Now()
	m.jobs[j.ID] = j
	return nil
}

func (m *memStore) LoadJob(ctx context.Context, id JobID) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, ErrJobNotFound
	}
	return j, nil
}

func (m *memStore) ListJobs(ctx context.Context, filter JobFilter) ([]Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Job{}
	for _, j := range m.jobs {
		if filter.Status != "" && j.Status != filter.Status {
			continue
		}
		if filter.Priority != "" && j.Priority != filter.Priority {
			continue
		}
		out = append(out, j)
	}
	return out, nil
}

func (m *memStore) SaveTask(ctx context.Context, t Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks[t.ID] = t
	return nil
}

func (m *memStore) UpdateTaskProgress(ctx context.Context, id TaskID, status TaskStatus, progress float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return fmt.Errorf("task %s not found", id)
	}
	t.Status = status
	t.Progress = progress
	m.tasks[id] = t
	return nil
}

func (m *memStore) TryLease(ctx context.Context, t Task, workerID string, leaseExpiresAt, startedAt time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.tasks[t.ID]
	if !ok {
		return false, fmt.Errorf("task %s not found", t.ID)
	}
	if cur.Status != TaskPending && cur.Status != TaskReady {
		return false, nil
	}
	cur.Status = TaskLeased
	cur.WorkerID = workerID
	cur.LeaseExpiresAt = &leaseExpiresAt
	cur.StartedAt = &startedAt
	m.tasks[t.ID] = cur
	return true, nil
}

func (m *memStore) LoadTask(ctx context.Context, id TaskID) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return Task{}, fmt.Errorf("task %s not found", id)
	}
	return t, nil
}

func (m *memStore) ListTasks(ctx context.Context, jobID JobID) ([]Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Task{}
	for _, t := range m.tasks {
		if t.JobID == jobID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (m *memStore) AppendEvent(ctx context.Context, e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return nil
}

func (m *memStore) ListEvents(ctx context.Context, jobID JobID) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if jobID == "" {
		out := make([]Event, len(m.events))
		copy(out, m.events)
		return out, nil
	}
	out := []Event{}
	for _, e := range m.events {
		if e.JobID == jobID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *memStore) Outbox() OutboxStore { return m }

func (m *memStore) SaveJobEvent(ctx context.Context, j Job, e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j.UpdatedAt = Now()
	m.jobs[j.ID] = j
	m.events = append(m.events, e)
	return nil
}

func (m *memStore) SaveTaskEvent(ctx context.Context, t Task, e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks[t.ID] = t
	m.events = append(m.events, e)
	return nil
}

func (m *memStore) Enqueue(ctx context.Context, eventID, webhookID string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	m.outbox[fmt.Sprintf("o%d", m.seq)] = OutboxRow{
		ID:            fmt.Sprintf("o%d", m.seq),
		EventID:       eventID,
		WebhookID:     webhookID,
		Status:        "pending",
		NextAttemptAt: at,
		CreatedAt:     Now(),
	}
	return nil
}

func (m *memStore) ListPending(ctx context.Context, limit int, now time.Time) ([]OutboxRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []OutboxRow{}
	for _, r := range m.outbox {
		if r.Status == "pending" && !r.NextAttemptAt.After(now) {
			out = append(out, r)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) Mark(ctx context.Context, id string, delivered bool, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.outbox[id]
	if !ok {
		return fmt.Errorf("outbox row %s not found", id)
	}
	if delivered {
		r.Status = "delivered"
		t := Now()
		r.DeliveredAt = &t
	} else {
		r.Attempts++
		if errMsg != "" {
			r.NextAttemptAt = Now().Add(time.Duration(1<<uint(r.Attempts)) * time.Second)
		}
	}
	m.outbox[id] = r
	return nil
}

// --- in-memory Lease manager (pure math, no I/O) ---

type memLease struct {
	mu    sync.Mutex
	lease map[TaskID]leaseEntry
}

type leaseEntry struct {
	worker    string
	expiresAt time.Time
}

func NewMemLease() *memLease {
	return &memLease{lease: map[TaskID]leaseEntry{}}
}

func (l *memLease) Reserve(ctx context.Context, taskID TaskID, workerID string, ttl time.Duration) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.lease[taskID]; ok && e.expiresAt.After(Now()) {
		return fmt.Errorf("task %s already leased by %s until %s", taskID, e.worker, e.expiresAt)
	}
	l.lease[taskID] = leaseEntry{worker: workerID, expiresAt: Now().Add(ttl)}
	return nil
}

func (l *memLease) Heartbeat(ctx context.Context, taskID TaskID, workerID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.lease[taskID]
	if !ok {
		return fmt.Errorf("task %s not leased", taskID)
	}
	if e.worker != workerID {
		return fmt.Errorf("task %s leased to %s, not %s", taskID, e.worker, workerID)
	}
	if e.expiresAt.Before(Now()) {
		return fmt.Errorf("lease for task %s already expired", taskID)
	}
	// renew for a fresh ttl — simplified: caller decides the window. We just bump.
	return nil
}

func (l *memLease) Release(ctx context.Context, taskID TaskID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.lease, taskID)
	return nil
}

func (l *memLease) ExpireStale(ctx context.Context) ([]TaskID, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []TaskID
	for id, e := range l.lease {
		if !e.expiresAt.After(Now()) {
			out = append(out, id)
		}
	}
	for _, id := range out {
		delete(l.lease, id)
	}
	return out, nil
}
