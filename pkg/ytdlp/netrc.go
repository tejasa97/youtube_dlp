package ytdlp

import (
	"context"
	"os"
	"path/filepath"

	credentialnetrc "github.com/ytdlp-go/ytdlp/internal/credentials/netrc"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
)

type netRCCredentialProvider struct {
	store *credentialnetrc.Store
}

func (netRCCredentialProvider) String() string   { return "[redacted netrc provider]" }
func (netRCCredentialProvider) GoString() string { return "ytdlp.netRCCredentialProvider{[redacted]}" }

func (provider netRCCredentialProvider) Lookup(ctx context.Context, machine string) (extractor.Credential, bool, error) {
	credential, ok, err := provider.store.Lookup(ctx, machine)
	if err != nil || !ok {
		return extractor.Credential{}, ok, err
	}
	return extractor.Credential{Username: credential.Login, Password: credential.Password}, true, nil
}

func loadNetRCCredentials(ctx context.Context, location string) (extractor.CredentialProvider, error) {
	path := location
	if path == "" || path == "~" || len(path) > 2 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, credentialnetrc.ErrIO
		}
		switch {
		case path == "":
			path = filepath.Join(home, ".netrc")
		case path == "~":
			path = home
		default:
			path = filepath.Join(home, filepath.FromSlash(path[2:]))
		}
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, ".netrc")
	}
	store, err := credentialnetrc.Load(ctx, path, credentialnetrc.Limits{})
	if err != nil {
		return nil, err
	}
	return netRCCredentialProvider{store: store}, nil
}
