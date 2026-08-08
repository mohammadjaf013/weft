package core

import (
	"context"
	"fmt"
	"io"
	"time"
)

// TaskInput is what a plugin receives.
type TaskInput struct {
	JobID    JobID
	TaskID   TaskID
	Kind     string
	InputRef string         // source asset reference
	InputURI string         // resolved local path/URI the executor prepared
	Profile  string         // requested profile
	Params   map[string]any // kind-specific parameters (languages, targets, ...)
	Storage  Storage        // where outputs go
	Progress ProgressFunc
	// Executor is injected so plugins can run ffmpeg/ffprobe without importing
	// Runtime. A plugin builds argv into Params["argv"] and calls Executor.Run.
	Executor Executor
}

// TaskOutput is what a plugin returns.
type TaskOutput struct {
	Assets []AssetRef
	Extra  map[string]any
}

type AssetRef struct {
	Kind  string // video|audio|subtitle|thumbnail|sprite|playlist|vtt|attachment
	Name  string
	URI   string
	Bytes int64
	// Dir is the destination subdirectory under the job's upload root, e.g.
	// "thumbnails" or "subtitle/fa". Empty means the upload root itself. The
	// upload plugin composes jobID/Dir/Name; producers set it so related
	// assets land together (poster+sprite+vtt, per-language subtitles, audio).
	Dir string
}

func (a AssetRef) String() string {
	return a.Kind + ":" + a.Name + " (" + formatBytes(a.Bytes) + ")"
}

func formatBytes(n int64) string {
	const unit = int64(1024)
	if n < unit {
		return "0 B"
	}
	div, exp := unit, 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Filter for listing jobs.
type JobFilter struct {
	Status   JobStatus
	Priority Priority
	Limit    int
	Cursor   string
}

// Store persists jobs, tasks, events and outbox rows. The Runtime SQLite
// implementation commits state + event + outbox in one transaction.
type Store interface {
	SaveJob(ctx context.Context, j Job) error
	LoadJob(ctx context.Context, id JobID) (Job, error)
	ListJobs(ctx context.Context, filter JobFilter) ([]Job, error)
	SaveTask(ctx context.Context, t Task) error
	LoadTask(ctx context.Context, id TaskID) (Task, error)
	ListTasks(ctx context.Context, jobID JobID) ([]Task, error)
	// UpdateTaskProgress writes only the progress/status fields of a task,
	// leaving lease, started_at, finished_at and error untouched. Used by the
	// worker's progress callback so frequent progress updates never clobber
	// the reservation metadata (which would break crash recovery).
	UpdateTaskProgress(ctx context.Context, id TaskID, status TaskStatus, progress float64) error
	AppendEvent(ctx context.Context, e Event) error // same tx as SaveJob/SaveTask in Runtime impl
	ListEvents(ctx context.Context, jobID JobID) ([]Event, error)
	Outbox() OutboxStore
	// In a transaction: write state + append event atomically.
	SaveJobEvent(ctx context.Context, j Job, e Event) error
	SaveTaskEvent(ctx context.Context, t Task, e Event) error
}

type OutboxStore interface {
	Enqueue(ctx context.Context, eventID, webhookID string, at time.Time) error
	ListPending(ctx context.Context, limit int, now time.Time) ([]OutboxRow, error)
	Mark(ctx context.Context, id string, delivered bool, errMsg string) error
}

type OutboxRow struct {
	ID            string
	EventID       string
	WebhookID     string
	Status        string // pending|delivered|dead_letter
	Attempts      int
	NextAttemptAt time.Time
	CreatedAt     time.Time
	DeliveredAt   *time.Time
}

type Executor interface {
	// One call per task runs the executor plugin.
	Run(ctx context.Context, task Task, in TaskInput) (Result, error)
	// Probe reads media metadata from an input file.
	Probe(ctx context.Context, path string) (MediaInfo, error)
}

// ProcessController is the optional executor extension that makes pause/resume
// real: the worker asks the executor to stop (SIGSTOP) or continue (SIGCONT)
// the OS process a task is currently running, instead of only flipping the
// job's status. Executors without a long-running child process (fakes in tests,
// remote executors) simply don't implement it and the worker treats pause as a
// no-op at the process level, which is a strict improvement for this executor.
type ProcessController interface {
	Pause(ctx context.Context, taskID TaskID) error
	Resume(ctx context.Context, taskID TaskID) error
}

type Result struct {
	ExitCode  int
	Output    string
	MediaInfo MediaInfo
}

type MediaInfo struct {
	DurationSec  float64
	Width        int
	Height       int
	FrameRate    float64
	HasVideo     bool
	HasAudio     bool
	HasSubtitles bool
	AudioStreams int
	VideoStreams int
	Format       string
}

type Storage interface {
	Open(ctx context.Context, ref AssetRef) (io.ReadCloser, error)
	Save(ctx context.Context, ref AssetRef, r io.Reader) error
	Delete(ctx context.Context, ref AssetRef) error
	Copy(ctx context.Context, src, dst AssetRef) error
	// Verify recomputes the checksum of a stored asset. Returns true if intact.
	Verify(ctx context.Context, ref AssetRef) (bool, error)
	// List returns the relative paths (slash-separated) of every file under
	// the storage root. Used by master rebuild to discover what was published.
	List(ctx context.Context) ([]string, error)
	Scheme() string // "local" | "ssh" | "s3" | ...
}

type EventBus interface {
	Publish(e Event)
	Subscribe(kind EventKind) <-chan Event
	SubscribeAll() <-chan Event
	Close()
}

type Lease interface {
	Reserve(ctx context.Context, taskID TaskID, workerID string, ttl time.Duration) error
	Heartbeat(ctx context.Context, taskID TaskID, workerID string) error
	Release(ctx context.Context, taskID TaskID) error
	ExpireStale(ctx context.Context) ([]TaskID, error) // returns requeued task ids
}

type DAGScheduler interface {
	Submit(ctx context.Context, job Job, tasks []Task) error
	NextReady(ctx context.Context) (*Task, error) // nil if nothing runnable
	MarkDone(ctx context.Context, taskID TaskID) error
	MarkFailed(ctx context.Context, taskID TaskID, err error) error
}

type StateMachine interface {
	Transition(ctx context.Context, jobID JobID, to JobStatus, reason string) error
	// TransitionJob transitions an already-loaded job. Both the status change
	// and its Event persist atomically. Implementations may still reload the
	// job in the write path.
	TransitionJob(ctx context.Context, job Job, to JobStatus, reason string) error
}

type Capabilities struct {
	Name            string
	SupportedInputs []string
	SupportedKinds  []string // task kinds this plugin handles, e.g. "video_encode"
	EstimatedCPU    float64  // 0..N cores
	EstimatedRAMMB  int
}

type Plugin interface {
	Name() string
	Capabilities() Capabilities
	Process(ctx context.Context, in TaskInput) (TaskOutput, error)
}
