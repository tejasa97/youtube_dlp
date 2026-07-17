package main

import (
	"net/http"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/network/impersonate"
)

func TestSummarizeExtractsBoundedFingerprintEvidence(t *testing.T) {
	profile, _ := impersonate.Lookup(impersonate.Chrome133Name)
	body := []byte(`{"user_agent":"Chrome/133","tls":{"ja3_hash":"ja3","peetprint_hash":"peet"},"http2":{"akamai_fingerprint_hash":"h2"}}`)
	result := summarize(profile, "https://example.test?token=secret", &http.Response{StatusCode: 200, Proto: "HTTP/2.0"}, body)
	if result.Profile != impersonate.Chrome133Name || result.JA3Hash != "ja3" || result.PeetPrintHash != "peet" || result.HTTP2FingerprintHash != "h2" || result.BodySHA256 == "" {
		t.Fatalf("report = %#v", result)
	}
	if result.URL != "https://example.test?token=REDACTED" {
		t.Fatalf("report URL = %q", result.URL)
	}
}
