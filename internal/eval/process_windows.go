//go:build windows

package eval

import (
	"os/exec"
	"strconv"
)

func prepareCommand(*exec.Cmd) {}

func killCommand(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}
