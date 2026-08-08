// Package metrics exposes Prometheus-compatible counters/gauges built from
// Event Bus subscriptions and Store snapshots. No external metric dependency —
// we render the text exposition format directly (Prometheus is simple enough
// that a hand-rolled exporter keeps v1 dependency-free and trivially testable).
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/mohammadjaf013/weft/core"
)

// Metrics is the runtime's metric registry + event bus subscriber.
type Metrics struct {
	mu sync.Mutex

	// counters keyed by kind
	eventsByKind map[core.EventKind]uint64
	tasksDone    uint64
	tasksFailed  uint64
	webhooksSent uint64
	webhookFail  uint64

	// gauges
	queuedJobs    uint64
	runningJobs   uint64
	completedJobs uint64
	failedJobs    uint64
	workerBusy    uint64
	workerIdle    uint64

	store StoreSnapshot
}

// StoreSnapshot is the tiny slice of Store that metrics needs; the API layer
// provides it so the exporter never imports Runtime.
type StoreSnapshot interface {
	Snapshot() (queued, running, completed, failed uint64)
}

func New(store StoreSnapshot) *Metrics {
	return &Metrics{
		eventsByKind: map[core.EventKind]uint64{},
		store:        store,
	}
}

// Handle processes events from the bus (call it from a subscriber goroutine).
func (m *Metrics) Handle(e core.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventsByKind[e.Kind]++
	switch e.Kind {
	case core.EvtPluginFinished:
		m.tasksDone++
	case core.EvtJobFailed:
		m.failedJobs++
	case core.EvtJobFinished:
		m.completedJobs++
	}
}

// IncWebhook tracks webhook deliveries (called by the dispatcher).
func (m *Metrics) IncWebhook(sent bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sent {
		m.webhooksSent++
	} else {
		m.webhookFail++
	}
}

// SetWorkers updates the worker gauge.
func (m *Metrics) SetWorkers(busy, idle int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workerBusy = uint64(busy)
	m.workerIdle = uint64(idle)
}

// Render produces Prometheus text exposition format.
func (m *Metrics) Render() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	queued, running, completed, failed := uint64(0), uint64(0), uint64(0), uint64(0)
	if m.store != nil {
		queued, running, completed, failed = m.store.Snapshot()
	}
	m.queuedJobs, m.runningJobs, m.completedJobs, m.failedJobs = queued, running, completed, failed

	var b strings.Builder
	fmt.Fprintf(&b, "# HELP weft_events_total Total events by kind.\n")
	fmt.Fprintf(&b, "# TYPE weft_events_total counter\n")
	kinds := make([]string, 0, len(m.eventsByKind))
	for k := range m.eventsByKind {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Fprintf(&b, "weft_events_total{kind=%q} %d\n", k, m.eventsByKind[core.EventKind(k)])
	}
	fmt.Fprintf(&b, "# TYPE weft_jobs counter\n")
	fmt.Fprintf(&b, "weft_jobs{status=\"queued\"} %d\n", m.queuedJobs)
	fmt.Fprintf(&b, "weft_jobs{status=\"running\"} %d\n", m.runningJobs)
	fmt.Fprintf(&b, "weft_jobs{status=\"completed\"} %d\n", m.completedJobs)
	fmt.Fprintf(&b, "weft_jobs{status=\"failed\"} %d\n", m.failedJobs)
	fmt.Fprintf(&b, "# TYPE weft_workers gauge\n")
	fmt.Fprintf(&b, "weft_workers{state=\"busy\"} %d\n", m.workerBusy)
	fmt.Fprintf(&b, "weft_workers{state=\"idle\"} %d\n", m.workerIdle)
	fmt.Fprintf(&b, "# TYPE weft_webhooks_total counter\n")
	fmt.Fprintf(&b, "weft_webhooks_sent_total %d\n", m.webhooksSent)
	fmt.Fprintf(&b, "weft_webhooks_failed_total %d\n", m.webhookFail)
	return b.String()
}
