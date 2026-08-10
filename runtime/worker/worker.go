// Package worker runs the DAG scheduler loop: pick a ready task, lease it,
// transition the job, run the plugin inside the registry sandbox, mark done or
// failed. One goroutine per worker; the Agent runs worker.Count of them.
package worker

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"sync"
	"time"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/runtime/registry"
)

type Options struct {
	Store    core.Store
	Bus      core.EventBus
	Sched    core.DAGScheduler
	SM       core.StateMachine
	Registry *registry.Registry
	Executor core.Executor
	LeaseTTL time.Duration
	Interval time.Duration
	// Storage resolves the destination core.Storage for an upload task from the
	// job. If nil, upload tasks run without a storage and the plugin errors.
	Storage func(job core.Job) (core.Storage, error)
	// OutputStore persists task outputs and lists a job's accumulated assets.
	// Optional; if nil, outputs are not retained and upload tasks get no assets.
	OutputStore OutputStore
	// InputResolver maps a job's InputRef to a local InputURI the executor can
	// read (e.g. local:/abs/path.mp4 → /abs/path.mp4). For a remote source
	// (job.SourceServerID set, or an http(s):// InputRef) this fetches the
	// file into a local cache — a potentially slow operation, hence the ctx
	// for cancellation. If nil, InputURI is left empty and plugins that
	// require a resolvable input fail fast.
	InputResolver func(ctx context.Context, job core.Job) (string, error)
	// Gate is a resource-aware scheduling hook: the worker only picks a task up
	// when Gate() returns true (and nil means "always pick"). Used to keep the
	// host under scheduler.max_cpu_percent / max_load_average: when over the
	// threshold the worker idles and leaves the task in the queue instead of
	// making the machine slower for everyone.
	Gate func() bool
	// Budget, when set, gates task pickup on the plugin's declared
	// Capabilities() cost (EstimatedCPU/EstimatedRAMMB) instead of measured
	// host usage — see Budget's doc comment for how this differs from Gate.
	// nil means "no declared-cost admission control" (only Gate applies).
	Budget *Budget
}

// OutputStore is the optional persistence needed to feed the upload task.
type OutputStore interface {
	SaveTaskOutputs(ctx context.Context, taskID core.TaskID, jobID core.JobID, assets []core.AssetRef) error
	ListJobOutputs(ctx context.Context, jobID core.JobID) ([]core.AssetRef, error)
}

type Worker struct {
	id   string
	opts Options
	// current state for /workers, read from a different goroutine (the API
	// handler backing GET /workers) than the one that writes it (this worker's
	// own Run loop) — guarded by mu.
	mu            sync.RWMutex
	currentTask   string
	status        string
	lastHeartbeat time.Time
}

func New(id string, opts Options) *Worker {
	if opts.LeaseTTL == 0 {
		opts.LeaseTTL = 5 * time.Minute
	}
	if opts.Interval == 0 {
		opts.Interval = 500 * time.Millisecond
	}
	return &Worker{id: id, opts: opts}
}

func (w *Worker) ID() string { return w.id }

func (w *Worker) CurrentTask() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.currentTask
}

func (w *Worker) Status() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.currentTask != "" {
		return "busy"
	}
	return "idle"
}

func (w *Worker) LastHeartbeat() time.Time {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.lastHeartbeat
}

func (w *Worker) setCurrentTask(id string) {
	w.mu.Lock()
	w.currentTask = id
	w.mu.Unlock()
}

func (w *Worker) setHeartbeat(t time.Time) {
	w.mu.Lock()
	w.lastHeartbeat = t
	w.mu.Unlock()
}

// Run polls the scheduler until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := w.cycle(ctx); err != nil {
				log.Printf("worker %s cycle error: %v", w.id, err)
			}
		}
	}
}

// cycle performs one scheduling step. When a resource Gate is configured and
// the host is over its threshold, the worker skips this cycle and leaves the
// queued task for later instead of competing for saturated CPU. When a
// Budget is configured, the task's plugin-declared cost must also fit inside
// the remaining budget — reserved for the lifetime of this cycle (release is
// deferred, since execute() blocks until the task fully finishes) so a wide
// DAG can't over-commit the box before Gate's measured CPU sampling would
// even notice.
//
// Known limitation: NextReady always returns the single highest-priority
// ready task. If that specific task doesn't fit the budget, this worker
// skips the whole cycle rather than picking a cheaper, lower-priority ready
// task instead — budget-aware task *selection* (not just admission) would
// need a scheduler change, out of scope here.
func (w *Worker) cycle(ctx context.Context) error {
	if w.opts.Gate != nil && !w.opts.Gate() {
		return nil
	}
	task, err := w.opts.Sched.NextReady(ctx)
	if err != nil {
		return err
	}
	if task == nil {
		return nil
	}

	if w.opts.Budget != nil {
		cpu, ramMB := w.taskCost(task.Kind)
		if !w.opts.Budget.TryReserve(cpu, ramMB) {
			// over budget this cycle; leave the task ready for a later cycle,
			// once something else finishes and frees capacity.
			return nil
		}
		defer w.opts.Budget.Release(cpu, ramMB)
	}

	// reserve a lease. reserve() sets currentTask before it's actually sure
	// the claim/transition succeeds, and both it and execute() have several
	// early-return error paths (store errors, failed state transitions,
	// storage resolution) that don't individually clear it -- this defer is
	// the single place that guarantees currentTask always goes back to ""
	// once this cycle is done, however it ends, so a transient error never
	// leaves the worker reporting "busy" forever with nothing to show for it.
	defer w.setCurrentTask("")
	reserved, err := w.reserve(ctx, *task)
	if err != nil {
		return err
	}
	if reserved == nil {
		// another worker claimed this task first; try again next cycle
		return nil
	}
	return w.execute(ctx, *reserved)
}

// taskCost looks up a task kind's declared resource cost from its plugin's
// Capabilities(). Unknown kind (shouldn't happen — the registry already
// resolved it once in NextReady's caller path) costs nothing, so a missing
// plugin can't wedge the budget shut.
func (w *Worker) taskCost(kind string) (cpu float64, ramMB int) {
	if w.opts.Registry == nil {
		return 0, 0
	}
	p, ok := w.opts.Registry.PluginFor(kind)
	if !ok {
		return 0, 0
	}
	caps := p.Capabilities()
	return caps.EstimatedCPU, caps.EstimatedRAMMB
}

// reserve atomically claims a task for this worker and transitions the job to
// running. When another worker already leased the task it returns nil, nil so
// the caller skips it (two workers both saw it runnable in NextReady).
func (w *Worker) reserve(ctx context.Context, task core.Task) (*core.Task, error) {
	w.setCurrentTask(string(task.ID))
	w.setHeartbeat(core.Now())

	// task-level transition: leased (atomic claim so >1 worker never
	// double-processes the same task)
	exp := core.Now().Add(w.opts.LeaseTTL)
	started := core.Now()
	task.Status = core.TaskLeased
	task.WorkerID = w.id
	task.LeaseExpiresAt = &exp
	task.StartedAt = &started
	claimed, err := w.opts.Store.TryLease(ctx, task, w.id, exp, started)
	if err != nil {
		return &task, err
	}
	if !claimed {
		w.setCurrentTask("")
		return nil, nil
	}

	// job-level: reserved (first time) → running → (uploading for upload tasks)
	job, err := w.opts.Store.LoadJob(ctx, task.JobID)
	if err != nil {
		return &task, err
	}
	switch job.Status {
	case core.JobQueued:
		if err := w.opts.SM.Transition(ctx, job.ID, core.JobReserved, ""); err != nil {
			return &task, err
		}
		fallthrough
	case core.JobReserved:
		if err := w.opts.SM.Transition(ctx, job.ID, core.JobRunning, ""); err != nil {
			return &task, err
		}
		fallthrough
	case core.JobRunning:
		// upload-phase tasks move the job into the uploading state
		if task.Kind == "upload" {
			if err := w.opts.SM.Transition(ctx, job.ID, core.JobUploading, ""); err != nil {
				return &task, err
			}
		}
	case core.JobPaused:
		if err := w.opts.SM.Transition(ctx, job.ID, core.JobResumed, ""); err != nil {
			return &task, err
		}
	case core.JobResumed:
		// upload-phase tasks move the job into the uploading state, same as the
		// JobRunning case — a resumed job's later tasks must still be able to
		// reach uploading/completed instead of staying stuck at "resumed".
		if task.Kind == "upload" {
			if err := w.opts.SM.Transition(ctx, job.ID, core.JobUploading, ""); err != nil {
				return &task, err
			}
		}
	}
	return &task, nil
}

// supervise watches job status while a task executes. When the operator pauses
// the job it signals the executor to stop the running process; when the job
// leaves paused it resumes. Executors without core.ProcessController (fakes,
// remote executors) get no process-level signal — pause degrades to the state
// machine only. Returns a stop func to detach the goroutine when the plugin
// returns.
func (w *Worker) supervise(ctx context.Context, task core.Task) func() {
	ctrl, ok := w.opts.Executor.(core.ProcessController)
	if !ok {
		return func() {}
	}
	quit := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(quit) }) }
	interval := w.opts.Interval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-quit:
				return
			case <-ticker.C:
			}
			job, err := w.opts.Store.LoadJob(ctx, task.JobID)
			if err != nil {
				continue
			}
			switch job.Status {
			case core.JobPaused:
				if err := ctrl.Pause(ctx, task.ID); err != nil && w.opts.Bus != nil {
					log.Printf("worker %s pause task %s: %v", w.id, task.ID, err)
				}
			case core.JobResumed, core.JobRunning:
				if err := ctrl.Resume(ctx, task.ID); err != nil && w.opts.Bus != nil {
					log.Printf("worker %s resume task %s: %v", w.id, task.ID, err)
				}
			}
		}
	}()
	return stop
}

// buildProgress returns a throttled ProgressFunc: it persists every update to
// the store (cheap, keeps jobs get live) and emits a TaskProgress bus event at
// most once per emitEvery (percent or seconds) so webhooks aren't flooded.
func buildProgress(opts Options, task core.Task, ctx context.Context) core.ProgressFunc {
	const emitEveryPct = 5.0
	const emitEverySec = 5 * time.Second
	var lastPct float64 = -1
	lastEmit := time.Time{}

	emit := func(pct float64) {
		payload, _ := json.Marshal(map[string]any{
			"task_id":          string(task.ID),
			"job_id":           string(task.JobID),
			"kind":             task.Kind,
			"progress_percent": pct,
		})
		ev := core.Event{
			ID:        core.NewID("evt"),
			JobID:     task.JobID,
			TaskID:    task.ID,
			Kind:      core.EvtTaskProgress,
			Payload:   payload,
			CreatedAt: time.Now().UTC(),
		}
		// persist so webhook delivery can load the event body (dispatcher reads
		// events from the store, not the bus)
		if opts.Store != nil {
			_ = opts.Store.AppendEvent(ctx, ev)
		}
		if opts.Bus != nil {
			opts.Bus.Publish(ev)
		}
	}

	return func(pct float64) {
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		_ = opts.Store.UpdateTaskProgress(ctx, task.ID, core.TaskRunning, pct)
		now := time.Now()
		step := (int)(pct / emitEveryPct)
		if step != (int)(lastPct/emitEveryPct) || now.Sub(lastEmit) > emitEverySec {
			lastPct = pct
			lastEmit = now
			emit(pct)
		}
	}
}

func (w *Worker) execute(ctx context.Context, task core.Task) error {
	job, err := w.opts.Store.LoadJob(ctx, task.JobID)
	if err != nil {
		return err
	}

	// build TaskInput
	in := core.TaskInput{
		JobID:    task.JobID,
		TaskID:   task.ID,
		Kind:     task.Kind,
		InputRef: job.InputRef,
		Profile:  job.Profile,
		Progress: buildProgress(w.opts, task, ctx),
		Params:   map[string]any{},
		Executor: w.opts.Executor,
	}
	// capture the task's process stderr so it can be inspected post-hoc; saved
	// once the plugin returns (success or failure).
	var logBuf cappedBuffer
	in.Log = &logBuf
	// per-task params set at creation time (e.g. subtitle lang) reach the plugin
	for k, v := range task.Params {
		in.Params[k] = v
	}

	// upload / update_master / poster_upload tasks: resolve destination
	// storage so the plugin can write directly to the published location.
	if task.Kind == "upload" || task.Kind == "update_master" || task.Kind == "poster_upload" {
		if w.opts.Storage != nil {
			st, serr := w.opts.Storage(job)
			if serr != nil {
				return serr
			}
			in.Storage = st
			// ssh.Storage caches its connection across every Save/Open/Delete
			// call made during this task (see plugins/storage/ssh) instead of
			// reconnecting per file; release it once the task is done. Storage
			// implementations with nothing to release (local, s3) don't
			// implement io.Closer, so this is a no-op for them.
			if closer, ok := st.(io.Closer); ok {
				defer closer.Close()
			}
		}
	}
	if task.Kind == "upload" {
		if w.opts.OutputStore != nil {
			assets, aerr := w.opts.OutputStore.ListJobOutputs(ctx, job.ID)
			if aerr != nil {
				return aerr
			}
			in.Params["assets"] = assets
		}
	}

	// resolve the source input to a local path the executor can read
	if w.opts.InputResolver != nil {
		uri, rerr := w.opts.InputResolver(ctx, job)
		if rerr != nil {
			w.opts.Sched.MarkFailed(ctx, task.ID, rerr)
			w.setCurrentTask("")
			return nil
		}
		in.InputURI = uri
	}

	// set running status
	task.Status = core.TaskRunning
	if err := w.opts.Store.SaveTask(ctx, task); err != nil {
		return err
	}

	// While the plugin runs (blocking on Executor.Run → ffmpeg), a background
	// supervisor watches the job status so Pause/Resume reach the OS process:
	// a job flipped to "paused" freezes the transcode (SIGSTOP), coming back
	// out continues it (SIGCONT). The caller blocks here, so the supervisor
	// runs in its own goroutine until the plugin returns.
	stopSupervise := w.supervise(ctx, task)
	out, err := w.opts.Registry.Process(ctx, task.Kind, in)
	stopSupervise()
	w.saveLog(ctx, task, logBuf.String())
	if err != nil {
		w.opts.Sched.MarkFailed(ctx, task.ID, err)
		w.setCurrentTask("")
		return nil // failure is surfaced via job state, not worker error
	}

	// persist produced assets so later upload tasks can collect them
	if w.opts.OutputStore != nil && len(out.Assets) > 0 {
		if err := w.opts.OutputStore.SaveTaskOutputs(ctx, task.ID, task.JobID, out.Assets); err != nil {
			w.opts.Sched.MarkFailed(ctx, task.ID, err)
			w.setCurrentTask("")
			return nil
		}
	}

	if err := w.opts.Sched.MarkDone(ctx, task.ID); err != nil {
		return err
	}
	w.setCurrentTask("")
	return nil
}

// saveLog persists the captured task log to the store when it supports task
// logs (sqlite.Store does). Best effort: a failed write must not fail the task.
func (w *Worker) saveLog(ctx context.Context, task core.Task, text string) {
	if text == "" {
		return
	}
	type logStore interface {
		SaveTaskLog(ctx context.Context, taskID core.TaskID, jobID core.JobID, log string) error
	}
	ls, ok := w.opts.Store.(logStore)
	if !ok {
		return
	}
	if err := ls.SaveTaskLog(ctx, task.ID, task.JobID, text); err != nil {
		log.Printf("worker %s save log for %s: %v", w.id, task.ID, err)
	}
}

// cappedBuffer is a bounded byte buffer (io.Writer) that keeps the tail of a
// command's output so runaway logs can't grow memory without bound. Task logs
// are further capped by the store.
type cappedBuffer struct {
	buf []byte
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.max == 0 {
		b.max = 256 * 1024
	}
	n := len(b.buf) + len(p)
	if n > b.max {
		if len(p) >= b.max {
			b.buf = append([]byte(nil), p[len(p)-b.max:]...)
			return len(p), nil
		}
		drop := n - b.max
		b.buf = append(b.buf[:0], b.buf[drop:]...)
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *cappedBuffer) String() string { return string(b.buf) }
