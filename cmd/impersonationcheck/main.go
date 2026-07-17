// Command impersonationcheck runs the separately controlled live browser
// fingerprint canary. It is intentionally not part of deterministic CI.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/network/impersonate"
)

const defaultCanaryURL = "https://tls.peet.ws/api/all"

type report struct {
	Profile              string `json:"profile"`
	Browser              string `json:"browser"`
	Engine               string `json:"engine"`
	EngineVersion        string `json:"engine_version"`
	URL                  string `json:"url"`
	Status               int    `json:"status"`
	Protocol             string `json:"protocol"`
	BodyBytes            int    `json:"body_bytes"`
	BodySHA256           string `json:"body_sha256"`
	ObservedUserAgent    string `json:"observed_user_agent,omitempty"`
	JA3Hash              string `json:"ja3_hash,omitempty"`
	PeetPrintHash        string `json:"peetprint_hash,omitempty"`
	HTTP2FingerprintHash string `json:"http2_fingerprint_hash,omitempty"`
}

func main() {
	flags := flag.NewFlagSet("impersonationcheck", flag.ExitOnError)
	profileName := flags.String("profile", impersonate.Chrome133Name, "named impersonation profile")
	target := flags.String("url", defaultCanaryURL, "controlled fingerprint canary URL")
	timeout := flags.Duration("timeout", 20*time.Second, "request deadline")
	maxBody := flags.Int64("max-body", 2<<20, "maximum response bytes")
	_ = flags.Parse(os.Args[1:])

	if *maxBody <= 0 || *maxBody > 16<<20 {
		fmt.Fprintln(os.Stderr, "impersonationcheck: max-body must be between 1 and 16777216")
		os.Exit(2)
	}
	profile, err := impersonate.Lookup(*profileName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "impersonationcheck: unknown profile")
		os.Exit(2)
	}
	client, err := network.New(network.Config{Timeout: *timeout, MaxPageSize: *maxBody})
	if err != nil {
		fmt.Fprintln(os.Stderr, "impersonationcheck: configure transport failed")
		os.Exit(1)
	}
	request, err := http.NewRequest(http.MethodGet, *target, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "impersonationcheck: invalid URL")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	response, err := client.DoProfile(ctx, request, *profileName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "impersonationcheck: request failed")
		os.Exit(1)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, *maxBody+1))
	if err != nil || int64(len(body)) > *maxBody {
		fmt.Fprintln(os.Stderr, "impersonationcheck: bounded response read failed")
		os.Exit(1)
	}
	result := summarize(profile, *target, response, body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		fmt.Fprintln(os.Stderr, "impersonationcheck: canary returned non-success status")
		os.Exit(1)
	}
	if *target == defaultCanaryURL && (result.JA3Hash == "" || result.HTTP2FingerprintHash == "") {
		fmt.Fprintln(os.Stderr, "impersonationcheck: canary omitted fingerprint evidence")
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "impersonationcheck: encode report failed")
		os.Exit(1)
	}
}

func summarize(profile impersonate.Profile, rawURL string, response *http.Response, body []byte) report {
	digest := sha256.Sum256(body)
	displayURL := "<invalid>"
	if parsed, err := url.Parse(rawURL); err == nil {
		displayURL = network.RedactURL(parsed)
	}
	result := report{
		Profile: profile.Name, Browser: profile.Browser + "/" + profile.BrowserVersion,
		Engine: profile.Engine, EngineVersion: profile.EngineVersion, URL: displayURL,
		Status: response.StatusCode, Protocol: response.Proto, BodyBytes: len(body),
		BodySHA256: hex.EncodeToString(digest[:]),
	}
	var observation struct {
		UserAgent string `json:"user_agent"`
		TLS       struct {
			JA3Hash       string `json:"ja3_hash"`
			PeetPrintHash string `json:"peetprint_hash"`
		} `json:"tls"`
		HTTP2 struct {
			AkamaiFingerprintHash string `json:"akamai_fingerprint_hash"`
		} `json:"http2"`
	}
	if json.Unmarshal(body, &observation) == nil {
		result.ObservedUserAgent = observation.UserAgent
		result.JA3Hash = observation.TLS.JA3Hash
		result.PeetPrintHash = observation.TLS.PeetPrintHash
		result.HTTP2FingerprintHash = observation.HTTP2.AkamaiFingerprintHash
	}
	return result
}
