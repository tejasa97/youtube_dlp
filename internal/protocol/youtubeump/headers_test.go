package youtubeump

import (
	"context"
	"net/http"
	"testing"
)

func TestSABRCallerHeadersCannotOverrideProtectedValues(t *testing.T) {
	request, err := newSABRRequest(context.Background(),
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr?sig=fixture", 0,
		[]byte("body"), "identity-agent", "en-US")
	if err != nil {
		t.Fatal(err)
	}
	caller := make(http.Header)
	caller.Set("Content-Type", "text/plain")
	caller.Set("Accept", "text/html")
	caller.Set("Accept-Encoding", "gzip")
	caller.Set("User-Agent", "evil-agent")
	caller.Set("Cookie", "secret=1")
	caller.Set("Authorization", "secret")
	caller.Set("X-Custom", "safe")
	applySABRCallerHeaders(request, caller)
	if request.Header.Get("Content-Type") != "application/x-protobuf" ||
		request.Header.Get("Accept") != "application/vnd.yt-ump" ||
		request.Header.Get("Accept-Encoding") != "identity" ||
		request.Header.Get("User-Agent") != "identity-agent" ||
		request.Header.Get("Cookie") != "" ||
		request.Header.Get("Authorization") != "" {
		t.Fatalf("headers=%#v", request.Header)
	}
	if request.Header.Get("X-Custom") != "safe" {
		t.Fatal("safe caller header dropped")
	}
}

func TestSABRCallerHeadersSurviveWhenUnset(t *testing.T) {
	request, err := newSABRRequest(context.Background(),
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr?sig=fixture", 0,
		[]byte("body"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	caller := make(http.Header)
	caller.Set("Referer", "https://www.youtube.com/watch?v=fixture0001")
	applySABRCallerHeaders(request, caller)
	if request.Header.Get("Referer") != "https://www.youtube.com/watch?v=fixture0001" {
		t.Fatalf("headers=%#v", request.Header)
	}
}
