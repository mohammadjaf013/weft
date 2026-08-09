package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/mohammadjaf013/weft/runtime/worker"
)

func TestWorkerPoolScale(t *testing.T) {
	p := newWorkerPool(worker.Options{})
	defer p.Wait()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.Scale(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if got := p.Count(); got != 3 {
		t.Fatalf("count after scale up = %d, want 3", got)
	}
	// idle workers are stopped first; eventually all of them go away
	if err := p.Scale(ctx, 0); err != nil {
		t.Fatal(err)
	}
	// cancel is async; workers drain shortly after
	deadline := time.Now().Add(3 * time.Second)
	for p.Count() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := p.Count(); got != 0 {
		t.Fatalf("count after scale down = %d, want 0", got)
	}
}
