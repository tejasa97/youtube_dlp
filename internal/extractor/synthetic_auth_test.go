package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/network"
)

const syntheticAuthFixtureRoot = "../../conformance/extractors/synthetic-auth"

type syntheticAuthRoundTripper struct {
	mu                sync.Mutex
	body              []byte
	status            int
	wantCookie        string
	seenCookie        string
	wantAuthorization string
	seenAuthorization string
	blockForDone      bool
}

func (roundTripper *syntheticAuthRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if roundTripper.blockForDone {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}
	roundTripper.mu.Lock()
	defer roundTripper.mu.Unlock()
	roundTripper.seenCookie = request.Header.Get("Cookie")
	roundTripper.seenAuthorization = request.Header.Get("Authorization")
	status := roundTripper.status
	if status == 0 {
		status = http.StatusOK
	}
	if roundTripper.wantCookie != "" && roundTripper.seenCookie != roundTripper.wantCookie {
		status = http.StatusUnauthorized
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(roundTripper.body)),
		Request:    request,
	}, nil
}

type syntheticCredentialProvider struct {
	machine, username, password string
}

func (provider syntheticCredentialProvider) Lookup(_ context.Context, machine string) (Credential, bool, error) {
	if machine != provider.machine {
		return Credential{}, false, nil
	}
	return Credential{Username: provider.username, Password: provider.password}, true, nil
}

func TestSyntheticAuthUsesExtractorScopedCredentials(t *testing.T) {
	const username, password = "fixture-user", "basic-secret-never-export"
	roundTripper := &syntheticAuthRoundTripper{body: syntheticAuthFixture(t, "success.json")}
	client := syntheticAuthClient(t, roundTripper)
	result, err := NewSyntheticAuth().Extract(context.Background(), Request{
		URL: "https://auth-fixture.invalid/watch/auth-001", Transport: client,
		Credentials: syntheticCredentialProvider{machine: "auth-fixture.invalid", username: username, password: password},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := result.Info.ID(); id != "auth-001" {
		t.Fatalf("id = %q", id)
	}
	roundTripper.mu.Lock()
	authorization := roundTripper.seenAuthorization
	roundTripper.mu.Unlock()
	request, _ := http.NewRequest(http.MethodGet, "https://auth-fixture.invalid", nil)
	request.SetBasicAuth(username, password)
	if authorization != request.Header.Get("Authorization") {
		t.Fatalf("Authorization was not the scoped Basic credential")
	}
	serialized, _ := json.Marshal(result.Info.Fields())
	if strings.Contains(string(serialized), username) || strings.Contains(string(serialized), password) {
		t.Fatalf("metadata retained credential: %s", serialized)
	}
}

func syntheticAuthFixture(t testing.TB, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(syntheticAuthFixtureRoot, name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func syntheticAuthClient(t testing.TB, roundTripper http.RoundTripper, cookies ...*http.Cookie) *network.Client {
	t.Helper()
	client, err := network.New(network.Config{RoundTripper: roundTripper})
	if err != nil {
		t.Fatal(err)
	}
	if len(cookies) > 0 {
		if err := client.AddCookies(cookies); err != nil {
			t.Fatal(err)
		}
	}
	return client
}

func TestSyntheticAuthSuitable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		rawURL string
		want   bool
	}{
		{"https://auth-fixture.invalid/watch/auth-001", true},
		{"https://auth-fixture.invalid/watch/id_with-dash/", true},
		{"http://auth-fixture.invalid/watch/auth-001", false},
		{"https://sub.auth-fixture.invalid/watch/auth-001", false},
		{"https://auth-fixture.invalid/api/media/auth-001", false},
		{"https://auth-fixture.invalid/watch/a/b", false},
	}
	for _, test := range tests {
		parsed, err := url.Parse(test.rawURL)
		if err != nil {
			t.Fatal(err)
		}
		if got := NewSyntheticAuth().Suitable(parsed); got != test.want {
			t.Errorf("Suitable(%q) = %t, want %t", test.rawURL, got, test.want)
		}
	}
}

func TestSyntheticAuthUsesScopedOperationJarAndDropsSecret(t *testing.T) {
	const secret = "operation-secret-never-export"
	roundTripper := &syntheticAuthRoundTripper{
		body:       syntheticAuthFixture(t, "success.json"),
		wantCookie: "operation_session=" + secret,
	}
	client := syntheticAuthClient(t, roundTripper,
		&http.Cookie{Name: "operation_session", Value: secret, Domain: "auth-fixture.invalid", Path: "/api/media/", Secure: true, HttpOnly: true},
		&http.Cookie{Name: "wrong_path", Value: "must-not-send", Domain: "auth-fixture.invalid", Path: "/account", Secure: true},
		&http.Cookie{Name: "wrong_origin", Value: "must-not-send", Domain: "other.invalid", Path: "/", Secure: true},
	)
	result, err := NewSyntheticAuth().Extract(context.Background(), Request{
		URL: "https://auth-fixture.invalid/watch/auth-001", Transport: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := result.Info.ID(); got != "auth-001" {
		t.Fatalf("id = %q", got)
	}
	if got, _ := result.Info.Title(); got != "Authenticated conformance media" {
		t.Fatalf("title = %q", got)
	}
	if duration, ok := result.Info.Lookup("duration").Int(); !ok || duration != 42 {
		t.Fatalf("duration = %d, %t", duration, ok)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) != 1 {
		t.Fatalf("formats = %#v", formats)
	}
	serialized, err := json.Marshal(result.Info.Fields())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), secret) || strings.Contains(string(serialized), "must-not-send") {
		t.Fatalf("normalized metadata retained authentication state: %s", serialized)
	}
	roundTripper.mu.Lock()
	seenCookie := roundTripper.seenCookie
	roundTripper.mu.Unlock()
	if seenCookie != "operation_session="+secret {
		t.Fatalf("protected request Cookie = %q", seenCookie)
	}
}

func TestSyntheticAuthCategorizesAuthenticationAndAvailability(t *testing.T) {
	tests := []struct {
		name       string
		body       []byte
		status     int
		cookie     *http.Cookie
		wantCookie string
		want       error
	}{
		{name: "missing cookie", body: syntheticAuthFixture(t, "success.json"), wantCookie: "operation_session=valid", want: ErrAuthentication},
		{name: "invalid cookie", body: syntheticAuthFixture(t, "success.json"), status: http.StatusForbidden, cookie: &http.Cookie{Name: "operation_session", Value: "invalid", Domain: "auth-fixture.invalid", Path: "/", Secure: true}, want: ErrAuthentication},
		{name: "unauthorized response", body: []byte(`{"session":{"authenticated":false}}`), want: ErrAuthentication},
		{name: "unavailable response", body: syntheticAuthFixture(t, "unavailable.json"), cookie: &http.Cookie{Name: "operation_session", Value: "valid", Domain: "auth-fixture.invalid", Path: "/", Secure: true}, want: ErrUnavailable},
		{name: "gone status", status: http.StatusGone, cookie: &http.Cookie{Name: "operation_session", Value: "valid", Domain: "auth-fixture.invalid", Path: "/", Secure: true}, want: ErrUnavailable},
		{name: "malformed metadata", body: []byte(`{"session":{"authenticated":true},"media":{"id":"wrong"}}`), cookie: &http.Cookie{Name: "operation_session", Value: "valid", Domain: "auth-fixture.invalid", Path: "/", Secure: true}, want: ErrInvalidMetadata},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			roundTripper := &syntheticAuthRoundTripper{body: test.body, status: test.status, wantCookie: test.wantCookie}
			var cookies []*http.Cookie
			if test.cookie != nil {
				cookies = append(cookies, test.cookie)
			}
			client := syntheticAuthClient(t, roundTripper, cookies...)
			_, err := NewSyntheticAuth().Extract(context.Background(), Request{URL: "https://auth-fixture.invalid/watch/auth-001", Transport: client})
			if !errors.Is(err, test.want) {
				t.Fatalf("Extract() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestSyntheticAuthErrorsDoNotExposeCookieOrResponse(t *testing.T) {
	const secret = "secret-in-cookie-and-body"
	roundTripper := &syntheticAuthRoundTripper{body: []byte(`{"secret":"` + secret + `"} trailing`)}
	client := syntheticAuthClient(t, roundTripper,
		&http.Cookie{Name: "operation_session", Value: secret, Domain: "auth-fixture.invalid", Path: "/", Secure: true})
	_, err := NewSyntheticAuth().Extract(context.Background(), Request{URL: "https://auth-fixture.invalid/watch/auth-001", Transport: client})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("Extract() error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed authentication value: %v", err)
	}
}

func TestSyntheticAuthHonorsCancellation(t *testing.T) {
	client := syntheticAuthClient(t, &syntheticAuthRoundTripper{blockForDone: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewSyntheticAuth().Extract(ctx, Request{URL: "https://auth-fixture.invalid/watch/auth-001", Transport: client})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Extract() error = %v, want context.Canceled", err)
	}
}

func FuzzSyntheticAuthResponse(f *testing.F) {
	f.Add(syntheticAuthFixture(f, "success.json"), "auth-001")
	f.Add([]byte(`{"session":{"authenticated":false}}`), "auth-001")
	f.Add([]byte(`{`), "auth-001")
	f.Fuzz(func(t *testing.T, body []byte, requestedID string) {
		if len(body) > 1<<20 || len(requestedID) > 4096 {
			t.Skip()
		}
		var response syntheticAuthResponse
		if json.Unmarshal(body, &response) != nil {
			return
		}
		_, _ = normalizeSyntheticAuth(response, requestedID)
	})
}
