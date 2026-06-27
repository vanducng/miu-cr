package netutil

import "net"

func HasPort(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	return err == nil && host != "" && port != ""
}
