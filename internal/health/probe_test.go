package health

import (
	"testing"
	"time"
)

func TestProbeEmptySnapshotIsHealthy(t *testing.T) {
	p := New()
	samples, ratio := p.Snapshot()
	if samples != 0 {
		t.Fatalf("expected 0 samples, got %d", samples)
	}
	if ratio != 1.0 {
		t.Fatalf("expected ratio 1.0 on empty probe, got %v", ratio)
	}
}

func TestProbeRatioReflectsObservations(t *testing.T) {
	p := New()
	for i := 0; i < 4; i++ {
		p.ObserveSuccess()
	}
	for i := 0; i < 6; i++ {
		p.ObserveFailure()
	}
	samples, ratio := p.Snapshot()
	if samples != 10 {
		t.Fatalf("expected 10 samples, got %d", samples)
	}
	if ratio != 0.4 {
		t.Fatalf("expected ratio 0.4, got %v", ratio)
	}
}

func TestProbeRollsAfterWindow(t *testing.T) {
	p := NewWithWindow(50 * time.Millisecond)
	p.ObserveFailure()
	p.ObserveFailure()
	if samples, _ := p.Snapshot(); samples != 2 {
		t.Fatalf("expected 2 samples before roll, got %d", samples)
	}
	time.Sleep(80 * time.Millisecond)
	p.ObserveSuccess()
	samples, ratio := p.Snapshot()
	if samples != 1 || ratio != 1.0 {
		t.Fatalf("expected fresh window with 1 success, got samples=%d ratio=%v", samples, ratio)
	}
}
