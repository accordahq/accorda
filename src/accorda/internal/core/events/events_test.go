package events

import (
	"context"
	"sync"
	"testing"
)

func TestBus_Publish_DeliversInOrder(t *testing.T) {
	b := NewBus()
	var got []string
	var mu sync.Mutex
	record := func(name string) Handler {
		return func(_ context.Context, e Event) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, name+":"+e.Type)
		}
	}
	b.Subscribe(record("a"))
	b.Subscribe(record("b"))

	b.Publish(context.Background(), Event{Type: EventDeploymentDetected})
	b.Publish(context.Background(), Event{Type: EventDeploymentStarted})

	want := []string{
		"a:" + EventDeploymentDetected,
		"b:" + EventDeploymentDetected,
		"a:" + EventDeploymentStarted,
		"b:" + EventDeploymentStarted,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBus_Unsubscribe_StopsDelivery(t *testing.T) {
	b := NewBus()
	var count int
	unsub := b.Subscribe(func(_ context.Context, e Event) { count++ })

	b.Publish(context.Background(), Event{Type: EventDeploymentDetected})
	if count != 1 {
		t.Fatalf("count after first publish = %d, want 1", count)
	}

	unsub()
	b.Publish(context.Background(), Event{Type: EventDeploymentDetected})
	if count != 1 {
		t.Fatalf("count after unsubscribe = %d, want 1", count)
	}

	// Unsubscribing twice is safe.
	unsub()
	b.Publish(context.Background(), Event{Type: EventDeploymentDetected})
	if count != 1 {
		t.Fatalf("count after double unsubscribe = %d, want 1", count)
	}
}

func TestBus_Publish_NoSubscribers(t *testing.T) {
	b := NewBus()
	// Publishing with no subscribers must not panic.
	b.Publish(context.Background(), Event{Type: EventDeploymentDetected})
}

func TestBus_ConcurrentPublish(t *testing.T) {
	b := NewBus()
	var mu sync.Mutex
	var count int
	b.Subscribe(func(_ context.Context, e Event) {
		mu.Lock()
		count++
		mu.Unlock()
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Publish(context.Background(), Event{Type: EventDeploymentDetected})
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if count != 100 {
		t.Errorf("count = %d, want 100", count)
	}
}
