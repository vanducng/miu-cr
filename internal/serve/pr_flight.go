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
		prFlightMu.Lock()
		f.n--
		if f.n == 0 && prFlights[key] == f {
			delete(prFlights, key)
		}
		prFlightMu.Unlock()
	}()
	return fn()
}
