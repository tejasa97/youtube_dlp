package ejs

import (
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/sha3"
)

const (
	Version = "0.8.0"

	coreSHA3 = "ee5b307d07f55e91e4723edf5ac205cc877a474187849d757dc1322e38427b157a9d706d510c1723d3670f98e5a3f8cbcde77874a80406bd7204bc9fea30f283"
	libSHA3  = "8420c259ad16e99ce004e4651ac1bcabb53b4457bf5668a97a9359be9a998a789fee8ab124ee17f91a2ea8fd84e0f2b2fc8eabcaf0b16a186ba734cf422ad053"
)

//go:embed assets/yt.solver.core.min.js
var coreScript string

//go:embed assets/yt.solver.lib.min.js
var libraryScript string

var (
	verifyOnce sync.Once
	verifyErr  error
)

// VerifyAssets checks the exact SHA3-512 allowlist published by the pinned
// yt-dlp reference for yt-dlp-ejs 0.8.0.
func VerifyAssets() error {
	verifyOnce.Do(func() {
		for name, asset := range map[string]struct{ source, expected string }{
			"core": {canonicalEmbeddedScript(coreScript), coreSHA3},
			"lib":  {canonicalEmbeddedScript(libraryScript), libSHA3},
		} {
			digest := sha3.Sum512([]byte(asset.source))
			if hex.EncodeToString(digest[:]) != asset.expected {
				verifyErr = fmt.Errorf("EJS %s asset hash mismatch", name)
				return
			}
		}
	})
	return verifyErr
}

func bundledScript() (string, error) {
	if err := VerifyAssets(); err != nil {
		return "", err
	}
	return canonicalEmbeddedScript(libraryScript) + "\nObject.assign(globalThis, lib);\n" + canonicalEmbeddedScript(coreScript), nil
}

// canonicalEmbeddedScript reverses Git's Windows checkout conversion before
// checking the upstream-published digest or executing the bundled source.
// Only CRLF pairs are canonicalized; arbitrary carriage returns remain part
// of the authenticated input and therefore cannot bypass the allowlist.
func canonicalEmbeddedScript(source string) string {
	return strings.ReplaceAll(source, "\r\n", "\n")
}
