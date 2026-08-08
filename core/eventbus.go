package core

import "sync"

// inmemEventBus is an in-process publish/subscribe implementation of EventBus.
// All consumers (outbox dispatcher, metrics, event-table subscriber) are plain
// subscribers. Core never knows who is listening.
type inmemEventBus struct {
	mu     sync.RWMutex
	closed bool
	// global subscribers receive every event
	all []chan Event
	// kind subscribers receive only matching events
	byKind map[EventKind][]chan Event
}

func NewEventBus() EventBus {
	return &inmemEventBus{byKind: map[EventKind][]chan Event{}}
}

func (b *inmemEventBus) Publish(e Event) {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return
	}
	snapshotAll := make([]chan Event, len(b.all))
	copy(snapshotAll, b.all)
	snapshotKind := make([]chan Event, 0, 8)
	for _, ch := range b.byKind[e.Kind] {
		snapshotKind = append(snapshotKind, ch)
	}
	b.mu.RUnlock()

	// Non-blocking sends: a slow consumer must not stall the publisher. This is
	// deliberate — Event Sourcing durability lives in the Store, not the bus.
	for _, ch := range snapshotAll {
		select {
		case ch <- e:
		default:
		}
	}
	for _, ch := range snapshotKind {
		select {
		case ch <- e:
		default:
		}
	}
}

func (b *inmemEventBus) Subscribe(kind EventKind) <-chan Event {
	ch := make(chan Event, 256)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(ch)
		return ch
	}
	b.byKind[kind] = append(b.byKind[kind], ch)
	return ch
}

func (b *inmemEventBus) SubscribeAll() <-chan Event {
	ch := make(chan Event, 256)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(ch)
		return ch
	}
	b.all = append(b.all, ch)
	return ch
}

func (b *inmemEventBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for _, ch := range b.all {
		close(ch)
	}
	for _, chans := range b.byKind {
		for _, ch := range chans {
			close(ch)
		}
	}
	b.all = nil
	b.byKind = map[EventKind][]chan Event{}
}
