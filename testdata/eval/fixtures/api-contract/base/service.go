package service

import "context"

type Client interface {
	Do(context.Context) error
}

type runner struct {
	client Client
}

func (r runner) Run(ctx context.Context) error {
	return r.client.Do(ctx)
}
