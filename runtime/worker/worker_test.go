package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/runtime/registry"
	"github.com/mohammadjaf013/weft/runtime/store/sqlite"
)

type testPlugin struct {
	name  string
	kinds []string
	err   error
	delay time.Duration
}

func (p *testPlugin) Name() string { return p.name }
func (p *testPlugin) Capabilities() core.Capabilities {
	return core.Capabilities{Name: p.name, SupportedKinds: p.kinds}
}
func (p *testPlugin) Process(ctx context.Context, in core.TaskInput) (core.TaskOutput, error) {
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return core.TaskOutput{}, ctx.Err()
		}
	}
	if p.err != nil {
		return core.TaskOutput{}, p.err
	}
	return core.TaskOutput{Assets: []core.AssetRef{{Kind: "video", Name: "out.mp4", URI: "local:out.mp4"}}}, nil
}

type env struct {
	store  *sqlite.Store
	worker *Worker
	ctx    context.Context
	cancel context.CancelFunc
}

func newWorkerEnv(t *testing.T, plugins ...core.Plugin) *env {
	t.Helper()
	store, err := sqlite.OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	bus := core.NewEventBus()
	sm := core.NewStateMachine(store, bus)
	sched := core.NewDAGScheduler(store, bus, sm)

	reg := registry.New()
	for _, p := range plugins {
		if err := reg.Register(p); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := New("worker-1", Options{
		Store:    store,
		Bus:      bus,
		Sched:    sched,
		SM:       sm,
		Registry: reg,
		Executor: fakeExec{},
		LeaseTTL: time.Minute,
		Interval: 10 * time.Millisecond,
	})
	e := &env{store: store, worker: w, ctx: ctx, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		bus.Close()
		store.Close()
	})
	return e
}

type fakeExec struct{}

func (fakeExec) Run(ctx context.Context, task core.Task, in core.TaskInput) (core.Result, error) {
	return core.Result{ExitCode: 0}, nil
}
func (fakeExec) Probe(ctx context.Context, path string) (core.MediaInfo, error) {
	return core.MediaInfo{}, nil
}

// waitStatus blocks until the job reaches one of the given statuses.
func waitStatus(t *testing.T, e *env, jobID core.JobID, want ...core.JobStatus) core.Job {
	t.Helper()
	ctx := context.Background()
	deadline := time.After(5 * time.Second)
	for {
		j, err := e.store.LoadJob(ctx, jobID)
		if err == nil {
			for _, s := range want {
				if j.Status == s {
					return j
				}
			}
		}
		select {
		case <-deadline:
			t.Fatalf("job %s never reached %v (last: %v)", jobID, want, j.Status)
		case <-time.After(15 * time.Millisecond):
		}
	}
}

func TestWorkerCompletesJob(t *testing.T) {
	e := newWorkerEnv(t, &testPlugin{name: "v", kinds: []string{"video_encode"}})
	ctx := context.Background()

	job := core.Job{ID: "j1", Status: core.JobQueued, Priority: core.PriorityNormal, Profile: "test"}
	if err := e.store.SaveJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	e.store.SaveTask(ctx, core.Task{ID: "t1", JobID: "j1", Kind: "video_encode", Status: core.TaskPending})

	go e.worker.Run(e.ctx)
	waitStatus(t, e, "j1", core.JobCompleted)

	tk, _ := e.store.LoadTask(ctx, "t1")
	if tk.Status != core.TaskDone {
		t.Fatalf("task status = %s, want done", tk.Status)
	}
	evs, _ := e.store.ListEvents(ctx, "j1")
	if len(evs) == 0 {
		t.Fatal("no events recorded")
	}
}

// TestWorkerKeepsLeaseThroughRunning verifies a task keeps its lease_expires_at
// after the worker flips it to running. A running task with a cleared lease
// can never be recovered: RequeueExpired only touches rows with a non-null
// lease, so a worker that dies mid-encode strands the task (and its job) in
// "running" forever. The execute step must re-save the reserved task — the one
// carrying the lease — not the original pre-reserve copy.
func TestWorkerKeepsLeaseThroughRunning(t *testing.T) {
	e := newWorkerEnv(t, &testPlugin{name: "v", kinds: []string{"video_encode"}})
	ctx := context.Background()

	job := core.Job{ID: "j1", Status: core.JobQueued, Priority: core.PriorityNormal, Profile: "test"}
	if err := e.store.SaveJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	e.store.SaveTask(ctx, core.Task{ID: "t1", JobID: "j1", Kind: "video_encode", Status: core.TaskPending})

	go e.worker.Run(e.ctx)
	waitStatus(t, e, "j1", core.JobCompleted)

	tk, _ := e.store.LoadTask(ctx, "t1")
	if tk.Status != core.TaskDone {
		t.Fatalf("task status = %s, want done", tk.Status)
	}
	if tk.LeaseExpiresAt == nil {
		t.Fatal("lease_expires_at was cleared when the task ran — a crashed worker can never recover it")
	}
}

// progressPlugin reports progress through the injected ProgressFunc.
type progressPlugin struct{ name, kind string }

func (p *progressPlugin) Name() string { return p.name }
func (p *progressPlugin) Capabilities() core.Capabilities {
	return core.Capabilities{Name: p.name, SupportedKinds: []string{p.kind}}
}
func (p *progressPlugin) Process(ctx context.Context, in core.TaskInput) (core.TaskOutput, error) {
	if in.Progress != nil {
		in.Progress(25)
		in.Progress(55)
		in.Progress(95)
	}
	return core.TaskOutput{}, nil
}

func TestWorkerEmitsTaskProgressEvents(t *testing.T) {
	store, err := sqlite.OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	bus := core.NewEventBus()
	sm := core.NewStateMachine(store, bus)
	sched := core.NewDAGScheduler(store, bus, sm)

	reg := registry.New()
	reg.Register(&progressPlugin{name: "p", kind: "ai_subtitle"})

	ctx, cancel := context.WithCancel(context.Background())
	w := New("w-prog", Options{
		Store: store, Bus: bus, Sched: sched, SM: sm, Registry: reg,
		Executor: fakeExec{}, LeaseTTL: time.Minute, Interval: 10 * time.Millisecond,
	})

	got := make(chan core.Event, 16)
	sub := bus.Subscribe(core.EvtTaskProgress)
	go func() {
		for ev := range sub {
			got <- ev
		}
	}()

	jctx := context.Background()
	job := core.Job{ID: "jp2", Status: core.JobQueued, Priority: core.PriorityNormal, Profile: "test"}
	if err := store.SaveJob(jctx, job); err != nil {
		t.Fatal(err)
	}
	store.SaveTask(jctx, core.Task{ID: "ta", JobID: "jp2", Kind: "ai_subtitle", Status: core.TaskPending})

	go w.Run(ctx)

	deadline := time.After(5 * time.Second)
	seen := map[float64]bool{}
	for len(seen) < 3 {
		select {
		case <-deadline:
			t.Fatalf("timeout; saw %v", seen)
		case ev := <-got:
			if ev.Kind != core.EvtTaskProgress {
				t.Fatalf("event kind = %v", ev.Kind)
			}
			var payload struct {
				Pct float64 `json:"progress_percent"`
			}
			_ = json.Unmarshal(ev.Payload, &payload)
			seen[payload.Pct] = true
		}
	}
	// wait for the job to finish so the worker stops cleanly
	waitStatus(t, &env{store: store, worker: w, ctx: ctx, cancel: cancel}, "jp2", core.JobCompleted)
	cancel()
	bus.Close()
	store.Close()

	for _, want := range []float64{25, 55, 95} {
		if !seen[want] {
			t.Errorf("missing progress event for %v (got %v)", want, seen)
		}
	}
}

func TestWorkerFailsJobOnPluginError(t *testing.T) {
	e := newWorkerEnv(t, &testPlugin{name: "v", kinds: []string{"video_encode"}, err: errors.New("encode failed")})
	ctx := context.Background()

	job := core.Job{ID: "jf", Status: core.JobQueued, Priority: core.PriorityNormal, Profile: "test"}
	e.store.SaveJob(ctx, job)
	e.store.SaveTask(ctx, core.Task{ID: "t1", JobID: "jf", Kind: "video_encode", Status: core.TaskPending})

	go e.worker.Run(e.ctx)
	j := waitStatus(t, e, "jf", core.JobFailed, core.JobCompleted)
	if j.Status != core.JobFailed {
		t.Fatalf("job status = %s, want failed", j.Status)
	}
	tk, _ := e.store.LoadTask(ctx, "t1")
	if tk.Status != core.TaskFailed {
		t.Fatalf("task status = %s, want failed", tk.Status)
	}
}

func TestWorkerParallelDAG(t *testing.T) {
	e := newWorkerEnv(t,
		&testPlugin{name: "video", kinds: []string{"video_encode"}},
		&testPlugin{name: "thumb", kinds: []string{"thumbnail"}},
	)
	ctx := context.Background()

	job := core.Job{ID: "jp", Status: core.JobQueued, Priority: core.PriorityNormal, Profile: "test"}
	e.store.SaveJob(ctx, job)
	e.store.SaveTask(ctx, core.Task{ID: "v", JobID: "jp", Kind: "video_encode", Status: core.TaskPending})
	e.store.SaveTask(ctx, core.Task{ID: "t", JobID: "jp", Kind: "thumbnail", Status: core.TaskPending, DependsOn: []core.TaskID{"v"}})

	go e.worker.Run(e.ctx)
	waitStatus(t, e, "jp", core.JobCompleted)

	tk, _ := e.store.LoadTask(ctx, "t")
	if tk.Status != core.TaskDone {
		t.Fatalf("dependent task status = %s", tk.Status)
	}
}

func TestWorkerSetsInputURIFromResolver(t *testing.T) {
	var gotURI string
	rec := &uriRecorder{name: "rec", kinds: []string{"video_encode"}}
	e := newWorkerEnv(t, rec)
	// reuse the same store/bus/sched but a new worker with the resolver
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	opts := e.worker.opts
	opts.InputResolver = func(job core.Job) (string, error) {
		gotURI = "/resolved/" + string(job.ID)
		return gotURI, nil
	}
	w := New("worker-resolver", opts)
	go w.Run(ctx)
	t.Cleanup(cancel)

	job := core.Job{ID: "juri", Status: core.JobQueued, Priority: core.PriorityNormal, Profile: "test"}
	if err := e.store.SaveJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	e.store.SaveTask(ctx, core.Task{ID: "turi", JobID: "juri", Kind: "video_encode", Status: core.TaskPending})

	waitStatus(t, e, "juri", core.JobCompleted)
	if gotURI != "/resolved/juri" {
		t.Fatalf("resolver not invoked with job id; got %q", gotURI)
	}
	if rec.seenURI != "/resolved/juri" {
		t.Fatalf("plugin received InputURI %q, want %q", rec.seenURI, gotURI)
	}
}

// uriRecorder records the InputURI a plugin was handed.
type uriRecorder struct {
	name    string
	kinds   []string
	seenURI string
}

func (p *uriRecorder) Name() string { return p.name }
func (p *uriRecorder) Capabilities() core.Capabilities {
	return core.Capabilities{Name: p.name, SupportedKinds: p.kinds}
}
func (p *uriRecorder) Process(_ context.Context, in core.TaskInput) (core.TaskOutput, error) {
	p.seenURI = in.InputURI
	return core.TaskOutput{}, nil
}

// TestWorkerGateBlocksScheduling verifies the resource gate keeps a ready task
// in the queue until the host is under the threshold again.
func TestWorkerGateBlocksScheduling(t *testing.T) {
	e := newWorkerEnv(t, &testPlugin{name: "v", kinds: []string{"video_encode"}})
	e.worker.opts.Gate = func() bool { return false } // host saturated
	ctx := context.Background()

	job := core.Job{ID: "jg", Status: core.JobQueued, Priority: core.PriorityNormal, Profile: "test"}
	if err := e.store.SaveJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	e.store.SaveTask(ctx, core.Task{ID: "tg", JobID: "jg", Kind: "video_encode", Status: core.TaskPending})

	go e.worker.Run(e.ctx)

	// give the worker a few cycles — the task must NOT be leased while gated
	time.Sleep(200 * time.Millisecond)
	tk, _ := e.store.LoadTask(ctx, "tg")
	if tk.Status != core.TaskPending {
		t.Fatalf("gated task was picked up: status = %s, want pending", tk.Status)
	}

	// open the gate; the task should now be processed
	e.worker.opts.Gate = func() bool { return true }
	waitStatus(t, e, "jg", core.JobCompleted)
}

// pauseRecorder is a fake executor that also implements core.ProcessController
// so the worker's supervisor can pause/resume it.
type pauseRecorder struct {
	mu      sync.Mutex
	pauses  int
	resumes int
}

func (f *pauseRecorder) Run(ctx context.Context, task core.Task, in core.TaskInput) (core.Result, error) {
	return core.Result{ExitCode: 0}, nil
}
func (f *pauseRecorder) Probe(ctx context.Context, path string) (core.MediaInfo, error) {
	return core.MediaInfo{}, nil
}
func (f *pauseRecorder) Pause(ctx context.Context, taskID core.TaskID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauses++
	return nil
}
func (f *pauseRecorder) Resume(ctx context.Context, taskID core.TaskID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes++
	return nil
}

func (f *pauseRecorder) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pauses, f.resumes
}

// TestWorkerPauseResumeSignals verifies that pausing a job while a task is
// running reaches the executor (real process stop) and resuming continues it.
func TestWorkerPauseResumeSignals(t *testing.T) {
	store, err := sqlite.OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	bus := core.NewEventBus()
	sm := core.NewStateMachine(store, bus)
	sched := core.NewDAGScheduler(store, bus, sm)
	reg := registry.New()
	if err := reg.Register(&testPlugin{name: "v", kinds: []string{"video_encode"}, delay: 300 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	rec := &pauseRecorder{}

	// The testPlugin honors ctx.Done, so a real pause would suspend ffmpeg; the
	// recorder just counts Pause/Resume calls. Give the job enough time to be
	// observed in both states while the plugin still runs.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := New("pause-1", Options{
		Store:    store,
		Bus:      bus,
		Sched:    sched,
		SM:       sm,
		Registry: reg,
		Executor: rec,
		LeaseTTL: time.Minute,
		Interval: 20 * time.Millisecond,
	})
	t.Cleanup(func() {
		cancel()
		bus.Close()
		store.Close()
	})

	cjob := core.Job{ID: "jp", Status: core.JobQueued, Priority: core.PriorityNormal, Profile: "test"}
	if err := store.SaveJob(ctx, cjob); err != nil {
		t.Fatal(err)
	}
	store.SaveTask(ctx, core.Task{ID: "tp", JobID: "jp", Kind: "video_encode", Status: core.TaskPending})

	go w.Run(ctx)
	waitStatus(t, &env{store: store, worker: w}, "jp", core.JobRunning)

	// pause while the plugin still runs → executor.Pause must be hit
	if err := sm.Transition(ctx, "jp", core.JobPaused, "test"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		p, _ := rec.counts()
		if p >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("executor.Pause was never called during job pause")
		case <-time.After(20 * time.Millisecond):
		}
	}

	// resume → executor.Resume must be hit
	if err := sm.Transition(ctx, "jp", core.JobResumed, "test"); err != nil {
		t.Fatal(err)
	}
	deadline = time.After(2 * time.Second)
	for {
		_, r := rec.counts()
		if r >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("executor.Resume was never called during job resume")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
