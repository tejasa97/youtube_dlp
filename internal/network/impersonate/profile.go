// Package impersonate provides explicitly versioned browser-like transports.
package impersonate

import (
	"fmt"
	"net/http"

	"github.com/imroc/req/v3/http2"
	utls "github.com/refraction-networking/utls"
)

const (
	Chrome133Name = "chrome-133"
	ReqVersion    = "v3.59.0"
)

// Profile binds one stable public name to TLS, HTTP/2, and header behavior.
// Updating the underlying fingerprint requires a new reviewed profile version.
type Profile struct {
	Name           string
	Browser        string
	BrowserVersion string
	Engine         string
	EngineVersion  string
	UserAgent      string
	Headers        http.Header
	HeaderOrder    []string

	clientHelloID     utls.ClientHelloID
	http2Settings     []http2.Setting
	pseudoHeaderOrder []string
	connectionFlow    uint32
}

var chrome133 = Profile{
	Name:           Chrome133Name,
	Browser:        "Chrome",
	BrowserVersion: "133",
	Engine:         "github.com/imroc/req/v3",
	EngineVersion:  ReqVersion,
	UserAgent:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
	Headers: http.Header{
		"Accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8"},
		"Accept-Encoding":           {"gzip, deflate, br"},
		"Accept-Language":           {"en-US,en;q=0.9"},
		"Sec-Ch-Ua":                 {`"Not(A:Brand";v="99", "Google Chrome";v="133", "Chromium";v="133"`},
		"Sec-Ch-Ua-Mobile":          {"?0"},
		"Sec-Ch-Ua-Platform":        {`"Windows"`},
		"Sec-Fetch-Dest":            {"document"},
		"Sec-Fetch-Mode":            {"navigate"},
		"Sec-Fetch-Site":            {"none"},
		"Sec-Fetch-User":            {"?1"},
		"Upgrade-Insecure-Requests": {"1"},
	},
	HeaderOrder: []string{
		"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "upgrade-insecure-requests",
		"user-agent", "accept", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-user",
		"sec-fetch-dest", "accept-encoding", "accept-language", "cookie",
	},
	clientHelloID: utls.HelloChrome_133,
	http2Settings: []http2.Setting{
		{ID: http2.SettingHeaderTableSize, Val: 65536},
		{ID: http2.SettingEnablePush, Val: 0},
		{ID: http2.SettingInitialWindowSize, Val: 6291456},
		{ID: http2.SettingMaxHeaderListSize, Val: 262144},
	},
	pseudoHeaderOrder: []string{":method", ":authority", ":scheme", ":path"},
	connectionFlow:    15663105,
}

func Lookup(name string) (Profile, error) {
	switch name {
	case Chrome133Name:
		profile := chrome133
		profile.Headers = profile.Headers.Clone()
		profile.HeaderOrder = append([]string(nil), profile.HeaderOrder...)
		profile.http2Settings = append([]http2.Setting(nil), profile.http2Settings...)
		profile.pseudoHeaderOrder = append([]string(nil), profile.pseudoHeaderOrder...)
		return profile, nil
	default:
		return Profile{}, fmt.Errorf("unknown impersonation profile %q", name)
	}
}

func Supported() []Profile {
	profile, _ := Lookup(Chrome133Name)
	return []Profile{profile}
}
