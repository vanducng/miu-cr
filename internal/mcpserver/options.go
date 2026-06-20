package mcpserver

import "time"

const TransportStdio = "stdio"

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
