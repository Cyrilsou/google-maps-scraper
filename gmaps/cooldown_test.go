package gmaps

import (
	"context"
	"testing"
	"time"
)

func TestAutoCooldownTriggersAfterThreshold(t *testing.T) {
	ac := NewAutoCooldown(3, time.Second, 50*time.Millisecond)

	for i := 0; i < 3; i++ {
		ac.RecordBlock()
	}

	if !ac.Active() {
		t.Fatal("expected cooldown to be active after 3 blocks")
	}

	remaining := ac.Remaining()
	if remaining <= 0 {
		t.Fatalf("expected positive remaining, got %v", remaining)
	}

	// Wait it out.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	slept := ac.Wait(ctx)
	if slept == 0 {
		t.Error("Wait returned 0 while cooldown was supposed to be active")
	}

	if ac.Active() {
		t.Error("cooldown still active after waiting it out")
	}
}

func TestAutoCooldownWindowed(t *testing.T) {
	ac := NewAutoCooldown(3, 40*time.Millisecond, 10*time.Millisecond)

	// Two blocks, then wait past the window, then two more. Neither batch
	// alone should trip the threshold.
	ac.RecordBlock()
	ac.RecordBlock()

	time.Sleep(60 * time.Millisecond)

	ac.RecordBlock()
	ac.RecordBlock()

	if ac.Active() {
		t.Fatal("cooldown should not have triggered — events were spread across windows")
	}
}

func TestAutoCooldownNilSafe(t *testing.T) {
	var ac *AutoCooldown

	ac.RecordBlock()
	if ac.Active() {
		t.Error("nil AutoCooldown should never be active")
	}

	if ac.Wait(context.Background()) != 0 {
		t.Error("nil AutoCooldown.Wait should return 0")
	}
}
