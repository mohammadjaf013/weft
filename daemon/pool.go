package daemon

import (
	"time"

	"github.com/mohammadjaf013/weft/runtime/api"
	"github.com/mohammadjaf013/weft/runtime/worker"
)

// workerPool adapts a set of *worker.Worker to api.WorkerHandle so /workers and
// /metrics can report live state.
type workerPool struct {
	workers []*worker.Worker
}

func newWorkerPool() *workerPool { return &workerPool{} }

func (p *workerPool) add(w *worker.Worker) { p.workers = append(p.workers, w) }

// Snapshot implements api.WorkerHandle.
func (p *workerPool) Snapshot() []api.WorkerState {
	out := make([]api.WorkerState, 0, len(p.workers))
	for _, w := range p.workers {
		hb := w.LastHeartbeat()
		if hb.IsZero() {
			hb = time.Now().UTC()
		}
		out = append(out, api.WorkerState{
			ID:            w.ID(),
			Status:        w.Status(),
			CurrentTaskID: w.CurrentTask(),
			LastHeartbeat: hb,
		})
	}
	return out
}
