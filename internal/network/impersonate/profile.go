// Package impersonate provides explicitly versioned browser-like transports.
package impersonate

import (
	"fmt"
	"net/http"

	"github.com/imroc/req/v3"
	"github.com/imroc/req/v3/http2"
	utls "github.com/refraction-networking/utls"
)

const (
	Chrome133Name  = "chrome-133"
	Firefox120Name = "firefox-120"
	ReqVersion     = "v3.59.0"
	UTLSVersion    = "v1.8.2"
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
	priorityFrames    []http2.PriorityFrame
	headerPriority    *http2.PriorityParam
}

// FingerprintMetadata is a defensive, inspectable description of every
// transport dimension configured by a profile. The TLS ID refers to the exact
// immutable uTLS parrot at UTLSVersion rather than a floating "auto" alias.
type FingerprintMetadata struct {
	TLSClient         string                  `json:"tls_client"`
	TLSVersion        string                  `json:"tls_version"`
	UTLSVersion       string                  `json:"utls_version"`
	HTTP2Settings     []HTTP2SettingMetadata  `json:"http2_settings"`
	PseudoHeaderOrder []string                `json:"pseudo_header_order"`
	ConnectionFlow    uint32                  `json:"connection_flow"`
	PriorityFrames    []HTTP2PriorityMetadata `json:"priority_frames,omitempty"`
	HeaderPriority    *HTTP2PriorityMetadata  `json:"header_priority,omitempty"`
	HeaderOrder       []string                `json:"header_order"`
	Headers           http.Header             `json:"headers"`
}

type HTTP2SettingMetadata struct {
	ID    uint16 `json:"id"`
	Value uint32 `json:"value"`
}

type HTTP2PriorityMetadata struct {
	StreamID  uint32 `json:"stream_id,omitempty"`
	DependsOn uint32 `json:"depends_on"`
	Exclusive bool   `json:"exclusive"`
	Weight    uint8  `json:"weight"`
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

// firefox120 is the complete req v3.59.0 Firefox 120 transport profile. Its
// ordered SETTINGS, PRIORITY frames, header priority and ClientHello ID are
// copied from the pinned engine rather than inferred from Chrome behavior.
var firefox120 = Profile{
	Name:           Firefox120Name,
	Browser:        "Firefox",
	BrowserVersion: "120",
	Engine:         "github.com/imroc/req/v3",
	EngineVersion:  ReqVersion,
	UserAgent:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:120.0) Gecko/20100101 Firefox/120.0",
	Headers: http.Header{
		"Accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"},
		"Accept-Encoding":           {"gzip, deflate, br"},
		"Accept-Language":           {"zh-CN,zh;q=0.8,zh-TW;q=0.7,zh-HK;q=0.5,en-US;q=0.3,en;q=0.2"},
		"Sec-Fetch-Dest":            {"document"},
		"Sec-Fetch-Mode":            {"navigate"},
		"Sec-Fetch-Site":            {"same-origin"},
		"Sec-Fetch-User":            {"?1"},
		"Upgrade-Insecure-Requests": {"1"},
	},
	HeaderOrder: []string{
		"user-agent", "accept", "accept-language", "accept-encoding", "referer", "cookie",
		"upgrade-insecure-requests", "sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site",
		"sec-fetch-user", "te",
	},
	clientHelloID: utls.HelloFirefox_120,
	http2Settings: []http2.Setting{
		{ID: http2.SettingHeaderTableSize, Val: 65536},
		{ID: http2.SettingInitialWindowSize, Val: 131072},
		{ID: http2.SettingMaxFrameSize, Val: 16384},
	},
	pseudoHeaderOrder: []string{":method", ":path", ":authority", ":scheme"},
	connectionFlow:    12517377,
	priorityFrames: []http2.PriorityFrame{
		{StreamID: 3, PriorityParam: http2.PriorityParam{StreamDep: 0, Weight: 200}},
		{StreamID: 5, PriorityParam: http2.PriorityParam{StreamDep: 0, Weight: 100}},
		{StreamID: 7, PriorityParam: http2.PriorityParam{StreamDep: 0, Weight: 0}},
		{StreamID: 9, PriorityParam: http2.PriorityParam{StreamDep: 7, Weight: 0}},
		{StreamID: 11, PriorityParam: http2.PriorityParam{StreamDep: 3, Weight: 0}},
		{StreamID: 13, PriorityParam: http2.PriorityParam{StreamDep: 0, Weight: 240}},
	},
	headerPriority: &http2.PriorityParam{StreamDep: 13, Weight: 41},
}

var profiles = map[string]Profile{
	Chrome133Name:  chrome133,
	Firefox120Name: firefox120,
}

func Lookup(name string) (Profile, error) {
	profile, supported := profiles[name]
	if !supported {
		return Profile{}, fmt.Errorf("unknown impersonation profile %q", name)
	}
	return cloneProfile(profile), nil
}

func Supported() []Profile {
	names := []string{Chrome133Name, Firefox120Name}
	result := make([]Profile, 0, len(names))
	for _, name := range names {
		profile, _ := Lookup(name)
		result = append(result, profile)
	}
	return result
}

func cloneProfile(profile Profile) Profile {
	profile.Headers = profile.Headers.Clone()
	profile.HeaderOrder = append([]string(nil), profile.HeaderOrder...)
	profile.http2Settings = append([]http2.Setting(nil), profile.http2Settings...)
	profile.pseudoHeaderOrder = append([]string(nil), profile.pseudoHeaderOrder...)
	profile.priorityFrames = append([]http2.PriorityFrame(nil), profile.priorityFrames...)
	if profile.headerPriority != nil {
		priority := *profile.headerPriority
		profile.headerPriority = &priority
	}
	return profile
}

// Fingerprint returns a complete defensive snapshot suitable for audit logs
// and deterministic conformance evidence.
func (profile Profile) Fingerprint() FingerprintMetadata {
	copy := cloneProfile(profile)
	metadata := FingerprintMetadata{
		TLSClient: copy.clientHelloID.Client, TLSVersion: copy.clientHelloID.Version, UTLSVersion: UTLSVersion,
		PseudoHeaderOrder: copy.pseudoHeaderOrder, ConnectionFlow: copy.connectionFlow,
		HeaderOrder: copy.HeaderOrder, Headers: copy.Headers,
	}
	for _, setting := range copy.http2Settings {
		metadata.HTTP2Settings = append(metadata.HTTP2Settings, HTTP2SettingMetadata{ID: uint16(setting.ID), Value: setting.Val})
	}
	for _, frame := range copy.priorityFrames {
		metadata.PriorityFrames = append(metadata.PriorityFrames, priorityMetadata(frame.StreamID, frame.PriorityParam))
	}
	if copy.headerPriority != nil {
		priority := priorityMetadata(0, *copy.headerPriority)
		metadata.HeaderPriority = &priority
	}
	return metadata
}

func priorityMetadata(streamID uint32, priority http2.PriorityParam) HTTP2PriorityMetadata {
	return HTTP2PriorityMetadata{StreamID: streamID, DependsOn: priority.StreamDep, Exclusive: priority.Exclusive, Weight: priority.Weight}
}

// apply installs every profile transport dimension on the pinned req engine.
// Keeping this in profile.go makes omission of Firefox priority metadata much
// harder when callers construct a client.
func (profile Profile) apply(client *req.Client) *req.Client {
	client.SetTLSFingerprint(profile.clientHelloID).
		SetHTTP2SettingsFrame(profile.http2Settings...).
		SetHTTP2ConnectionFlow(profile.connectionFlow).
		SetCommonPseudoHeaderOder(profile.pseudoHeaderOrder...).
		SetCommonHeaderOrder(profile.HeaderOrder...)
	if len(profile.priorityFrames) != 0 {
		client.SetHTTP2PriorityFrames(profile.priorityFrames...)
	}
	if profile.headerPriority != nil {
		client.SetHTTP2HeaderPriority(*profile.headerPriority)
	}
	return client
}
