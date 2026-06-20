package main

import (
	"os"

	"github.com/vanducng/miu-cr/internal/cli"
	_ "github.com/vanducng/miu-cr/internal/cli/wire" // registers the engine-backed Reviewer
)

func main() {
	if err := cli.Execute(os.Args[1:]); err != nil {
		os.Exit(cli.ExitCode(err))
	}
}
