//go:build windows

package eval

import "os/exec"

func prepareCommand(*exec.Cmd) {}

func killCommand(*exec.Cmd) {}
