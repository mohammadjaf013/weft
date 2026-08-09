package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
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

	// Outbox rows are enqueued synchronously, in the same DB transaction as the
	// triggering state change (runtime/store/sqlite's insertEventTx) — no
	// separate bus-driven enqueuer goroutine needed here anymore.

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

	// job retention: periodically delete terminal-status jobs (and their
	// tasks/events/outputs/logs) older than the configured retention window,
	// so job history — and the per-poll scan in NextReady/GET /queue — never
	// grows without bound. cron.cleanup.retention_hours; 0 disables.
	if hrs := d.cfg.Cron.Cleanup.RetentionHours; hrs > 0 {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			jobRetentionPruner(ctx, d.store, hrs, time.Hour)
		}()
	}

	// cron: cleanup/benchmark/health_scan on their configured schedules, each
	// also triggerable on demand (GET /cron, POST /cron/{job}/run, weft cron
	// list/run). The scheduler itself only checks for due jobs every tick —
	// a minute is plenty of resolution for schedules measured in minutes/hours.
	if d.cronSched != nil {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.cronSched.Run(ctx, time.Minute)
		}()
	}

	// workers. A resource gate idles workers when the host exceeds the
	// scheduler thresholds, so the queue stays put instead of saturating CPU.
	// workers.max caps how many tasks run at once (spawn that many workers);
	// workers.max: 0 means "auto" — see resolveWorkerCount.
	count := resolveWorkerCount(d.cfg.Workers.Min, d.cfg.Workers.Max, runtime.NumCPU())
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
	var budget *worker.Budget
	if d.cfg.Scheduler.MaxEstimatedCPUCores > 0 || d.cfg.Scheduler.MaxEstimatedRAMMB > 0 {
		budget = worker.NewBudget(d.cfg.Scheduler.MaxEstimatedCPUCores, d.cfg.Scheduler.MaxEstimatedRAMMB)
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
		Budget:        budget,
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

// resolveWorkerCount computes the worker pool's steady-state size.
// workers.max > 0 is an explicit cap (used as-is, so long as it's >= min).
// workers.max == 0 ("auto") means one worker per logical CPU core — bounded
// below by workers.min — so a bigger box actually gets a bigger default pool
// instead of the previous behavior, where "auto" silently meant "use
// workers.min" (1, by default) regardless of host size. numCPU is a
// parameter (not read via runtime.NumCPU() internally) so this is testable
// without depending on the test machine's actual core count.
func resolveWorkerCount(min, max, numCPU int) int {
	if min <= 0 {
		min = 1
	}
	switch {
	case max > 0 && max >= min:
		return max
	case max == 0 && numCPU > min:
		return numCPU
	}
	return min
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
func (d *Daemon) resolveInputOr(ctx context.Context, job core.Job) (string, error) {
	if d.inputResolver != nil {
		return d.inputResolver(ctx, job)
	}
	return d.resolveInput(ctx, job)
}

// resolveInput turns a job's InputRef into a local path ffmpeg can read.
// Three cases:
//   - job.SourceServerID != 0: InputRef is a relative path fetched from that
//     REGISTERED storage server (the same servers already used for job
//     output, weft storage add) into a local cache file — works uniformly
//     across local/ssh/s3 since it only depends on core.Storage.Open.
//   - InputRef starts with http:// or https://: fetched directly via one
//     HTTP GET into the same local cache.
//   - otherwise: "local:/abs/path", "/abs/path", "C:\..." (Windows) resolved
//     directly, no fetch, exactly as before. A bare s3://ssh:// InputRef
//     without a source_server_id still errors loudly — there is no
//     credential to fetch it with.
func (d *Daemon) resolveInput(ctx context.Context, job core.Job) (string, error) {
	ref := strings.TrimSpace(job.InputRef)
	if ref == "" {
		return "", fmt.Errorf("job %s has empty input_ref", job.ID)
	}
	if job.SourceServerID != 0 {
		return d.fetchFromSourceServer(ctx, job, ref)
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return d.fetchHTTP(ctx, job, ref)
	}
	for _, scheme := range []string{"s3://", "ssh://"} {
		if strings.HasPrefix(ref, scheme) {
			return "", fmt.Errorf("input_ref %q: remote sources need source_server_id set to a registered storage server (weft storage add)", ref)
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

// fetchFromSourceServer streams job.InputRef (a relative path, e.g.
// "movies/foo.mp4") from the registered storage server job.SourceServerID
// into a local cache file, returning the cache path.
func (d *Daemon) fetchFromSourceServer(ctx context.Context, job core.Job, relPath string) (string, error) {
	st, err := d.storageForID(ctx, job.SourceServerID, "")
	if err != nil {
		return "", fmt.Errorf("source_server_id %d: %w", job.SourceServerID, err)
	}
	rc, err := st.Open(ctx, core.AssetRef{Name: relPath})
	if err != nil {
		return "", fmt.Errorf("fetch source %q from server %d: %w", relPath, job.SourceServerID, err)
	}
	defer rc.Close()
	return d.cacheToLocal(job, relPath, rc)
}

// fetchHTTP downloads a directly-reachable http(s):// InputRef into a local
// cache file.
func (d *Daemon) fetchHTTP(ctx context.Context, job core.Job, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %q: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("fetch %q: http %d", url, resp.StatusCode)
	}
	name := path.Base(strings.TrimRight(url, "/"))
	if name == "" || name == "." || name == "/" {
		name = string(job.ID)
	}
	return d.cacheToLocal(job, name, resp.Body)
}

// cacheToLocal writes r to a per-job scratch cache dir under
// mediautil.WorkRoot, naming the file after the source's base name. Removed
// by the cleaner once the job finishes (daemon/cleanup.go), same as the
// per-task work dirs.
func (d *Daemon) cacheToLocal(job core.Job, name string, r io.Reader) (string, error) {
	dir := filepath.Join(mediautil.WorkRoot, "cache", string(job.ID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, filepath.Base(name))
	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		os.Remove(dest)
		return "", fmt.Errorf("cache %q: %w", dest, err)
	}
	return dest, nil
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

// jobRetentionPruner periodically deletes terminal-status jobs (and their
// tasks/events/outputs/logs) older than retentionHours so job history never
// grows without bound. Best effort: a failed prune is logged and retried next
// tick.
func jobRetentionPruner(ctx context.Context, store *sqlite.Store, retentionHours int, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			cutoff := core.Now().Add(-time.Duration(retentionHours) * time.Hour)
			n, err := store.PruneJobs(ctx, cutoff)
			if err != nil {
				log.Printf("job retention: %v", err)
				continue
			}
			if n > 0 {
				log.Printf("job retention pruned %d jobs older than %dh", n, retentionHours)
			}
		}
	}
}
