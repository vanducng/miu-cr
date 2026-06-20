package mcpserver

import "time"

// TransportStdio is the only supported (and default) MCP transport.
const TransportStdio = "stdio"

// Options configures the MCP server: transport, implementation identity, the
// per-tool output byte cap, and the per-call timeout.
type Options struct {
	Transport             string
	ImplementationName    string
	ImplementationVersion string
	MaxBytes              int
	Timeout               time.Duration
}

func (o Options) withDefaults() Options {
	if o.Transport == "" {
		o.Transport = TransportStdio
	}
	if o.ImplementationName == "" {
		o.ImplementationName = "miucr"
	}
	if o.ImplementationVersion == "" {
		o.ImplementationVersion = "dev"
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 1 << 20
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	return o
}
