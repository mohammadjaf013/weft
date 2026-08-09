package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/mediautil"
	"github.com/mohammadjaf013/weft/plugins/storage/local"
	"github.com/mohammadjaf013/weft/runtime/store/sqlite"
	"github.com/mohammadjaf013/weft/runtime/sysinfo"
	"github.com/mohammadjaf013/weft/runtime/webhook"
	"github.com/mohammadjaf013/weft/runtime/worker"
)

// Serve starts workers, the webhook dispatcher + enqueuer, and the HTTP API.
// It blocks until ctx is cancelled or the server fails; returns after shutdown.
func (d *Daemon) Serve(ctx context.Context) error {
	ctx, d.cancel = context.WithCancel(ctx)
	defer d.cancel()

	// seed webhooks from config into the store
	if err := d.seedWebhooks(ctx); err != nil {
		return err
	}

	// outbox enqueuer: bus event -> outbox rows for matching webhooks
	enq := &outboxEnqueuer{store: d.store, bus: d.bus, interval: 300 * time.Millisecond}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		if err := enq.Run(ctx); err != nil {
			log.Printf("outbox enqueuer stopped: %v", err)
		}
	}()

	// webhook dispatcher: poll outbox, deliver with retry/backoff
	d.wh = webhook.NewDispatcher(webhook.Options{Store: d.store, Interval: 500 * time.Millisecond})
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		if err := d.wh.Run(ctx); err != nil {
			log.Printf("webhook dispatcher stopped: %v", err)
		}
	}()

	// cleaner: on job completion, optionally delete the source file and always
	// remove the per-task temp work dirs.
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		if err := (&cleaner{store: d.store, bus: d.bus, workRoot: mediautil.WorkRoot}).Run(ctx); err != nil {
			log.Printf("cleaner stopped: %v", err)
		}
	}()

	// lease expirer: requeue tasks whose worker died mid-task (crash recovery)
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		expirer(ctx, d.store, 2*time.Second)
	}()

	// event retention: periodically delete events older than the configured
	// retention window so the event log (and webhook outbox) never grows
	// without bound. cron.cleanup.event_retention_days; 0 disables.
	if days := d.cfg.Cron.Cleanup.EventRetentionDays; days > 0 {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			retentionPruner(ctx, d.store, days, time.Hour)
		}()
	}

	// workers. A resource gate idles workers when the host exceeds the
	// scheduler thresholds, so the queue stays put instead of saturating CPU.
	// workers.max caps how many tasks run at once (spawn that many workers);
	// when unset, workers.min is the steady-state pool size.
	min := d.cfg.Workers.Min
	if min <= 0 {
		min = 1
	}
	count := min
	if d.cfg.Workers.Max > 0 && d.cfg.Workers.Max >= min {
		count = d.cfg.Workers.Max
	}
	leaseTTL := 5 * time.Minute
	if d.cfg.Workers.LeaseTTLSeconds > 0 {
		leaseTTL = time.Duration(d.cfg.Workers.LeaseTTLSeconds) * time.Second
	}
	var gate *sysinfo.Gate
	if d.cfg.Scheduler.MaxCPUPercent > 0 || d.cfg.Scheduler.MaxLoadAvg > 0 {
		gate = &sysinfo.Gate{
			MaxCPUPercent: d.cfg.Scheduler.MaxCPUPercent,
			MaxLoadAvg:    d.cfg.Scheduler.MaxLoadAvg,
		}
	}
	var gateAllow func() bool
	if gate != nil {
		gateAllow = gate.Allow
	}
	// the pool owns the worker goroutines so it can be scaled at runtime
	d.pool.setOptions(worker.Options{
		Store:         d.store,
		Bus:           d.bus,
		Sched:         d.sched,
		SM:            d.sm,
		Registry:      d.reg,
		Executor:      d.exec,
		Storage:       d.storageFor,
		OutputStore:   d.store,
		LeaseTTL:      leaseTTL,
		InputResolver: d.resolveInputOr,
		Gate:          gateAllow,
	})
	if err := d.pool.Scale(ctx, count); err != nil {
		return err
	}

	// metrics bus subscription
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-d.bus.SubscribeAll():
				if !ok {
					return
				}
				d.metric.Handle(e)
				d.refreshWorkerGauges()
			}
		}
	}()

	// HTTP server
	ln, err := net.Listen("tcp", d.cfg.Network.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", d.cfg.Network.Listen, err)
	}
	d.Addr = ln.Addr().String()
	d.httpSrv = &http.Server{Handler: d.srv.Router(), ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		log.Printf("weft serving %s", d.cfg.Network.Listen)
		if err := d.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = d.httpSrv.Shutdown(shutCtx)
	d.wg.Wait()
	if d.pool != nil {
		d.pool.Wait()
	}
	return nil
}

// Stop cancels Serve.
func (d *Daemon) Stop() {
	if d.cancel != nil {
		d.cancel()
	}
}

// Store exposes the underlying store (used by tests for assertions/cleanup).
func (d *Daemon) Store() *sqlite.Store { return d.store }

func (d *Daemon) seedWebhooks(ctx context.Context) error {
	for _, wc := range d.cfg.Webhooks {
		id := wc.ID
		if id == "" {
			id = "webhook_" + core.NewID("")
		}
		if wc.MaxRetries <= 0 {
			wc.MaxRetries = 5
		}
		if wc.TimeoutSeconds <= 0 {
			wc.TimeoutSeconds = 10
		}
		if err := d.store.SaveWebhook(ctx, toStoreWebhook(id, wc)); err != nil {
			return fmt.Errorf("seed webhook %s: %w", id, err)
		}
	}
	return nil
}

func (d *Daemon) refreshWorkerGauges() {
	snap := d.pool.Snapshot()
	busy, idle := 0, 0
	for _, s := range snap {
		if s.Status == "busy" {
			busy++
		} else {
			idle++
		}
	}
	d.metric.SetWorkers(busy, idle)
}

// resolveInputOr returns the injected InputResolver when set, else the default.
func (d *Daemon) resolveInputOr(job core.Job) (string, error) {
	if d.inputResolver != nil {
		return d.inputResolver(job)
	}
	return d.resolveInput(job)
}

// resolveInput turns a job's InputRef into a local path ffmpeg can read.
// Supported refs: "local:/abs/path", "/abs/path", "C:\..." (Windows). Remote
// schemes (s3://, ssh://, http://) are not fetched in this phase — they error
// loudly so the job fails fast instead of silently producing nothing.
func (d *Daemon) resolveInput(job core.Job) (string, error) {
	ref := strings.TrimSpace(job.InputRef)
	if ref == "" {
		return "", fmt.Errorf("job %s has empty input_ref", job.ID)
	}
	for _, scheme := range []string{"s3://", "ssh://", "http://", "https://"} {
		if strings.HasPrefix(ref, scheme) {
			return "", fmt.Errorf("input_ref %q: fetching remote inputs is not supported yet", ref)
		}
	}
	ref = strings.TrimPrefix(ref, "local:")
	if ref == "" {
		return "", fmt.Errorf("input_ref %q resolves to an empty local path", job.InputRef)
	}
	if fi, err := os.Stat(ref); err != nil {
		return "", fmt.Errorf("input_ref %q not readable: %w", job.InputRef, err)
	} else if fi.IsDir() {
		return "", fmt.Errorf("input_ref %q is a directory, want a file", job.InputRef)
	}
	return ref, nil
}

// storageFor resolves the destination storage for a job from its DestinationID.
// DestinationID 0 means the default local storage (config storage.local.base_path).
// A job's DestPath is appended under the storage root so one server can serve
// many directories (e.g. movie/, series/) selected per job.
func (d *Daemon) storageFor(job core.Job) (core.Storage, error) {
	return d.storageForID(context.Background(), job.DestinationID, job.DestPath)
}

// storageForID builds a core.Storage for a destination server id + subdir.
// Destination 0 is the default local storage; other ids must be registered.
func (d *Daemon) storageForID(ctx context.Context, destinationID int, destPath string) (core.Storage, error) {
	join := func(root, sub string) string {
		if sub == "" {
			return root
		}
		return path.Join(root, filepath.ToSlash(sub))
	}
	if destinationID == 0 {
		return local.New(join(d.cfg.Storage.Local.BasePath, destPath))
	}
	servers, err := d.store.ListStorageServers(ctx)
	if err != nil {
		return nil, err
	}
	for _, sv := range servers {
		if sv.ID != destinationID {
			continue
		}
		return buildStorage(sv.Type, sv, destPath)
	}
	return nil, fmt.Errorf("destination_id %d not registered", destinationID)
}

// outboxEnqueuer subscribes to the bus and enqueues an outbox row for every
// configured webhook whose Events list matches the event's wire name.
type outboxEnqueuer struct {
	store    *sqlite.Store
	bus      core.EventBus
	interval time.Duration
}

func (o *outboxEnqueuer) Run(ctx context.Context) error {
	sub := o.bus.SubscribeAll()
	tick := time.NewTicker(o.interval)
	defer tick.Stop()
	// drain periodically too in case a webhook was added after events fired
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-sub:
			if !ok {
				return nil
			}
			if err := o.enqueueFor(ctx, e); err != nil {
				return err
			}
		case <-tick.C:
			// nothing; bus events drive enqueueing
		}
	}
}

func (o *outboxEnqueuer) enqueueFor(ctx context.Context, e core.Event) error {
	whs, err := o.store.ListWebhooks(ctx)
	if err != nil {
		return err
	}
	wire := webhook.WireName(e.Kind)
	for _, wh := range whs {
		if !matches(wh.Events, wire) {
			continue
		}
		if err := o.store.Outbox().Enqueue(ctx, e.ID, wh.ID, core.Now()); err != nil {
			return err
		}
	}
	return nil
}

func matches(list []string, wire string) bool {
	for _, s := range list {
		if s == wire || s == "*" {
			return true
		}
	}
	return false
}

// expirer periodically requeues tasks with expired leases so a dead worker's
// task is picked up by a healthy one instead of stalling the job forever.
func expirer(ctx context.Context, store *sqlite.Store, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			ids, err := store.RequeueExpired(ctx, core.Now())
			if err != nil {
				log.Printf("lease expirer: %v", err)
				continue
			}
			if len(ids) > 0 {
				log.Printf("lease expirer requeued %d expired tasks", len(ids))
			}
		}
	}
}

// retentionPruner periodically deletes events (and their outbox rows) older
// than retentionDays so the event log doesn't grow without bound. Best effort:
// a failed prune is logged and retried next tick.
func retentionPruner(ctx context.Context, store *sqlite.Store, retentionDays int, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			cutoff := core.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
			n, err := store.PruneEvents(ctx, cutoff)
			if err != nil {
				log.Printf("event retention: %v", err)
				continue
			}
			if n > 0 {
				log.Printf("event retention pruned %d events older than %d days", n, retentionDays)
			}
		}
	}
}
