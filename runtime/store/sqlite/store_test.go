package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mohammadjaf013/weft/core"
)

func TestStoreRoundTrip(t *testing.T) {
	store, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	j := core.Job{ID: "j1", Status: core.JobQueued, Priority: core.PriorityNormal, Profile: "vod", InputRef: "s3://in/movie.mp4"}
	if err := store.SaveJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadJob(ctx, "j1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != core.JobQueued || got.Profile != "vod" || got.InputRef != "s3://in/movie.mp4" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	// update
	j.Status = core.JobRunning
	store.SaveJob(ctx, j)
	got, _ = store.LoadJob(ctx, "j1")
	if got.Status != core.JobRunning {
		t.Fatalf("expected running, got %s", got.Status)
	}
}

func TestStoreJobEventAtomic(t *testing.T) {
	store, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	j := core.Job{ID: "j2", Status: core.JobQueued, Priority: core.PriorityNormal}
	e := core.Event{ID: "e1", JobID: "j2", Kind: core.EvtJobCreated, CreatedAt: core.Now()}
	if err := store.SaveJobEvent(ctx, j, e); err != nil {
		t.Fatal(err)
	}
	evs, err := store.ListEvents(ctx, "j2")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != core.EvtJobCreated {
		t.Fatalf("events = %v", evs)
	}
}

func TestStoreTaskDeps(t *testing.T) {
	store, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	ts := []core.Task{
		{ID: "t1", JobID: "j", Kind: "video", Status: core.TaskDone, DependsOn: []core.TaskID{"a", "b"}},
	}
	if err := store.SaveTask(ctx, ts[0]); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != core.TaskDone || len(got.DependsOn) != 2 {
		t.Fatalf("task round trip: %+v", got)
	}
	list, _ := store.ListTasks(ctx, "j")
	if len(list) != 1 {
		t.Fatalf("list = %v", list)
	}
}

func TestStoreOutboxLifecycle(t *testing.T) {
	store, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	ob := store.Outbox()

	// 1. enqueue -> pending
	if err := ob.Enqueue(ctx, "evt1", "wh1", core.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err := ob.ListPending(ctx, 10, core.Now().Add(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "pending" {
		t.Fatalf("pending rows = %v", rows)
	}
	// 2. mark delivered
	if err := ob.Mark(ctx, rows[0].ID, true, ""); err != nil {
		t.Fatal(err)
	}
	rows, _ = ob.ListPending(ctx, 10, core.Now().Add(1))
	if len(rows) != 0 {
		t.Fatalf("expected no pending after delivery, got %v", rows)
	}
	// 3. dead letter
	ob.Enqueue(ctx, "evt2", "wh1", core.Now())
	rows, _ = ob.ListPending(ctx, 10, core.Now().Add(1))
	if err := ob.(interface {
		DeadLetter(context.Context, string) error
	}).DeadLetter(ctx, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	rows, _ = ob.ListPending(ctx, 10, core.Now().Add(1))
	if len(rows) != 0 {
		t.Fatal("dead-lettered row should not be pending")
	}
}

func TestStoreListJobsFilter(t *testing.T) {
	store, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, j := range []core.Job{
		{ID: "a", Status: core.JobRunning, Priority: core.PriorityHigh},
		{ID: "b", Status: core.JobQueued, Priority: core.PriorityLow},
		{ID: "c", Status: core.JobRunning, Priority: core.PriorityLow},
	} {
		store.SaveJob(ctx, j)
	}
	jobs, _ := store.ListJobs(ctx, core.JobFilter{Status: core.JobRunning})
	if len(jobs) != 2 {
		t.Fatalf("running jobs = %d, want 2", len(jobs))
	}
	jobs, _ = store.ListJobs(ctx, core.JobFilter{Priority: core.PriorityLow})
	if len(jobs) != 2 {
		t.Fatalf("low jobs = %d, want 2", len(jobs))
	}
}

// TestUpdateTaskProgressPreservesReservation verifies that the worker's
// progress callback only touches progress/status and never clobbers the lease,
// started_at, worker_id or error fields (previously a full SaveTask with an
// empty Task did, breaking crash recovery mid-run).
func TestUpdateTaskProgressPreservesReservation(t *testing.T) {
	store, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	now := core.Now()
	tk := core.Task{
		ID: "t1", JobID: "j1", Kind: "hls", Status: core.TaskRunning,
		WorkerID: "w0", Progress: 5,
		LeaseExpiresAt: &now, StartedAt: &now,
	}
	if err := store.SaveTask(ctx, tk); err != nil {
		t.Fatal(err)
	}

	if err := store.UpdateTaskProgress(ctx, "t1", core.TaskRunning, 42.5); err != nil {
		t.Fatal(err)
	}

	got, err := store.LoadTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress != 42.5 {
		t.Errorf("progress = %v, want 42.5", got.Progress)
	}
	if got.LeaseExpiresAt == nil || !got.LeaseExpiresAt.Equal(now) {
		t.Error("UpdateTaskProgress clobbered lease_expires_at")
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(now) {
		t.Error("UpdateTaskProgress clobbered started_at")
	}
	if got.WorkerID != "w0" {
		t.Errorf("worker_id = %q, want w0", got.WorkerID)
	}
}

func TestStoreLoadMissingJob(t *testing.T) {	store, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.LoadJob(context.Background(), "nope")
	if !errors.Is(err, core.ErrJobNotFound) {
		t.Fatalf("want ErrJobNotFound, got %v", err)
	}
}

func TestStoreWebhookAndKeys(t *testing.T) {
	store, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	wh := Webhook{ID: "wh1", URL: "https://x/h", Secret: "s3cret", Events: []string{"job.started", "job.completed"}, MaxRetries: 5}
	if err := store.SaveWebhook(ctx, wh); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadWebhook(ctx, "wh1")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != wh.URL || len(got.Events) != 2 || got.MaxRetries != 5 {
		t.Fatalf("webhook round trip: %+v", got)
	}
	list, _ := store.ListWebhooks(ctx)
	if len(list) != 1 {
		t.Fatalf("webhooks = %v", list)
	}

	store.SaveAPIKey(ctx, APIKey{ID: "k1", Name: "admin", KeyHash: "argon2...", Scopes: []string{"jobs:write"}})
	keys, _ := store.ListAPIKeys(ctx)
	if len(keys) != 1 || keys[0].Name != "admin" {
		t.Fatalf("keys = %v", keys)
	}

	store.SaveStorageServer(ctx, StorageServer{ID: 2, Type: "ssh", Host: "5.6.7.8", User: "root", Config: map[string]any{"path": "/srv"}})
	servers, _ := store.ListStorageServers(ctx)
	if len(servers) != 1 || servers[0].Config["path"] != "/srv" {
		t.Fatalf("servers = %v", servers)
	}
}

func TestStoreTryLease(t *testing.T) {
	store, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	task := core.Task{ID: "t1", JobID: "j1", Kind: "video_encode", Status: core.TaskReady}
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	exp := core.Now().Add(time.Minute)
	claimed, err := store.TryLease(ctx, task, "w1", exp, core.Now())
	if err != nil || !claimed {
		t.Fatalf("first lease = %v, %v; want true", claimed, err)
	}
	// second worker must not claim the same task
	claimed, _ = store.TryLease(ctx, task, "w2", exp, core.Now())
	if claimed {
		t.Fatalf("second lease succeeded; want false")
	}
	got, _ := store.LoadTask(ctx, "t1")
	if got.Status != core.TaskLeased || got.WorkerID != "w1" {
		t.Fatalf("task after lease = %+v", got)
	}
}

func TestStoreDeleteJobCascade(t *testing.T) {
	store, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	j := core.Job{ID: "jdel", Status: core.JobCompleted, Priority: core.PriorityNormal, Profile: "vod"}
	if err := store.SaveJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	task := core.Task{ID: "tdel", JobID: "jdel", Kind: "video_encode", Status: core.TaskDone}
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTaskOutputs(ctx, "tdel", "jdel", []core.AssetRef{{Kind: "video", Name: "v.mp4", URI: "/x/v.mp4", Bytes: 10}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTaskLog(ctx, "tdel", "jdel", "ffmpeg ok"); err != nil {
		t.Fatal(err)
	}
	ev := core.Event{ID: "edel", JobID: "jdel", Kind: core.EvtJobCreated, CreatedAt: core.Now()}
	if err := store.AppendEvent(ctx, ev); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteJob(ctx, "jdel"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadJob(ctx, "jdel"); !errors.Is(err, core.ErrJobNotFound) {
		t.Fatalf("job still present: %v", err)
	}
	if _, err := store.LoadTask(ctx, "tdel"); err == nil {
		t.Fatal("task still present")
	}
	if evs, _ := store.ListEvents(ctx, "jdel"); len(evs) != 0 {
		t.Fatalf("events = %v", evs)
	}
	if outs, _ := store.ListJobOutputs(ctx, "jdel"); len(outs) != 0 {
		t.Fatalf("outputs = %v", outs)
	}
	if log, _ := store.LoadTaskLog(ctx, "tdel"); log != "" {
		t.Fatalf("log = %q", log)
	}
}

func TestStoreTaskLogRoundTrip(t *testing.T) {
	store, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SaveTaskLog(ctx, "tlog", "j1", "whisper progress..."); err != nil {
		t.Fatal(err)
	}
	log, err := store.LoadTaskLog(ctx, "tlog")
	if err != nil {
		t.Fatal(err)
	}
	if log != "whisper progress..." {
		t.Fatalf("log = %q", log)
	}
	// overwrite
	if err := store.SaveTaskLog(ctx, "tlog", "j1", "new"); err != nil {
		t.Fatal(err)
	}
	log, _ = store.LoadTaskLog(ctx, "tlog")
	if log != "new" {
		t.Fatalf("log after overwrite = %q", log)
	}
	// missing
	if log, _ := store.LoadTaskLog(ctx, "nope"); log != "" {
		t.Fatalf("missing log = %q", log)
	}
}

func TestStorePruneEvents(t *testing.T) {
	store, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	old := core.Event{ID: "eold", JobID: "j1", Kind: core.EvtJobStarted, CreatedAt: core.Now().Add(-48 * time.Hour)}
	newer := core.Event{ID: "enew", JobID: "j2", Kind: core.EvtJobStarted, CreatedAt: core.Now()}
	if err := store.AppendEvent(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(ctx, newer); err != nil {
		t.Fatal(err)
	}
	n, err := store.PruneEvents(ctx, core.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	evs, _ := store.ListEvents(ctx, "")
	if len(evs) != 1 || evs[0].ID != "enew" {
		t.Fatalf("remaining events = %v", evs)
	}
}
