package wire

import (
	stdctx "context"
	"errors"
	"strings"

	"github.com/vanducng/miu-cr/internal/cli"
	"github.com/vanducng/miu-cr/internal/config"
	mgithub "github.com/vanducng/miu-cr/internal/github"
)

func upsertReviewErrorSummary(ctx stdctx.Context, client mgithub.Client, info *mgithub.PRInfo, reviewErr error) error {
	cctx, cancel := stdctx.WithTimeout(stdctx.WithoutCancel(ctx), reviewErrorSummaryTimeout)
	defer cancel()
	_, err := mgithub.UpsertSummaryComment(cctx, client, info, mgithub.RenderErrorNotice(info, reviewErrorNotice(reviewErr), cli.Version()))
	return err
}

func reviewErrorNotice(err error) mgithub.ErrorNotice {
	notice := mgithub.ErrorNotice{
		Level:   "caution",
		Title:   "miucr hit an internal error",
		Message: config.RedactString(err.Error()),
	}
	var ce *cli.CLIError
	if errors.As(err, &ce) {
		notice.Code = ce.Code
		notice.Message = config.RedactString(ce.Message)
		notice.Hint = config.RedactString(ce.Hint)
		if reviewErrorIsOperational(ce.Code, ce.Retry) {
			notice.Level = "warning"
			notice.Title = reviewErrorTitle(ce.Code)
		}
	} else if reviewErrorTextLooksOperational(err.Error()) {
		notice.Level = "warning"
		notice.Title = "Operational review issue"
	}
	return notice
}

func reviewErrorIsOperational(code string, retryable bool) bool {
	switch strings.TrimSpace(code) {
	case "agent.auth_failed",
		"agent.auth_expired",
		"agent.auth_command_failed",
		"agent.unavailable",
		"provider.rate_limited",
		"quota.exceeded",
		"review.timeout",
		"review.stalled",
		"review.canceled",
		"github.rate_limited",
		"github.unavailable",
		"store.unavailable":
		return true
	}
	return retryable && (strings.HasPrefix(code, "agent.") || strings.HasPrefix(code, "provider.") || strings.HasPrefix(code, "github."))
}

func reviewErrorTitle(code string) string {
	switch strings.TrimSpace(code) {
	case "agent.auth_failed", "agent.auth_expired", "agent.auth_command_failed":
		return "Provider authentication issue"
	case "agent.unavailable":
		return "Provider unavailable"
	case "provider.rate_limited":
		return "Provider rate limit"
	case "quota.exceeded":
		return "Provider quota reached"
	case "review.timeout":
		return "Review timed out"
	case "review.stalled":
		return "Review stalled"
	case "review.canceled":
		return "Review canceled"
	case "github.rate_limited":
		return "GitHub rate limit"
	case "github.unavailable":
		return "GitHub unavailable"
	case "store.unavailable":
		return "Review store unavailable"
	default:
		return "Operational review issue"
	}
}

func reviewErrorTextLooksOperational(msg string) bool {
	s := strings.ToLower(msg)
	for _, needle := range []string{
		"timeout",
		"deadline exceeded",
		"rate limit",
		"429",
		"quota",
		"overload",
		"529",
		"temporarily unavailable",
		"connection reset",
		"connection refused",
		"network",
		"tls handshake timeout",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
