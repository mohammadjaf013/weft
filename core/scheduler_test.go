package core

import (
	"context"
	"testing"
)

func newScheduler(t *testing.T) (DAGScheduler, *memStore, StateMachine) {
	t.Helper()
	store := NewMemStore()
	bus := NewEventBus()
	t.Cleanup(bus.Close)
	sm := NewStateMachine(store, bus)
	return NewDAGScheduler(store, bus, sm), store, sm
}

// driveJob walks the job through the legal lifecycle up to the given status,
// the way the Runtime worker loop would.
func driveJob(t *testing.T, sm StateMachine, id JobID, to JobStatus) {
	t.Helper()
	ctx := context.Background()
	chain := []JobStatus{JobReserved, JobRunning, JobUploading}
	for _, s := range chain {
		if err := sm.Transition(ctx, id, s, ""); err != nil {
			t.Fatalf("drive to %s failed at %s: %v", to, s, err)
		}
		if s == to {
			return
		}
	}
}

func TestDAGLinearPipeline(t *testing.T) {
	sched, store, sm := newScheduler(t)
	ctx := context.Background()
	job := Job{ID: "j1", Priority: PriorityNormal, Profile: "vod"}
	tasks := []Task{
		{ID: "t1", JobID: "j1", Kind: "video_encode"},
		{ID: "t2", JobID: "j1", Kind: "thumbnail", DependsOn: []TaskID{"t1"}},
		{ID: "t3", JobID: "j1", Kind: "upload", DependsOn: []TaskID{"t2"}},
	}
	if err := sched.Submit(ctx, job, tasks); err != nil {
		t.Fatal(err)
	}
	driveJob(t, sm, "j1", JobRunning)

	// First ready task must be t1 (no deps)
	tk, err := sched.NextReady(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tk == nil || tk.ID != "t1" {
		t.Fatalf("first ready = %v, want t1", tk)
	}
	// Ready tasks stay ready on re-poll (crash between ready and reserve recovers)
	tk, _ = sched.NextReady(ctx)
	if tk == nil || tk.ID != "t1" {
		t.Fatalf("re-poll ready = %v, want t1 again", tk)
	}
	// t2 must NOT be ready while t1 not done
	if err := sched.MarkDone(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	tk, _ = sched.NextReady(ctx)
	if tk == nil || tk.ID != "t2" {
		t.Fatalf("after t1 done ready = %v, want t2", tk)
	}
	if err := sched.MarkDone(ctx, "t2"); err != nil {
		t.Fatal(err)
	}
	// uploading phase before the upload task finishes
	sm.Transition(ctx, "j1", JobUploading, "")
	tk, _ = sched.NextReady(ctx)
	if tk == nil || tk.ID != "t3" {
		t.Fatalf("after t2 done ready = %v, want t3", tk)
	}
	if err := sched.MarkDone(ctx, "t3"); err != nil {
		t.Fatal(err)
	}
	// all done → job completed
	j, _ := store.LoadJob(ctx, "j1")
	if j.Status != JobCompleted {
		t.Fatalf("job status %s, want completed", j.Status)
	}
}

func TestDAGParallelTasks(t *testing.T) {
	sched, store, sm := newScheduler(t)
	ctx := context.Background()
	job := Job{ID: "j2", Priority: PriorityNormal, Profile: "vod"}
	tasks := []Task{
		{ID: "video", JobID: "j2", Kind: "video_encode"},
		{ID: "thumb", JobID: "j2", Kind: "thumbnail", DependsOn: []TaskID{"video"}},
		{ID: "sub", JobID: "j2", Kind: "subtitle", DependsOn: []TaskID{"video"}},
		{ID: "master", JobID: "j2", Kind: "master_playlist", DependsOn: []TaskID{"thumb", "sub"}},
		{ID: "upload", JobID: "j2", Kind: "upload", DependsOn: []TaskID{"master"}},
	}
	if err := sched.Submit(ctx, job, tasks); err != nil {
		t.Fatal(err)
	}
	driveJob(t, sm, "j2", JobRunning)

	tk, _ := sched.NextReady(ctx)
	if tk == nil || tk.ID != "video" {
		t.Fatalf("first ready %v, want video", tk)
	}
	sched.MarkDone(ctx, "video")

	// thumb and sub must BOTH be ready: parallelism, not sequential
	first, _ := sched.NextReady(ctx)
	if first == nil || (first.ID != "thumb" && first.ID != "sub") {
		t.Fatalf("expected thumb or sub, got %v", first)
	}
	sched.MarkDone(ctx, "thumb")
	second, _ := sched.NextReady(ctx)
	if second == nil || second.ID != "sub" {
		t.Fatalf("expected sub next, got %v", second)
	}
	// master still blocked until sub done
	blocked, _ := sched.NextReady(ctx)
	if blocked == nil || blocked.ID != "sub" {
		t.Fatalf("expected sub (still ready), got %v", blocked)
	}
	sched.MarkDone(ctx, "sub")
	master, _ := sched.NextReady(ctx)
	if master == nil || master.ID != "master" {
		t.Fatalf("expected master, got %v", master)
	}
	sched.MarkDone(ctx, "master")
	sm.Transition(ctx, "j2", JobUploading, "")
	up, _ := sched.NextReady(ctx)
	if up == nil || up.ID != "upload" {
		t.Fatalf("expected upload, got %v", up)
	}
	sched.MarkDone(ctx, "upload")
	j, _ := store.LoadJob(ctx, "j2")
	if j.Status != JobCompleted {
		t.Fatalf("status %s, want completed", j.Status)
	}
}

func TestDAGPriorityOrdering(t *testing.T) {
	sched, _, sm := newScheduler(t)
	ctx := context.Background()
	// low-priority job submitted first, emergency second — emergency must run first
	low := Job{ID: "low", Priority: PriorityLow, Profile: "p"}
	emerg := Job{ID: "emerg", Priority: PriorityEmergency, Profile: "p"}
	sched.Submit(ctx, low, []Task{{ID: "lt", JobID: "low", Kind: "video"}})
	sched.Submit(ctx, emerg, []Task{{ID: "et", JobID: "emerg", Kind: "video"}})
	driveJob(t, sm, "low", JobRunning)
	driveJob(t, sm, "emerg", JobRunning)

	tk, err := sched.NextReady(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tk == nil || tk.ID != "et" {
		t.Fatalf("expected emergency task et, got %v", tk)
	}
}

func TestDAGFailedTaskFailsJob(t *testing.T) {
	sched, store, sm := newScheduler(t)
	ctx := context.Background()
	job := Job{ID: "j3", Priority: PriorityNormal, Profile: "p"}
	sched.Submit(ctx, job, []Task{
		{ID: "t1", JobID: "j3", Kind: "video"},
		{ID: "t2", JobID: "j3", Kind: "upload", DependsOn: []TaskID{"t1"}},
	})
	driveJob(t, sm, "j3", JobRunning)
	if err := sched.MarkFailed(ctx, "t1", errTest); err != nil {
		t.Fatal(err)
	}
	j, _ := store.LoadJob(ctx, "j3")
	if j.Status != JobFailed {
		t.Fatalf("status %s, want failed", j.Status)
	}
	tk, _ := sched.NextReady(ctx)
	if tk != nil {
		t.Fatalf("no task should be ready after failure, got %s", tk.ID)
	}
}

var errTest = &testErr{msg: "boom"}

type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }

func TestDAGRetryResetsFailedTasksAndRequeues(t *testing.T) {
	sched, store, sm := newScheduler(t)
	ctx := context.Background()
	job := Job{ID: "j4", Priority: PriorityNormal, Profile: "p"}
	sched.Submit(ctx, job, []Task{
		{ID: "t1", JobID: "j4", Kind: "video"},
		{ID: "t2", JobID: "j4", Kind: "upload", DependsOn: []TaskID{"t1"}},
	})
	driveJob(t, sm, "j4", JobRunning)
	if err := sched.MarkFailed(ctx, "t1", errTest); err != nil {
		t.Fatal(err)
	}
	j, _ := store.LoadJob(ctx, "j4")
	if j.Status != JobFailed {
		t.Fatalf("status %s, want failed", j.Status)
	}
	// operator requests retry: job → retry
	if err := sm.Transition(ctx, "j4", JobRetry, "user action"); err != nil {
		t.Fatal(err)
	}
	// next scheduling pass must requeue the job and reset t1
	tk, err := sched.NextReady(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tk == nil || tk.ID != "t1" {
		t.Fatalf("after retry ready = %v, want t1", tk)
	}
	j, _ = store.LoadJob(ctx, "j4")
	if j.Status != JobQueued {
		t.Fatalf("job status %s, want queued after retry", j.Status)
	}
	t1, _ := store.LoadTask(ctx, "t1")
	if t1.Status != TaskReady && t1.Status != TaskPending {
		t.Fatalf("task t1 status %s, want reset to pending/ready", t1.Status)
	}
}
