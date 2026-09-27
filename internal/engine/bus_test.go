package engine

import "testing"

func TestBusPublishSubscribe(t *testing.T) {
	b := NewBus()
	ch, cancel := b.Subscribe()
	b.Publish(MonitorChanged{ProjectID: "p", MonitorID: "m"})
	select {
	case e := <-ch:
		if e.MonitorID != "m" {
			t.Fatalf("got %+v", e)
		}
	default:
		t.Fatal("no event delivered")
	}
	cancel()
	b.Publish(MonitorChanged{MonitorID: "after cancel"})
	select {
	case e := <-ch:
		t.Fatalf("unexpected event after cancel: %+v", e)
	default:
	}
}

func TestBusDropsWhenFull(t *testing.T) {
	b := NewBus()
	_, cancel := b.Subscribe()
	defer cancel()
	for range 70 {
		b.Publish(MonitorChanged{})
	}
	if b.Dropped.Load() != 6 {
		t.Fatalf("dropped = %d", b.Dropped.Load())
	}
}
