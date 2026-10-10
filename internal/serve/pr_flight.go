package serve

import "sync"

type prFlight struct {
	mu sync.Mutex
	n  int
}

var (
	prFlightMu sync.Mutex
	prFlights  = map[string]*prFlight{}
)

// TryWithPRFlight runs fn when the pull request lock is free. A busy lock returns false
// so a poll tick can skip the work instead of waiting out a publish.
func TryWithPRFlight(key string, fn func() error) (bool, error) {
	prFlightMu.Lock()
	f := prFlights[key]
	if f == nil {
		f = &prFlight{}
		prFlights[key] = f
	}
	f.n++
	prFlightMu.Unlock()

	if !f.mu.TryLock() {
		releasePRFlight(key, f)
		return false, nil
	}
	defer func() {
		f.mu.Unlock()
		releasePRFlight(key, f)
	}()
	return true, fn()
}

func releasePRFlight(key string, f *prFlight) {
	prFlightMu.Lock()
	f.n--
	if f.n == 0 && prFlights[key] == f {
		delete(prFlights, key)
	}
	prFlightMu.Unlock()
}

// WithPRFlight serializes summary writers for one pull request so a reply cannot be reverted.
func WithPRFlight(key string, fn func() error) error {
	prFlightMu.Lock()
	f := prFlights[key]
	if f == nil {
		f = &prFlight{}
		prFlights[key] = f
	}
	f.n++
	prFlightMu.Unlock()

	f.mu.Lock()
	defer func() {
		f.mu.Unlock()
		releasePRFlight(key, f)
	}()
	return fn()
}
