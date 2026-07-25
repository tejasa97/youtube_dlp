package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestPanoptoFamilySuccess(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(NewPanopto(), NewPanoptoPlaylist())
	vid := "26b3ae9e-4a48-4dcc-96ba-0befba08a0fb"
	host := "demo.hosted.panopto.com"
	t.Run("panopto", func(t *testing.T) {
		endpoint := "https://" + host + "/Panopto/Pages/Viewer/DeliveryInfo.aspx?deliveryId=" + vid + "&responseType=json"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			endpoint: {body: familyFixture(t, "panopto", "deliveryinfo.json")},
		}}
		result, err := NewPanopto().Extract(context.Background(), Request{
			URL: "https://" + host + "/Panopto/Pages/Viewer.aspx?id=" + vid, Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	t.Run("panopto_playlist", func(t *testing.T) {
		pid := "f3b39fcf-882f-4849-93d6-a9f401236d36"
		slist := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://" + host + "/Panopto/Api/Playlists/" + pid: {body: familyFixture(t, "panopto_playlist", "playlist.json")},
			"https://" + host + "/Panopto/Api/SessionLists/" + slist + "?collections[0].maxCount=500&collections[0].name=items": {
				body: familyFixture(t, "panopto_playlist", "sessionlist.json"),
			},
		}}
		result, err := NewPanoptoPlaylist().Extract(context.Background(), Request{
			URL: "https://" + host + "/Panopto/Pages/Viewer.aspx?pid=" + pid, Transport: transport,
		})
		if err != nil || !result.IsPlaylist() {
			t.Fatalf("%#v %v", result, err)
		}
		if transport.requestCount() != 0 {
			t.Fatalf("lazy fetch before iterate: %d", transport.requestCount())
		}
		entries, err := CollectEntries(context.Background(), result.Entries, panoptoMaxEntries)
		if err != nil || len(entries) != 2 || entries[0].ExtractorKey != "panopto" {
			t.Fatalf("entries=%v err=%v", entries, err)
		}
	})
	for _, test := range []struct {
		rawURL, want string
	}{
		{"https://demo.hosted.panopto.com/Panopto/Pages/Viewer.aspx?id=" + vid, "panopto"},
		{"https://demo.hosted.panopto.com/Panopto/Pages/Viewer.aspx?pid=f3b39fcf-882f-4849-93d6-a9f401236d36", "panopto_playlist"},
	} {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q)=%v err=%v want %q", test.rawURL, selected, err, test.want)
		}
	}
}

func TestPanoptoFamilyNegatives(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewPanopto().Extract(canceled, Request{
		URL:       "https://demo.hosted.panopto.com/Panopto/Pages/Viewer.aspx?id=26b3ae9e-4a48-4dcc-96ba-0befba08a0fb",
		Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	auth := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://demo.hosted.panopto.com/Panopto/Pages/Viewer/DeliveryInfo.aspx?deliveryId=26b3ae9e-4a48-4dcc-96ba-0befba08a0fb&responseType=json": {
			status: http.StatusUnauthorized, body: []byte("token=must-not-leak"),
		},
	}}
	if _, err := NewPanopto().Extract(context.Background(), Request{
		URL: "https://demo.hosted.panopto.com/Panopto/Pages/Viewer.aspx?id=26b3ae9e-4a48-4dcc-96ba-0befba08a0fb", Transport: auth,
	}); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("auth=%v", err)
	}
	parsed, _ := url.Parse("https://evilpanopto.com/Panopto/Pages/Viewer.aspx?id=26b3ae9e-4a48-4dcc-96ba-0befba08a0fb")
	if NewPanopto().Suitable(parsed) {
		t.Fatal("suffix confusable host must not match")
	}
}

func FuzzParsePanoptoURLs(f *testing.F) {
	f.Add("https://demo.hosted.panopto.com/Panopto/Pages/Viewer.aspx?id=26b3ae9e-4a48-4dcc-96ba-0befba08a0fb")
	f.Add("https://demo.hosted.panopto.com/Panopto/Pages/Viewer.aspx?pid=f3b39fcf-882f-4849-93d6-a9f401236d36")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > sharedHostingMaxURLBytes {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		_, _, _ = parsePanoptoVideoURL(parsed)
		_, _, _ = parsePanoptoPlaylistURL(parsed)
	})
}
