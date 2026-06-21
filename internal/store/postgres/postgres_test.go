package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/vanducng/miu-cr/internal/cli/clierr"
)

// TestOpenRedactsDSN feeds secret-bearing DSNs (incl. a url.Parse-breaking form
// and a libpq quoted-password form) through the Open error path and asserts the
// secret never survives into the typed store.unavailable message. Open against an
// unroutable host fails fast (bounded connect timeout), exercising the error path
// without a live server.
func TestOpenRedactsDSN(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
	}{
		{"url", "postgres://user:secret@127.0.0.1:1/db?sslmode=disable"},
		// url.Parse-breaking: a raw '%' / space the URL parser rejects, forcing the
		// driver-parse error path to carry the DSN text into the message.
		{"url_parse_breaking", "postgres://user:secret@127.0.0.1:1/db?bad=%zz space"},
		{"libpq_quoted", "host=127.0.0.1 port=1 user=u password='secret' dbname=db"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Open(context.Background(), tc.dsn)
			if err == nil {
				t.Fatalf("expected open failure for %q", tc.dsn)
			}
			var ce *clierr.CLIError
			if !asCLIError(err, &ce) {
				t.Fatalf("want *clierr.CLIError, got %T: %v", err, err)
			}
			if ce.Code != "store.unavailable" {
				t.Fatalf("code = %q, want store.unavailable", ce.Code)
			}
			if ce.Exit != 1 || !ce.SafeRetry {
				t.Fatalf("want Exit=1 SafeRetry=true, got Exit=%d SafeRetry=%v", ce.Exit, ce.SafeRetry)
			}
			if strings.Contains(ce.Message, "secret") {
				t.Fatalf("redaction failed, message leaks secret: %q", ce.Message)
			}
		})
	}
}

func asCLIError(err error, target **clierr.CLIError) bool {
	if ce, ok := err.(*clierr.CLIError); ok {
		*target = ce
		return true
	}
	return false
}
