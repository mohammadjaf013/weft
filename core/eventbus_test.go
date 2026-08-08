package core

import (
	"context"
	"testing"
	"time"
)

func TestEventBusPubSub(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	got := make(chan Event, 16)
	sub := bus.Subscribe(EvtJobCreated)
	go func() {
		for e := range sub {
			got <- e
		}
	}()

	e := Event{ID: "e1", JobID: "j1", Kind: EvtJobCreated}
	bus.Publish(e)

	select {
	case gotE := <-got:
		if gotE.ID != "e1" {
			t.Fatalf("got event %+v, want e1", gotE)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventBusFiltersByKind(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	jobSub := bus.Subscribe(EvtJobCreated)
	finSub := bus.Subscribe(EvtJobFinished)

	bus.Publish(Event{ID: "e1", Kind: EvtJobCreated})
	bus.Publish(Event{ID: "e2", Kind: EvtJobFinished})

	select {
	case e := <-jobSub:
		if e.ID != "e1" {
			t.Fatalf("job subscriber got %q, want e1", e.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("job subscriber missed event")
	}
	select {
	case e := <-finSub:
		if e.ID != "e2" {
			t.Fatalf("fin subscriber got %q, want e2", e.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("fin subscriber missed event")
	}
	// job subscriber must not receive finished event
	select {
	case e := <-jobSub:
		t.Fatalf("job subscriber wrongly got %q", e.ID)
	default:
	}
}

func TestEventBusSubscribeAll(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()
	all := bus.SubscribeAll()
	bus.Publish(Event{ID: "a", Kind: EvtJobCreated})
	bus.Publish(Event{ID: "b", Kind: EvtNodeJoined})
	seen := []string{}
	timeout := time.After(time.Second)
	for len(seen) < 2 {
		select {
		case e := <-all:
			seen = append(seen, e.ID)
		case <-timeout:
			t.Fatalf("only saw %v", seen)
		}
	}
}

func TestStateMachineHappyPath(t *testing.T) {
	store := NewMemStore()
	bus := NewEventBus()
	defer bus.Close()
	sm := NewStateMachine(store, bus)

	job := Job{ID: "j1", Status: JobQueued, Priority: PriorityNormal}
	ctx := context.Background()
	// create via store directly (as scheduler would)
	if err := store.SaveJobEvent(ctx, job, Event{ID: NewID("evt"), JobID: job.ID, Kind: EvtJobCreated, CreatedAt: Now()}); err != nil {
		t.Fatal(err)
	}
	chain := []JobStatus{JobReserved, JobRunning, JobUploading, JobCompleted}
	for _, to := range chain {
		if err := sm.Transition(ctx, "j1", to, ""); err != nil {
			t.Fatalf("transition to %s failed: %v", to, err)
		}
	}
	j, _ := store.LoadJob(ctx, "j1")
	if j.Status != JobCompleted {
		t.Fatalf("final status %s, want completed", j.Status)
	}
	events, _ := store.ListEvents(ctx, "j1")
	if len(events) < 4 {
		t.Fatalf("got %d events, want >=4: %v", len(events), events)
	}
	if events[len(events)-1].Kind != EvtJobFinished {
		t.Fatalf("last event %s, want JobFinished", events[len(events)-1].Kind)
	}
}

func TestStateMachineRejectsIllegalTransition(t *testing.T) {
	store := NewMemStore()
	bus := NewEventBus()
	defer bus.Close()
	sm := NewStateMachine(store, bus)
	ctx := context.Background()
	store.SaveJobEvent(ctx, Job{ID: "j1", Status: JobQueued, Priority: PriorityNormal}, Event{ID: NewID("evt"), JobID: "j1", Kind: EvtJobCreated, CreatedAt: Now()})

	// completed → running is illegal
	if err := sm.Transition(ctx, "j1", JobCompleted, ""); err != nil {
		// illegal, but the target here is also reachable? queued->completed is illegal too
		t.Logf("queued->completed rejected as expected: %v", err)
	}
	// legal path first
	if err := sm.Transition(ctx, "j1", JobReserved, ""); err != nil {
		t.Fatal(err)
	}
	// reserved -> completed is illegal
	err := sm.Transition(ctx, "j1", JobCompleted, "")
	if err == nil {
		t.Fatal("expected illegal transition reserved->completed to be rejected")
	}
}

func TestStateMachineNotFound(t *testing.T) {
	store := NewMemStore()
	bus := NewEventBus()
	defer bus.Close()
	sm := NewStateMachine(store, bus)
	if err := sm.Transition(context.Background(), "missing", JobCompleted, ""); err == nil {
		t.Fatal("expected error for missing job")
	}
}
