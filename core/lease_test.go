package core

import (
	"context"
	"testing"
	"time"
)

func TestLeaseReserveHeartbeatRelease(t *testing.T) {
	l := NewMemLease()
	ctx := context.Background()

	if err := l.Reserve(ctx, "t1", "worker-a", time.Minute); err != nil {
		t.Fatal(err)
	}
	// second reserve while active must fail
	if err := l.Reserve(ctx, "t1", "worker-b", time.Minute); err == nil {
		t.Fatal("expected second reserve to be rejected")
	}
	// heartbeat from wrong worker must fail
	if err := l.Heartbeat(ctx, "t1", "worker-b"); err == nil {
		t.Fatal("expected heartbeat from wrong worker to fail")
	}
	if err := l.Heartbeat(ctx, "t1", "worker-a"); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	// after release, a fresh reserve works
	if err := l.Reserve(ctx, "t1", "worker-c", time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseExpiry(t *testing.T) {
	l := NewMemLease()
	ctx := context.Background()

	// use a short ttl and frozen clock
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	Now = func() time.Time { return base }
	t.Cleanup(func() { Now = func() time.Time { return time.Now().UTC() } })

	if err := l.Reserve(ctx, "t1", "worker-a", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	// advance clock past expiry
	Now = func() time.Time { return base.Add(30 * time.Second) }
	expired, err := l.ExpireStale(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0] != "t1" {
		t.Fatalf("expired = %v, want [t1]", expired)
	}
	// now re-reservable
	if err := l.Reserve(ctx, "t1", "worker-b", time.Minute); err != nil {
		t.Fatal(err)
	}
}
