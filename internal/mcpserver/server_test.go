package mcpserver_test

import (
	"testing"

	"github.com/vanducng/miu-cr/internal/engine"
	"github.com/vanducng/miu-cr/internal/engine/gitcmd"
	"github.com/vanducng/miu-cr/internal/mcpserver"
)

func TestNewBuildsServer(t *testing.T) {
	eng := engine.New(nil, gitcmd.New())
	srv, err := mcpserver.New(mcpserver.Deps{Engine: eng}, mcpserver.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if srv == nil {
		t.Fatal("New returned nil server")
	}
}

func TestNewRejectsNilEngine(t *testing.T) {
	if _, err := mcpserver.New(mcpserver.Deps{}, mcpserver.Options{}); err == nil {
		t.Fatal("New should reject a nil engine")
	}
}
