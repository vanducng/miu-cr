package mcpserver

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// eofGrace lets an in-flight response flush when the peer closes stdin in the
// same read as it sent its request (one-shot pipes); real hosts keep the stream
// open and never reach this path.
const eofGrace = 150 * time.Millisecond

// UnsupportedTransportError reports a requested transport this server cannot
// serve (only stdio is supported).
type UnsupportedTransportError struct {
	Transport string
}

func (e *UnsupportedTransportError) Error() string {
	return fmt.Sprintf("unsupported MCP transport %q", e.Transport)
}

func transportFor(name string, stdin io.Reader, stdout io.Writer) (mcp.Transport, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", TransportStdio:
		return &mcp.IOTransport{
			Reader: &graceReadCloser{Reader: stdin},
			Writer: flushWriteCloser{Writer: stdout},
		}, nil
	case "http", "streamable-http":
		return nil, &UnsupportedTransportError{Transport: name}
	default:
		return nil, &UnsupportedTransportError{Transport: name}
	}
}

type flusher interface{ Sync() error }

type flushWriteCloser struct {
	io.Writer
}

// Write flushes after each frame so a queued response reaches the OS pipe before
// process teardown when the peer closes stdin immediately after one request.
func (w flushWriteCloser) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if err == nil {
		if f, ok := w.Writer.(flusher); ok {
			_ = f.Sync()
		}
	}
	return n, err
}

func (flushWriteCloser) Close() error { return nil }

// graceReadCloser delays the first EOF by eofGrace so an in-flight response can
// flush when the peer closed stdin in the same read it sent its request.
type graceReadCloser struct {
	io.Reader
	delayed bool
}

func (r *graceReadCloser) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF && n == 0 && !r.delayed {
		r.delayed = true
		time.Sleep(eofGrace)
	}
	return n, err
}

func (r *graceReadCloser) Close() error { return nil }
