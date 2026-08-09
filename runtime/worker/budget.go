package worker

import "sync"

// Budget is a declarative admission-control counter: it sums the
// EstimatedCPU/EstimatedRAMMB (core.Capabilities, declared by every plugin)
// of currently in-flight tasks and refuses a new claim that would push either
// total over its configured ceiling. This is complementary to the resource
// Gate (measured, host-level CPU/load): Gate reacts to *actual* saturation,
// Budget prevents *over-committing* before saturation is even measurable —
// e.g. not starting 8 HLS encodes at once on an 8-core box just because
// instantaneous CPU sampling hasn't caught up yet.
//
// A zero ceiling on either dimension means that dimension is unlimited.
type Budget struct {
	mu        sync.Mutex
	maxCPU    float64
	maxRAMMB  int
	usedCPU   float64
	usedRAMMB int
}

// NewBudget builds a Budget with the given ceilings (0 = unlimited).
func NewBudget(maxCPUCores float64, maxRAMMB int) *Budget {
	return &Budget{maxCPU: maxCPUCores, maxRAMMB: maxRAMMB}
}

// TryReserve claims cpu/ramMB against the budget if doing so wouldn't exceed
// either configured ceiling. A single task costlier than the whole budget is
// still admitted when nothing else is currently reserved — a Budget smaller
// than one task's declared cost must not starve every task forever, it
// should only prevent a SECOND concurrent one from also starting.
func (b *Budget) TryReserve(cpu float64, ramMB int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.maxCPU > 0 && b.usedCPU > 0 && b.usedCPU+cpu > b.maxCPU {
		return false
	}
	if b.maxRAMMB > 0 && b.usedRAMMB > 0 && b.usedRAMMB+ramMB > b.maxRAMMB {
		return false
	}
	b.usedCPU += cpu
	b.usedRAMMB += ramMB
	return true
}

// Release returns cpu/ramMB to the budget once a task finishes (success,
// failure, or it never actually started, e.g. it lost the lease race after
// TryReserve — see worker.reserve).
func (b *Budget) Release(cpu float64, ramMB int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usedCPU -= cpu
	if b.usedCPU < 0 {
		b.usedCPU = 0
	}
	b.usedRAMMB -= ramMB
	if b.usedRAMMB < 0 {
		b.usedRAMMB = 0
	}
}
