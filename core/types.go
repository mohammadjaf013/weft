// Package core is Layer 1 of Weft. It is pure Go — zero imports outside the
// standard library and its own interfaces. It never touches a disk, a network
// socket, or a database directly. Everything below it (Runtime, Plugins) is a
// swappable implementation of the interfaces in this package.
package core

import (
	"encoding/json"
	"time"
)

type JobID string
type TaskID string

type JobStatus string

const (
	JobQueued     JobStatus = "queued"
	JobReserved   JobStatus = "reserved"
	JobRunning    JobStatus = "running"
	JobUploading  JobStatus = "uploading"
	JobCompleted  JobStatus = "completed"
	JobFailed     JobStatus = "failed"
	JobCancelled  JobStatus = "cancelled"
	JobTimeout    JobStatus = "timeout"
	JobRetry      JobStatus = "retry"
	JobDeadLetter JobStatus = "dead_letter"
	JobPaused     JobStatus = "paused"
	JobResumed    JobStatus = "resumed"
)

func (s JobStatus) String() string { return string(s) }

type TaskStatus string

const (
	TaskPending TaskStatus = "pending" // waiting on dependencies
	TaskReady   TaskStatus = "ready"   // dependencies met, in queue
	TaskLeased  TaskStatus = "leased"  // reserved by a worker
	TaskRunning TaskStatus = "running"
	TaskDone    TaskStatus = "done"
	TaskFailed  TaskStatus = "failed"
)

func (s TaskStatus) String() string { return string(s) }

type EventKind string

const (
	EvtJobCreated       EventKind = "JobCreated"
	EvtJobStarted       EventKind = "JobStarted"
	EvtJobProgress      EventKind = "JobProgress"
	EvtJobPaused        EventKind = "JobPaused"
	EvtJobResumed       EventKind = "JobResumed"
	EvtJobCancelled     EventKind = "JobCancelled"
	EvtJobFinished      EventKind = "JobFinished"
	EvtJobFailed        EventKind = "JobFailed"
	EvtPipelineStarted  EventKind = "PipelineStarted"
	EvtPipelineFinished EventKind = "PipelineFinished"
	EvtPluginStarted    EventKind = "PluginStarted"
	EvtPluginFinished   EventKind = "PluginFinished"
	EvtNodeJoined       EventKind = "NodeJoined"
	EvtNodeLeft         EventKind = "NodeLeft"
	EvtStorageUploaded  EventKind = "StorageUploaded"
	EvtStorageFailed    EventKind = "StorageFailed"
	EvtNodeHealth       EventKind = "NodeHealthSampled"
	EvtTaskProgress     EventKind = "TaskProgress"
)

type Priority string

const (
	PriorityEmergency  Priority = "emergency"
	PriorityHigh       Priority = "high"
	PriorityNormal     Priority = "normal"
	PriorityLow        Priority = "low"
	PriorityBackground Priority = "background"
)

var Priorities = []Priority{PriorityEmergency, PriorityHigh, PriorityNormal, PriorityLow, PriorityBackground}

// Rank returns the numeric rank of a priority band. Lower = more urgent.
func (p Priority) Rank() int {
	for i, pp := range Priorities {
		if pp == p {
			return i
		}
	}
	return 2 // default: normal
}

func (p Priority) Valid() bool {
	for _, pp := range Priorities {
		if pp == p {
			return true
		}
	}
	return false
}

type Job struct {
	ID            JobID
	Status        JobStatus
	Priority      Priority
	Profile       string
	DestinationID int
	// DestPath is an optional subdirectory under the destination storage's
	// base path (e.g. "movie" or "series"). Empty means the storage root.
	// Lets one storage server fan out to many directories.
	DestPath string
	// SourceServerID, when non-zero, means InputRef is a relative path (not a
	// local filesystem path or a bare URL) resolved against a REGISTERED
	// storage server's root — the same servers already used for job output
	// (weft storage add). 0 means InputRef is resolved the old way: a local
	// path, or a directly-fetchable http(s):// URL.
	SourceServerID int
	InputRef       string
	Verified  bool
	// DeleteSource removes the source input file from disk once the job has
	// completed successfully (upload finished). Safe default is false.
	DeleteSource bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Error        string
}

type Task struct {
	ID             TaskID
	JobID          JobID
	Kind           string
	Status         TaskStatus
	Progress       float64 // 0..100
	DependsOn      []TaskID
	WorkerID       string
	LeaseExpiresAt *time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
	Error          string
	// Params carries per-task configuration supplied at creation time (e.g.
	// {"lang":"fa"} for a subtitle task). The worker forwards these into
	// TaskInput.Params so a plugin can honor job-level options.
	Params map[string]any
}

type Event struct {
	ID        string
	JobID     JobID
	TaskID    TaskID
	Kind      EventKind
	Payload   json.RawMessage
	CreatedAt time.Time
}

// ProgressFunc is handed to plugins so they can stream progress back to the
// Runtime without holding any reference to Core internals.
type ProgressFunc func(pct float64)
