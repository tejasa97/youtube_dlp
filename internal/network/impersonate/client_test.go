package impersonate

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"reflect"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

func TestProfileMetadataAndCopiesAreStable(t *testing.T) {
	profile, err := Lookup(Chrome133Name)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Browser != "Chrome" || profile.BrowserVersion != "133" || profile.EngineVersion != TLSClientVersion {
		t.Fatalf("profile = %#v", profile)
	}
	profile.Headers.Set("User-Agent", "mutated")
	profile.HeaderOrder[0] = "mutated"
	again, _ := Lookup(Chrome133Name)
	if again.Headers.Get("User-Agent") == "mutated" || again.HeaderOrder[0] == "mutated" {
		t.Fatal("Lookup returned shared mutable profile state")
	}
	if _, err := Lookup("unknown"); err == nil {
		t.Fatal("unknown profile succeeded")
	}
}

func TestRequestAdapterPreservesSemanticsAndAppliesProfile(t *testing.T) {
	profile, _ := Lookup(Chrome133Name)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/path", bytes.NewBufferString("body"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept-Language", "fr")
	request.Header.Set("X-Custom", "present")
	request.Host = "override.example.test"
	converted, err := toFingerprintRequest(request, profile)
	if err != nil {
		t.Fatal(err)
	}
	if converted.Context() != ctx || converted.Method != request.Method || converted.Host != request.Host {
		t.Fatalf("converted request = %#v", converted)
	}
	if converted.Header.Get("Accept-Language") != "fr" || converted.Header.Get("X-Custom") != "present" {
		t.Fatalf("headers = %#v", converted.Header)
	}
	if converted.Header.Get("User-Agent") != profile.UserAgent || !reflect.DeepEqual(converted.Header[fhttp.HeaderOrderKey], profile.HeaderOrder) {
		t.Fatalf("profile headers = %#v", converted.Header)
	}
	payload, err := io.ReadAll(converted.Body)
	if err != nil || string(payload) != "body" {
		t.Fatalf("body = %q, %v", payload, err)
	}
}

func TestCookieJarAdapterRoundTripsAllSupportedFields(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	adapter := cookieJarAdapter{jar: jar}
	target := mustURL(t, "https://example.test/path")
	want := &fhttp.Cookie{
		Name: "session", Value: "secret", Path: "/", Domain: "example.test",
		Expires: time.Now().Add(time.Hour).Truncate(time.Second), MaxAge: 60,
		Secure: true, HttpOnly: true, SameSite: fhttp.SameSiteLaxMode,
	}
	adapter.SetCookies(target, []*fhttp.Cookie{want})
	cookies := adapter.Cookies(target)
	if len(cookies) != 1 {
		t.Fatalf("cookies = %#v", cookies)
	}
	got := cookies[0]
	if got.Name != want.Name || got.Value != want.Value {
		t.Fatalf("cookie = %#v, want %#v", got, want)
	}
	roundTrip := toFingerprintCookie(toStandardCookie(want))
	if roundTrip.Path != want.Path || roundTrip.Domain != want.Domain || !roundTrip.Expires.Equal(want.Expires) ||
		roundTrip.MaxAge != want.MaxAge || roundTrip.Secure != want.Secure || roundTrip.HttpOnly != want.HttpOnly || roundTrip.SameSite != want.SameSite {
		t.Fatalf("cookie conversion = %#v, want %#v", roundTrip, want)
	}
}

func mustURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
