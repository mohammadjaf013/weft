package core

import (
	"context"
	"encoding/json"
	"time"
)

// DAGScheduler implements dependency resolution and ready-task detection for a
// job's task graph. A Task becomes runnable the moment all of its dependencies
// are done — nothing waits on an artificial sequential order. It is pure logic
// over the Store; no I/O of its own.
type dagScheduler struct {
	store Store
	bus   EventBus
	sm    StateMachine
}

func NewDAGScheduler(store Store, bus EventBus, sm StateMachine) DAGScheduler {
	return &dagScheduler{store: store, bus: bus, sm: sm}
}

// Submit persists a Job and its tasks, emits JobCreated, and leaves the job
// queued. Task statuses start at pending; NextReady promotes them.
func (s *dagScheduler) Submit(ctx context.Context, job Job, tasks []Task) error {
	job.Status = JobQueued
	job.CreatedAt = Now()
	job.UpdatedAt = Now()
	for i := range tasks {
		tasks[i].Status = TaskPending
		tasks[i].StartedAt = nil
		tasks[i].FinishedAt = nil
	}
	payload, _ := json.Marshal(map[string]any{
		"job_id":  job.ID,
		"profile": job.Profile,
		"input":   job.InputRef,
	})
	evt := Event{ID: NewID("evt"), JobID: job.ID, Kind: EvtJobCreated, Payload: payload, CreatedAt: Now()}
	if err := s.store.SaveJobEvent(ctx, job, evt); err != nil {
		return err
	}
	for _, t := range tasks {
		if err := s.store.SaveTask(ctx, t); err != nil {
			return err
		}
	}
	s.bus.Publish(evt)
	return nil
}

// NextReady returns the highest-priority runnable task (all deps done, not
// already leased/running), promoting it to ready, or nil when nothing is
// runnable. Ready tasks that are still ready on a later poll are returned again
// so a crash between "ready" and "reserve" recovers automatically.
func (s *dagScheduler) NextReady(ctx context.Context) (*Task, error) {
	jobs, err := s.store.ListJobs(ctx, JobFilter{})
	if err != nil {
		return nil, err
	}
	// requeue any job the operator marked for retry, resetting its failed tasks
	for _, j := range jobs {
		if j.Status == JobRetry {
			if err := s.requeue(ctx, j); err != nil {
				return nil, err
			}
		}
	}
	best := (*Task)(nil)
	bestRank := 5
	// map of taskID -> status for dependency lookup
	byID := map[TaskID]Task{}
	jobByID := map[JobID]Job{}
	var allTasks []Task
	for _, j := range jobs {
		jobByID[j.ID] = j
		ts, err := s.store.ListTasks(ctx, j.ID)
		if err != nil {
			return nil, err
		}
		allTasks = append(allTasks, ts...)
		for _, t := range ts {
			byID[t.ID] = t
		}
	}
	for _, t := range allTasks {
		job := jobByID[t.JobID]
		// skip jobs that are done or cancelled or failed
		switch job.Status {
		case JobCompleted, JobCancelled, JobFailed, JobTimeout, JobDeadLetter:
			continue
		}
		if t.Status != TaskPending && t.Status != TaskReady {
			continue
		}
		if !s.depsMet(byID, t) {
			continue
		}
		rank := job.Priority.Rank()
		if best == nil || rank < bestRank {
			tt := t
			best = &tt
			bestRank = rank
		}
	}
	if best == nil {
		return nil, nil
	}
	if best.Status == TaskPending {
		best.Status = TaskReady
		if err := s.store.SaveTask(ctx, *best); err != nil {
			return nil, err
		}
	}
	return best, nil
}

func (s *dagScheduler) depsMet(byID map[TaskID]Task, t Task) bool {
	for _, dep := range t.DependsOn {
		d, ok := byID[dep]
		if !ok {
			return false
		}
		if d.Status != TaskDone {
			return false
		}
	}
	return true
}

// requeue returns a retry-marked job to the queue and resets its tasks that did
// not finish, so NextReady can pick them up again. The job itself re-enters the
// normal queued→reserved→running flow.
func (s *dagScheduler) requeue(ctx context.Context, job Job) error {
	tasks, err := s.store.ListTasks(ctx, job.ID)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.Status != TaskDone {
			t.Status = TaskPending
			t.Error = ""
			if err := s.store.SaveTask(ctx, t); err != nil {
				return err
			}
		}
	}
	return s.sm.TransitionJob(ctx, job, JobQueued, "")
}

// MarkDone completes a task, then finishes the job when every task is done.
func (s *dagScheduler) MarkDone(ctx context.Context, taskID TaskID) error {
	t, err := s.store.LoadTask(ctx, taskID)
	if err != nil {
		return err
	}
	t.Status = TaskDone
	t.FinishedAt = nowPtr(Now())
	t.Progress = 100
	evt := Event{ID: NewID("evt"), JobID: t.JobID, TaskID: t.ID, Kind: EvtPluginFinished, Payload: json.RawMessage(`{}`), CreatedAt: Now()}
	if err := s.store.SaveTaskEvent(ctx, t, evt); err != nil {
		return err
	}
	s.bus.Publish(evt)

	tasks, err := s.store.ListTasks(ctx, t.JobID)
	if err != nil {
		return err
	}
	allDone := len(tasks) > 0
	for _, x := range tasks {
		if x.Status != TaskDone {
			allDone = false
			break
		}
	}
	if allDone {
		job, err := s.store.LoadJob(ctx, t.JobID)
		if err != nil {
			return err
		}
		return s.finishJob(ctx, job)
	}
	return nil
}

// finishJob walks the job through the legal lifecycle to completed. A job that
// finished its tasks while still running must pass through uploading first;
// uploading→completed is the only legal terminal step.
func (s *dagScheduler) finishJob(ctx context.Context, job Job) error {
	switch job.Status {
	case JobQueued, JobReserved, JobRunning:
		if err := s.sm.TransitionJob(ctx, job, JobUploading, ""); err != nil {
			return err
		}
		job.Status = JobUploading
	}
	return s.sm.TransitionJob(ctx, job, JobCompleted, "")
}

// MarkFailed fails a task and, if it was the last runnable one, fails the job.
func (s *dagScheduler) MarkFailed(ctx context.Context, taskID TaskID, err error) error {
	t, err2 := s.store.LoadTask(ctx, taskID)
	if err2 != nil {
		return err2
	}
	t.Status = TaskFailed
	t.FinishedAt = nowPtr(Now())
	if err != nil {
		t.Error = err.Error()
	}
	payload, _ := json.Marshal(map[string]any{"task_id": t.ID, "error": t.Error})
	evt := Event{ID: NewID("evt"), JobID: t.JobID, TaskID: t.ID, Kind: EvtJobFailed, Payload: payload, CreatedAt: Now()}
	if err := s.store.SaveTaskEvent(ctx, t, evt); err != nil {
		return err
	}
	s.bus.Publish(evt)

	job, err3 := s.store.LoadJob(ctx, t.JobID)
	if err3 != nil {
		return err3
	}
	return s.sm.TransitionJob(ctx, job, JobFailed, t.Error)
}

func nowPtr(t time.Time) *time.Time {
	return &t
}
