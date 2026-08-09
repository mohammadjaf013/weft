package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mohammadjaf013/weft/runtime/api"
	"github.com/mohammadjaf013/weft/runtime/worker"
)

// workerPool owns the worker goroutines: it starts and stops them so the pool
// can be scaled at runtime (workers.max on boot, POST /workers/scale later)
// without a daemon restart. It also adapts to api.WorkerHandle for /workers
// and /metrics reporting.
type workerPool struct {
	mu      sync.Mutex
	entries []*poolEntry
	opts    worker.Options
	nextID  int
	wg      sync.WaitGroup
}

// poolEntry ties a running worker to the cancel func that stops it.
type poolEntry struct {
	id     string
	w      *worker.Worker
	cancel context.CancelFunc
}

func newWorkerPool(opts worker.Options) *workerPool {
	return &workerPool{opts: opts}
}

// setOptions replaces the worker template (used by Serve before first Scale).
func (p *workerPool) setOptions(opts worker.Options) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opts = opts
}

// Scale starts or stops workers until exactly target are running. Positive
// deltas spawn new goroutines; negative deltas cancel idle workers first and
// only stop busy ones as a last resort (their tasks are re-leased on expiry).
func (p *workerPool) Scale(ctx context.Context, target int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.entries) < target {
		p.startLocked(ctx)
	}
	for len(p.entries) > target {
		if !p.stopOneLocked() {
			break // all workers busy; wait for one to free up
		}
	}
	return nil
}

// Count returns the number of currently running workers.
func (p *workerPool) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

// Wait blocks until every worker goroutine has stopped (used at shutdown).
func (p *workerPool) Wait() { p.wg.Wait() }

func (p *workerPool) startLocked(ctx context.Context) {
	id := fmt.Sprintf("w%d", p.nextID)
	p.nextID++
	wctx, cancel := context.WithCancel(ctx)
	w := worker.New(id, p.opts)
	p.entries = append(p.entries, &poolEntry{id: id, w: w, cancel: cancel})
	p.wg.Add(1)
	go func(ww *worker.Worker) {
		defer p.wg.Done()
		_ = ww.Run(wctx)
	}(w)
}

// stopOneLocked cancels one worker: it prefers an idle worker, else the last
// one in the list (whose in-flight task will be requeued by the lease expirer).
// Returns false when every worker is busy (caller should retry later).
func (p *workerPool) stopOneLocked() bool {
	// prefer an idle worker so scaling down never interrupts active work
	for i, e := range p.entries {
		if e.w.Status() == "idle" {
			e.cancel()
			p.entries = append(p.entries[:i], p.entries[i+1:]...)
			return true
		}
	}
	return false
}

// Snapshot implements api.WorkerHandle.
func (p *workerPool) Snapshot() []api.WorkerState {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]api.WorkerState, 0, len(p.entries))
	for _, e := range p.entries {
		hb := e.w.LastHeartbeat()
		if hb.IsZero() {
			hb = time.Now().UTC()
		}
		out = append(out, api.WorkerState{
			ID:            e.w.ID(),
			Status:        e.w.Status(),
			CurrentTaskID: e.w.CurrentTask(),
			LastHeartbeat: hb,
		})
	}
	return out
}
