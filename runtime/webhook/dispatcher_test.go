package webhook

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/runtime/store/sqlite"
)

// fakeHTTP returns 200 always unless told otherwise; records deliveries.
type fakeHTTP struct {
	mu        sync.Mutex
	statuses  map[string]int // url -> status to return
	delivered []string
	received  []receivedMsg
	attempts  map[string]int
	onPost    func()
}

type receivedMsg struct {
	url     string
	payload string
	sig     string
}

func (f *fakeHTTP) handler() func(ctx context.Context, url, body string, headers map[string]string) (int, error) {
	return func(ctx context.Context, url, body string, headers map[string]string) (int, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.attempts[url]++
		if st, ok := f.statuses[url]; ok && st != 200 {
			f.received = append(f.received, receivedMsg{url, body, headers["X-Weft-Signature"]})
			return st, nil
		}
		f.delivered = append(f.delivered, body)
		f.received = append(f.received, receivedMsg{url, body, headers["X-Weft-Signature"]})
		if f.onPost != nil {
			f.onPost()
		}
		return 200, nil
	}
}

func newTestEnv(t *testing.T) (*sqlite.Store, *Dispatcher, *fakeHTTP) {
	t.Helper()
	store, err := sqlite.OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	fh := &fakeHTTP{attempts: map[string]int{}}
	d := NewDispatcher(Options{Store: store})
	d.fakeHTTP = fh.handler()
	return store, d, fh
}

func enqueueJobFinished(t *testing.T, store *sqlite.Store, evID, whID string) {
	t.Helper()
	ctx := context.Background()
	// persist the event row so the dispatcher can load it
	store.AppendEvent(ctx, core.Event{ID: evID, JobID: "j1", Kind: core.EvtJobFinished, Payload: []byte(`{"status":"completed"}`), CreatedAt: core.Now()})
	if err := store.Outbox().Enqueue(ctx, evID, whID, core.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestDeliverHappyPath(t *testing.T) {
	store, d, fh := newTestEnv(t)
	ctx := context.Background()

	store.SaveWebhook(ctx, sqlite.Webhook{ID: "wh1", URL: "https://admin/h", Secret: "s3cret", Events: []string{"job.completed"}, MaxRetries: 3})
	enqueueJobFinished(t, store, "evt1", "wh1")

	if err := d.Once(ctx); err != nil {
		t.Fatal(err)
	}
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if len(fh.delivered) != 1 {
		t.Fatalf("delivered = %d, want 1", len(fh.delivered))
	}
	// outbox row must now be delivered (no longer pending)
	rows, _ := store.Outbox().ListPending(ctx, 10, time.Now().Add(time.Second))
	if len(rows) != 0 {
		t.Fatalf("expected row delivered, still pending: %v", rows)
	}
}

func TestDeliverSignatureValid(t *testing.T) {
	store, d, fh := newTestEnv(t)
	ctx := context.Background()
	secret := "s3cret"
	store.SaveWebhook(ctx, sqlite.Webhook{ID: "wh1", URL: "https://admin/h", Secret: secret, Events: []string{"job.completed"}, MaxRetries: 3})
	enqueueJobFinished(t, store, "evt1", "wh1")
	if err := d.Once(ctx); err != nil {
		t.Fatal(err)
	}
	fh.mu.Lock()
	defer fh.mu.Unlock()
	msg := fh.received[0]
	if !Verify(secret, []byte(msg.payload), msg.sig) {
		t.Fatal("signature verification failed on delivered payload")
	}
}

func TestPayloadCarriesDestPathAndOutputs(t *testing.T) {
	store, d, fh := newTestEnv(t)
	ctx := context.Background()
	store.SaveWebhook(ctx, sqlite.Webhook{ID: "wh1", URL: "https://admin/h", Secret: "s", Events: []string{"job.completed"}, MaxRetries: 3})
	store.SaveJob(ctx, core.Job{ID: "j1", Status: core.JobQueued, DestPath: "Series-Test/movie1"})
	store.SaveTaskOutputs(ctx, "task_u1", "j1", []core.AssetRef{{Kind: "playlist", Name: "playlist.m3u8", URI: "local://playlist.m3u8"}})
	enqueueJobFinished(t, store, "evt1", "wh1")
	if err := d.Once(ctx); err != nil {
		t.Fatal(err)
	}
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if len(fh.received) != 1 {
		t.Fatalf("delivered = %d, want 1", len(fh.received))
	}
	var we wireEvent
	if err := json.Unmarshal([]byte(fh.received[0].payload), &we); err != nil {
		t.Fatal(err)
	}
	if we.DestPath != "Series-Test/movie1" {
		t.Errorf("dest_path = %q, want Series-Test/movie1", we.DestPath)
	}
	if len(we.Outputs) != 1 || we.Outputs[0].Name != "playlist.m3u8" {
		t.Errorf("outputs = %+v, want one playlist.m3u8", we.Outputs)
	}
}

func TestBackoffAndDeadLetter(t *testing.T) {
	store, d, fh := newTestEnv(t)
	ctx := context.Background()
	fh.statuses = map[string]int{"https://bad/h": 500}
	// URL always returns 500 → exhausts max_retries=2 → dead_letter
	store.SaveWebhook(ctx, sqlite.Webhook{ID: "wh1", URL: "https://bad/h", Secret: "s", Events: []string{"job.completed"}, MaxRetries: 2})
	enqueueJobFinished(t, store, "evt1", "wh1")

	// First pass: attempt 1 → backoff (2s)
	if err := d.Once(ctx); err != nil {
		t.Fatal(err)
	}
	// attempt 2 is scheduled 2s out, so a second immediate pass sees nothing
	if err := d.Once(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _ := store.Outbox().ListPending(ctx, 10, time.Now().Add(3*time.Second))
	if len(rows) != 1 {
		t.Fatalf("expected 1 row pending for retry, got %v", rows)
	}
	fh.mu.Lock()
	first := fh.attempts["https://bad/h"]
	fh.mu.Unlock()
	if first != 1 {
		t.Fatalf("attempts = %d, want 1", first)
	}
}

func TestDeadLetterAfterRetries(t *testing.T) {
	store, d, fh := newTestEnv(t)
	ctx := context.Background()
	fh.statuses = map[string]int{"https://bad/h": 500}
	store.SaveWebhook(ctx, sqlite.Webhook{ID: "wh1", URL: "https://bad/h", Secret: "s", Events: []string{"job.completed"}, MaxRetries: 1})
	enqueueJobFinished(t, store, "evt1", "wh1")

	// attempts start at 0. Pass 1 → attempt 1 > maxRetries(1)? no (1>1 false) → backoff.
	if err := d.Once(ctx); err != nil {
		t.Fatal(err)
	}
	// fast-forward: set next_attempt_at to now so pass 2 delivers attempt 2 → dead letter
	rows, _ := store.Outbox().ListPending(ctx, 10, time.Now().Add(3*time.Second))
	if len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	// force due immediately
	ob := store.Outbox().(interface {
		NextAttempt(context.Context, string, time.Time) error
	})
	if err := ob.NextAttempt(ctx, rows[0].ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := d.Once(ctx); err != nil {
		t.Fatal(err)
	}
	// dead-lettered rows are not pending
	rows, _ = store.Outbox().ListPending(ctx, 10, time.Now().Add(time.Second))
	if len(rows) != 0 {
		t.Fatalf("expected dead letter, still pending: %v", rows)
	}
}

func TestWireNameMapping(t *testing.T) {
	cases := map[core.EventKind]string{
		core.EvtJobCreated:  "job.created",
		core.EvtJobStarted:  "job.started",
		core.EvtJobFinished: "job.completed",
		core.EvtJobFailed:   "job.failed",
		core.EvtJobProgress: "job.progress",
		core.EvtTaskProgress: "task.progress",
	}
	for k, want := range cases {
		if got := WireName(k); got != want {
			t.Errorf("WireName(%s) = %s, want %s", k, got, want)
		}
	}
}

func TestVerifyRejectsTampered(t *testing.T) {
	if Verify("secret", []byte("payload"), "deadbeef") {
		t.Fatal("tampered signature verified")
	}
	if !Verify("secret", []byte("payload"), sign("secret", []byte("payload"))) {
		t.Fatal("valid signature rejected")
	}
}
