// Package worker runs the DAG scheduler loop: pick a ready task, lease it,
// transition the job, run the plugin inside the registry sandbox, mark done or
// failed. One goroutine per worker; the Agent runs worker.Count of them.
package worker

import (
	"context"
	"encoding/json"
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
	// read (e.g. local:/abs/path.mp4 → /abs/path.mp4). If nil, InputURI is left
	// empty and plugins that require a resolvable input fail fast.
	InputResolver func(job core.Job) (string, error)
	// Gate is a resource-aware scheduling hook: the worker only picks a task up
	// when Gate() returns true (and nil means "always pick"). Used to keep the
	// host under scheduler.max_cpu_percent / max_load_average: when over the
	// threshold the worker idles and leaves the task in the queue instead of
	// making the machine slower for everyone.
	Gate func() bool
}

// OutputStore is the optional persistence needed to feed the upload task.
type OutputStore interface {
	SaveTaskOutputs(ctx context.Context, taskID core.TaskID, jobID core.JobID, assets []core.AssetRef) error
	ListJobOutputs(ctx context.Context, jobID core.JobID) ([]core.AssetRef, error)
}

type Worker struct {
	id   string
	opts Options
	// current state for /workers
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

func (w *Worker) CurrentTask() string { return w.currentTask }

func (w *Worker) Status() string {
	if w.currentTask != "" {
		return "busy"
	}
	return "idle"
}

func (w *Worker) LastHeartbeat() time.Time { return w.lastHeartbeat }

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
// queued task for later instead of competing for saturated CPU.
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

	// reserve a lease
	if err := w.reserve(ctx, *task); err != nil {
		return err
	}
	return w.execute(ctx, *task)
}

func (w *Worker) reserve(ctx context.Context, task core.Task) error {
	w.currentTask = string(task.ID)
	w.lastHeartbeat = core.Now()

	// task-level transition: leased
	task.Status = core.TaskLeased
	task.WorkerID = w.id
	exp := core.Now().Add(w.opts.LeaseTTL)
	task.LeaseExpiresAt = &exp
	started := core.Now()
	task.StartedAt = &started
	if err := w.opts.Store.SaveTask(ctx, task); err != nil {
		return err
	}

	// job-level: reserved (first time) → running → (uploading for upload tasks)
	job, err := w.opts.Store.LoadJob(ctx, task.JobID)
	if err != nil {
		return err
	}
	switch job.Status {
	case core.JobQueued:
		if err := w.opts.SM.Transition(ctx, job.ID, core.JobReserved, ""); err != nil {
			return err
		}
		fallthrough
	case core.JobReserved:
		if err := w.opts.SM.Transition(ctx, job.ID, core.JobRunning, ""); err != nil {
			return err
		}
		fallthrough
	case core.JobRunning:
		// upload-phase tasks move the job into the uploading state
		if task.Kind == "upload" {
			if err := w.opts.SM.Transition(ctx, job.ID, core.JobUploading, ""); err != nil {
				return err
			}
		}
	case core.JobPaused:
		if err := w.opts.SM.Transition(ctx, job.ID, core.JobResumed, ""); err != nil {
			return err
		}
	}
	return nil
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
	// per-task params set at creation time (e.g. subtitle lang) reach the plugin
	for k, v := range task.Params {
		in.Params[k] = v
	}

	// upload / update_master tasks: resolve destination storage so the plugin
	// can write directly to the published location.
	if task.Kind == "upload" || task.Kind == "update_master" {
		if w.opts.Storage != nil {
			st, serr := w.opts.Storage(job)
			if serr != nil {
				return serr
			}
			in.Storage = st
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
		uri, rerr := w.opts.InputResolver(job)
		if rerr != nil {
			w.opts.Sched.MarkFailed(ctx, task.ID, rerr)
			w.currentTask = ""
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
	if err != nil {
		w.opts.Sched.MarkFailed(ctx, task.ID, err)
		w.currentTask = ""
		return nil // failure is surfaced via job state, not worker error
	}

	// persist produced assets so later upload tasks can collect them
	if w.opts.OutputStore != nil && len(out.Assets) > 0 {
		if err := w.opts.OutputStore.SaveTaskOutputs(ctx, task.ID, task.JobID, out.Assets); err != nil {
			w.opts.Sched.MarkFailed(ctx, task.ID, err)
			w.currentTask = ""
			return nil
		}
	}

	if err := w.opts.Sched.MarkDone(ctx, task.ID); err != nil {
		return err
	}
	w.currentTask = ""
	return nil
}
