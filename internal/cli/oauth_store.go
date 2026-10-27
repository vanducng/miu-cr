package cli

import (
	stdctx "context"

	"github.com/vanducng/miu-cr/internal/config"
)

var oauthStoreFactory func(ctx stdctx.Context) (config.OAuthStore, error)

func SetOAuthStoreFactory(f func(ctx stdctx.Context) (config.OAuthStore, error)) {
	oauthStoreFactory = f
}

func openOAuthStore(ctx stdctx.Context) (config.OAuthStore, error) {
	if oauthStoreFactory == nil {
		return config.FileOAuthStore{}, nil
	}
	return oauthStoreFactory(ctx)
}

func oauthStoreFields(st config.OAuthStore, data map[string]any) map[string]any {
	data["oauth_store"] = st.Backend()
	if p := oauthFilePath(st); p != "" {
		data["oauth_path"] = p
	}
	return data
}

func oauthStoreLocation(st config.OAuthStore) string {
	if p := oauthFilePath(st); p != "" {
		return "cached at " + p
	}
	return "stored encrypted in " + st.Backend()
}

func oauthFilePath(st config.OAuthStore) string {
	if st.Backend() != config.OAuthStoreFile {
		return ""
	}
	p, _ := config.OAuthPath()
	return p
}
