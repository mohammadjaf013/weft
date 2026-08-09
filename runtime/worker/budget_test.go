package worker

import "testing"

func TestBudgetTryReserveWithinCeiling(t *testing.T) {
	b := NewBudget(4, 2048)
	if !b.TryReserve(1.5, 512) {
		t.Fatal("expected first reservation within budget to succeed")
	}
	if !b.TryReserve(2, 1024) {
		t.Fatal("expected second reservation still within budget to succeed")
	}
	if b.TryReserve(1, 1) {
		t.Fatal("expected reservation exceeding cpu ceiling to fail")
	}
}

func TestBudgetReleaseFreesCapacity(t *testing.T) {
	b := NewBudget(2, 0) // RAM unlimited
	if !b.TryReserve(2, 100) {
		t.Fatal("expected full-budget reservation to succeed")
	}
	if b.TryReserve(0.1, 0) {
		t.Fatal("expected reservation over cpu ceiling to fail while budget is fully used")
	}
	b.Release(2, 100)
	if !b.TryReserve(2, 100) {
		t.Fatal("expected reservation to succeed again after release")
	}
}

func TestBudgetAdmitsASingleOversizedTaskWhenEmpty(t *testing.T) {
	// A task costlier than the whole budget must still be admitted when
	// nothing else is reserved — otherwise a budget smaller than one task's
	// declared cost would starve every task forever.
	b := NewBudget(1, 0)
	if !b.TryReserve(5, 0) {
		t.Fatal("expected an oversized task to be admitted when the budget is empty")
	}
	// but a SECOND task must now be refused, even a cheap one.
	if b.TryReserve(0.1, 0) {
		t.Fatal("expected a second task to be refused while the oversized one is still reserved")
	}
}

func TestBudgetUnlimitedDimension(t *testing.T) {
	b := NewBudget(0, 0) // both unlimited
	for i := 0; i < 100; i++ {
		if !b.TryReserve(10, 10000) {
			t.Fatalf("reservation %d should succeed against an unlimited budget", i)
		}
	}
}

func TestBudgetReleaseNeverGoesNegative(t *testing.T) {
	b := NewBudget(4, 4096)
	b.Release(10, 10000) // release without a matching reserve
	if !b.TryReserve(4, 4096) {
		t.Fatal("expected full budget to still be available after an over-release")
	}
}
