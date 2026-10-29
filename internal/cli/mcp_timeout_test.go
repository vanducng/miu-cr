package cli

import (
	stdctx "context"
	"testing"
	"time"
)

type captureMCP struct{ got MCPRequest }

func (c *captureMCP) Serve(_ stdctx.Context, req MCPRequest) error {
	c.got = req
	return nil
}

func TestMCPTimeoutInheritsReviewDefault(t *testing.T) {
	tests := []struct {
		name   string
		config string
		args   []string
		want   time.Duration
	}{
		{name: "default review timeout", want: defaultReviewTimeout},
		{name: "config review timeout", config: "[review]\ntimeout = \"10m\"\n", want: 10 * time.Minute},
		{name: "explicit flag wins", config: "[review]\ntimeout = \"10m\"\n", args: []string{"--timeout", "45s"}, want: 45 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeUserConfig(t, tt.config)
			fake := &captureMCP{}
			prev := mcpServer
			SetMCPServer(fake)
			t.Cleanup(func() { SetMCPServer(prev) })

			root := rootCommand(&options{output: "json"})
			root.SetArgs(append([]string{"mcp"}, tt.args...))
			root.SilenceUsage = true
			if err := root.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if fake.got.Timeout != tt.want {
				t.Errorf("timeout = %v, want %v", fake.got.Timeout, tt.want)
			}
		})
	}
}
