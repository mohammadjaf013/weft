// Package webhook delivers outbox rows to configured webhooks. It is itself a
// plain Event Bus subscriber (via the outbox) — Core never knows it exists.
// Delivery is at-least-once, HMAC-SHA256 signed, with exponential backoff and
// a replayable Dead Letter log.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/runtime/store/sqlite"
)

// CoreEvent → wire event mapping. Keep this mapping in ONE place — the
// dispatcher. Don't let either naming style leak into the other layer.
var wireNames = map[core.EventKind]string{
	core.EvtJobCreated:       "job.created",
	core.EvtJobStarted:       "job.started",
	core.EvtJobProgress:      "job.progress",
	core.EvtJobPaused:        "job.paused",
	core.EvtJobResumed:       "job.resumed",
	core.EvtJobCancelled:     "job.cancelled",
	core.EvtJobFinished:      "job.completed",
	core.EvtJobFailed:        "job.failed",
	core.EvtTaskProgress:     "task.progress",
	core.EvtStorageUploaded:  "storage.uploaded",
	core.EvtStorageFailed:    "storage.failed",
	core.EvtPipelineStarted:  "pipeline.started",
	core.EvtPipelineFinished: "pipeline.finished",
	core.EvtPluginStarted:    "plugin.started",
	core.EvtPluginFinished:   "plugin.finished",
	core.EvtNodeJoined:       "node.joined",
	core.EvtNodeLeft:         "node.left",
}

func WireName(k core.EventKind) string {
	if n, ok := wireNames[k]; ok {
		return n
	}
	return string(k)
}

// Dispatcher is the outbox reader + deliverer.
type Dispatcher struct {
	store    *sqlite.Store
	client   *http.Client
	interval time.Duration
	now      func() time.Time

	// fakeHTTP is used in tests instead of a real HTTP client.
	fakeHTTP func(ctx context.Context, url, body string, headers map[string]string) (int, error)

	// hook for tests to observe deliveries
	OnDeliver func(wire string, eventID string)
}

type Options struct {
	Store      *sqlite.Store
	Interval   time.Duration
	HTTPClient *http.Client
}

func NewDispatcher(o Options) *Dispatcher {
	if o.Interval == 0 {
		o.Interval = 500 * time.Millisecond
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Dispatcher{
		store:    o.Store,
		client:   o.HTTPClient,
		interval: o.Interval,
		now:      time.Now,
	}
}

// Run polls the outbox forever until ctx is done.
func (d *Dispatcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := d.Once(ctx); err != nil {
				return err
			}
		}
	}
}

// Once drains currently-pending outbox rows (one pass). Used by tests and by
// Run on each tick.
func (d *Dispatcher) Once(ctx context.Context) error {
	rows, err := d.store.Outbox().ListPending(ctx, 100, d.now())
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := d.deliver(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) deliver(ctx context.Context, row core.OutboxRow) error {
	ev, err := d.loadEvent(ctx, row.EventID)
	if err != nil {
		return err
	}
	wh, err := d.store.LoadWebhook(ctx, row.WebhookID)
	if err != nil {
		return err
	}

	payload, err := d.buildPayload(ev, wh)
	if err != nil {
		return err
	}

	status, err := d.post(ctx, wh, payload)
	if err == nil && status >= 200 && status < 300 {
		if err := d.store.Outbox().Mark(ctx, row.ID, true, ""); err != nil {
			return err
		}
		if d.OnDeliver != nil {
			d.OnDeliver(WireName(ev.Kind), ev.ID)
		}
		return nil
	}
	// failure path: retry with exponential backoff or dead-letter
	attempts := row.Attempts + 1
	if attempts > wh.MaxRetries {
		if dl, ok := d.store.Outbox().(interface {
			DeadLetter(context.Context, string) error
		}); ok {
			return dl.DeadLetter(ctx, row.ID)
		}
		return d.store.Outbox().Mark(ctx, row.ID, false, "exhausted")
	}
	backoff := time.Duration(1<<uint(attempts)) * time.Second // 2^n
	// cap backoff at 5 minutes
	if backoff > 5*time.Minute {
		backoff = 5 * time.Minute
	}
	if na, ok := d.store.Outbox().(interface {
		NextAttempt(context.Context, string, time.Time) error
	}); ok {
		return na.NextAttempt(ctx, row.ID, d.now().Add(backoff))
	}
	return d.store.Outbox().Mark(ctx, row.ID, false, "")
}

func (d *Dispatcher) loadEvent(ctx context.Context, eventID string) (core.Event, error) {
	events, err := d.store.ListEvents(ctx, "")
	if err != nil {
		return core.Event{}, err
	}
	for _, e := range events {
		if e.ID == eventID {
			return e, nil
		}
	}
	return core.Event{}, fmt.Errorf("event %s not found", eventID)
}

type wireEvent struct {
	EventID   string          `json:"event_id"`
	EventType string          `json:"event_type"`
	JobID     string          `json:"job_id"`
	TaskID    string          `json:"task_id,omitempty"`
	DestPath  string          `json:"dest_path,omitempty"`
	Outputs   []wireOutput    `json:"outputs,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp int64           `json:"timestamp"`
}

type wireOutput struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	URI  string `json:"uri"`
}

func (d *Dispatcher) buildPayload(ev core.Event, wh sqlite.Webhook) ([]byte, error) {
	we := wireEvent{
		EventID:   ev.ID,
		EventType: WireName(ev.Kind),
		JobID:     string(ev.JobID),
		TaskID:    string(ev.TaskID),
		Payload:   ev.Payload,
		Timestamp: ev.CreatedAt.Unix(),
	}
	// Attach the job's destination folder and final asset list whenever we can.
	if ev.JobID != "" {
		ctx := context.Background()
		if j, err := d.store.LoadJob(ctx, ev.JobID); err == nil {
			we.DestPath = j.DestPath
		}
		if outs, err := d.store.ListJobOutputs(ctx, ev.JobID); err == nil {
			we.Outputs = make([]wireOutput, 0, len(outs))
			for _, o := range outs {
				we.Outputs = append(we.Outputs, wireOutput{Kind: o.Kind, Name: o.Name, URI: o.URI})
			}
		}
	}
	if we.Payload == nil {
		we.Payload = json.RawMessage(`{}`)
	}
	return json.Marshal(we)
}

func (d *Dispatcher) post(ctx context.Context, wh sqlite.Webhook, payload []byte) (int, error) {
	if d.fakeHTTP != nil {
		return d.fakeHTTP(ctx, wh.URL, string(payload), map[string]string{"X-Weft-Signature": sign(wh.Secret, payload)})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Weft-Signature", sign(wh.Secret, payload))
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// Sign computes the HMAC-SHA256 of the payload with the webhook secret,
// hex-encoded, matching what receivers verify via X-Weft-Signature.
func sign(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify checks an X-Weft-Signature header against a payload (exported so
// external receivers — and the e2e Admin stub — can validate deliveries).
func Verify(secret string, payload []byte, signature string) bool {
	want := sign(secret, payload)
	return hmac.Equal([]byte(want), []byte(signature))
}
