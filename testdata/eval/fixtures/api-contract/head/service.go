package service

import "context"

type Options struct {
	DryRun bool
}

type Client interface {
	Do(context.Context, Options) error
}

type runner struct {
	client Client
}

func (r runner) Run(ctx context.Context) error {
	return r.client.Do(ctx)
}
