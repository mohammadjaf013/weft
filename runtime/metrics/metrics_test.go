package metrics

import (
	"strings"
	"testing"

	"github.com/mohammadjaf013/weft/core"
)

type fakeSnapshot struct{ q, r, c, f uint64 }

func (s fakeSnapshot) Snapshot() (uint64, uint64, uint64, uint64) {
	return s.q, s.r, s.c, s.f
}

func TestRenderPrometheus(t *testing.T) {
	m := New(fakeSnapshot{q: 3, r: 2, c: 10, f: 1})
	m.Handle(core.Event{Kind: core.EvtPluginFinished})
	m.Handle(core.Event{Kind: core.EvtPluginFinished})
	m.Handle(core.Event{Kind: core.EvtJobFailed})
	m.SetWorkers(2, 1)

	out := m.Render()
	if !strings.Contains(out, `weft_jobs{status="queued"} 3`) {
		t.Errorf("missing queued gauge:\n%s", out)
	}
	if !strings.Contains(out, `weft_jobs{status="completed"} 10`) {
		t.Errorf("missing completed gauge:\n%s", out)
	}
	if !strings.Contains(out, `weft_events_total{kind="PluginFinished"} 2`) {
		t.Errorf("missing event counter:\n%s", out)
	}
	if !strings.Contains(out, `weft_workers{state="busy"} 2`) {
		t.Errorf("missing worker gauge:\n%s", out)
	}
	if !strings.Contains(out, `weft_webhooks_sent_total 0`) {
		t.Errorf("missing webhook counter:\n%s", out)
	}
}
