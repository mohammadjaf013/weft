package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// StateMachine is the only place a Job's status is allowed to change. Every
// legal transition loads the Job, applies the change, and persists it together
// with an Event in one atomic Store call — this is what makes Event Sourcing
// accurate. Illegal transitions are rejected with ErrInvalidTransition.
type stateMachine struct {
	store Store
	bus   EventBus
}

var (
	ErrInvalidTransition = errors.New("invalid state transition")
	ErrJobNotFound       = errors.New("job not found")
)

func NewStateMachine(store Store, bus EventBus) StateMachine {
	return &stateMachine{store: store, bus: bus}
}

// allowedTransitions lists every legal JobStatus → JobStatus pair.
var allowedTransitions = map[JobStatus]map[JobStatus]bool{
	JobQueued:     {JobReserved: true, JobCancelled: true, JobRetry: true},
	JobReserved:   {JobRunning: true, JobFailed: true, JobTimeout: true, JobCancelled: true, JobRetry: true, JobDeadLetter: true},
	JobRunning:    {JobUploading: true, JobFailed: true, JobTimeout: true, JobCancelled: true, JobRetry: true, JobPaused: true},
	JobUploading:  {JobCompleted: true, JobFailed: true, JobTimeout: true, JobCancelled: true, JobRetry: true},
	JobPaused:     {JobResumed: true, JobCancelled: true, JobTimeout: true, JobFailed: true},
	JobResumed:    {JobUploading: true, JobFailed: true, JobTimeout: true, JobCancelled: true, JobRetry: true, JobPaused: true},
	JobRetry:      {JobQueued: true, JobDeadLetter: true},
	JobFailed:     {JobRetry: true},
	JobTimeout:    {JobRetry: true, JobDeadLetter: true},
	JobDeadLetter: {JobRetry: true},
	// JobCompleted and JobCancelled are terminal.
}

// eventFor picks the event emitted for a given (from, to) transition. Defaults
// to EvtJobProgress for transitions with no dedicated event.
func eventFor(from, to JobStatus) EventKind {
	switch to {
	case JobCompleted:
		return EvtJobFinished
	case JobFailed:
		return EvtJobFailed
	case JobCancelled:
		return EvtJobCancelled
	case JobPaused:
		return EvtJobPaused
	case JobResumed:
		return EvtJobResumed
	case JobRunning:
		if from == JobPaused {
			return EvtJobResumed
		}
		return EvtJobStarted
	}
	return EvtJobProgress
}

func (sm *stateMachine) Transition(ctx context.Context, jobID JobID, to JobStatus, reason string) error {
	job, err := sm.store.LoadJob(ctx, jobID)
	if err != nil {
		return err
	}
	return sm.TransitionJob(ctx, job, to, reason)
}

// TransitionJob transitions an already-loaded job, avoiding a redundant store
// read. It still reloads inside the write path if the store needs it — the
// contract is: the job's status change and its event persist atomically.
func (sm *stateMachine) TransitionJob(ctx context.Context, job Job, to JobStatus, reason string) error {
	if !allowedTransitions[job.Status][to] {
		return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, job.Status, to)
	}
	evt := eventFor(job.Status, to)
	if reason != "" {
		job.Error = reason
	}
	job.Status = to
	job.UpdatedAt = Now()
	payload, _ := json.Marshal(map[string]any{
		"job_id": job.ID,
		"status": to,
		"error":  reason,
		"at":     Now().Unix(),
	})
	e := Event{
		ID:        NewID("evt"),
		JobID:     job.ID,
		Kind:      evt,
		Payload:   payload,
		CreatedAt: Now(),
	}
	if err := sm.store.SaveJobEvent(ctx, job, e); err != nil {
		return err
	}
	sm.bus.Publish(e)
	return nil
}
