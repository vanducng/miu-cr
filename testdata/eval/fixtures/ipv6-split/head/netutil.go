package netutil

import "strings"

func HasPort(addr string) bool {
	return strings.Contains(addr, ":")
}
