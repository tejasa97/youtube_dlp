package extractor

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestAnvatoSuitableAndSuccess(t *testing.T) {
	t.Parallel()
	accessKey := fox9AnvatoAccessKey
	videoID := "8032455"
	rawURL := "anvato:" + accessKey + ":" + videoID
	parsed, err := url.Parse(rawURL)
	if err != nil || !NewAnvato().Suitable(parsed) {
		t.Fatalf("Suitable failed: %v", err)
	}
	serverTime := int64(1700000000)
	serverEndpoint := anvatoAPIBase + "/server_time?anvack=" + url.QueryEscape(accessKey)
	videoBase := anvatoAPIBase + "/mcp/video/" + videoID + "?anvack=" + url.QueryEscape(accessKey)
	input := fmt.Sprintf("%d~%x~%x", serverTime, md5.Sum([]byte(videoBase)), md5.Sum([]byte(strconv.FormatInt(serverTime, 10))))
	if len(input) > 64 {
		input = input[:64]
	}
	auth := make([]byte, len(anvatoAuthKey))
	for i := range anvatoAuthKey {
		auth[i] = input[i] ^ anvatoAuthKey[i]
	}
	query := url.Values{}
	query.Set("anvack", accessKey)
	query.Set("X-Anvato-Adst-Auth", base64.StdEncoding.EncodeToString(auth))
	query.Set("rtyp", "fp")
	videoEndpoint := anvatoAPIBase + "/mcp/video/" + videoID + "?" + query.Encode()
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		serverEndpoint: {body: []byte(`{"server_time":1700000000}`)},
		videoEndpoint:  {body: familyFixture(t, "anvato", "video.json")},
	}}
	result, err := NewAnvato().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	id, ok := result.Info.ID()
	if !ok || id != videoID {
		t.Fatalf("id=%q ok=%t", id, ok)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) == 0 {
		t.Fatal("missing formats")
	}
}

func TestAnvatoErrorsAndFOX9(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewAnvato().Extract(canceled, Request{
		URL: "anvato:" + fox9AnvatoAccessKey + ":8032455", Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	result, err := NewFOX9().Extract(context.Background(), Request{URL: "https://www.fox9.com/video/314473"})
	if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "anvato" {
		t.Fatalf("fox9=%#v err=%v", result, err)
	}
	transport := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.fox9.com/news/bear-climbs-tree": familyFixture(t, "fox9", "news.html"),
	}}
	news, err := NewFOX9News().Extract(context.Background(), Request{
		URL: "https://www.fox9.com/news/bear-climbs-tree", Transport: transport,
	})
	if err != nil || !news.IsURL() || news.Redirect.ExtractorKey != "fox9" || news.Redirect.ID != "314473" {
		t.Fatalf("fox9_news=%#v err=%v", news, err)
	}
	authTransport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		anvatoAPIBase + "/server_time?anvack=" + url.QueryEscape(fox9AnvatoAccessKey): {body: []byte(`{"server_time":1700000000}`)},
	}}
	// Force video request miss -> unexpected fixture request error path is fine; instead test status helper.
	if err := hostedStatusError(http.StatusForbidden, []byte(`token=must-not-leak`)); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("secret-safe=%v", err)
	}
	_ = authTransport
	for _, bad := range []string{
		"https://fox9.com/",
		"https://evil.example/video/314473",
		"anvato:bad:id",
	} {
		parsed, err := url.Parse(bad)
		if err != nil {
			t.Fatal(err)
		}
		if NewFOX9().Suitable(parsed) || NewAnvato().Suitable(parsed) {
			t.Fatalf("unexpected Suitable(%q)", bad)
		}
	}
}

func FuzzParseAnvatoURL(f *testing.F) {
	f.Add("anvato:" + fox9AnvatoAccessKey + ":8032455")
	f.Add("anvato:lin:123")
	f.Add("nope")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > sharedHostingMaxURLBytes {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		_, _ = parseAnvatoURL(parsed)
		_, _ = parseFOX9URL(parsed)
		_, _ = parseFOX9NewsURL(parsed)
	})
}
