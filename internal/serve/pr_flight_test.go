package serve

import (
	"sync"
	"testing"
	"time"
)

func TestTryWithPRFlightSkipsWhenBusy(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = WithPRFlight("acme/app#4", func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	ran, err := TryWithPRFlight("acme/app#4", func() error { return nil })
	if err != nil || ran {
		t.Fatalf("busy flight ran=%v err=%v", ran, err)
	}
	close(release)
	<-done
	ran, err = TryWithPRFlight("acme/app#4", func() error { return nil })
	if err != nil || !ran {
		t.Fatalf("free flight ran=%v err=%v", ran, err)
	}
}

func TestWithPRFlightSerializesAndDrops(t *testing.T) {
	var mu sync.Mutex
	var in int
	var maxIn int
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := WithPRFlight("acme/app#1", func() error {
				mu.Lock()
				in++
				if in > maxIn {
					maxIn = in
				}
				mu.Unlock()
				time.Sleep(5 * time.Millisecond)
				mu.Lock()
				in--
				mu.Unlock()
				return nil
			}); err != nil {
				t.Errorf("flight: %v", err)
			}
		}()
	}
	wg.Wait()
	if maxIn != 1 {
		t.Fatalf("max in flight = %d, want 1", maxIn)
	}
	prFlightMu.Lock()
	defer prFlightMu.Unlock()
	if n := len(prFlights); n != 0 {
		t.Fatalf("flights left = %d", n)
	}
}
