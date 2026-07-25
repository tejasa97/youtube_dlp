package extractor

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

const (
	panoptoTestHost   = "demo.hosted.panopto.com"
	panoptoTestPID    = "f3b39fcf-882f-4849-93d6-a9f401236d36"
	panoptoTestSList  = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	panoptoTestVideo1 = "26b3ae9e-4a48-4dcc-96ba-0befba08a0fb"
	panoptoTestVideo2 = "ed01b077-c9e5-4c7b-b8ff-15fa306d7a59"
)

func panoptoDeliveryEndpoint(host, id string) string {
	return "https://" + host + "/Panopto/Pages/Viewer/DeliveryInfo.aspx?deliveryId=" + id + "&responseType=json"
}

func TestPanoptoFamilySuccess(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(NewPanoptoPlaylist(), NewPanopto())

	t.Run("panopto video", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			panoptoDeliveryEndpoint(panoptoTestHost, panoptoTestVideo1): {body: familyFixture(t, "panopto", "deliveryinfo.json")},
		}}
		result, err := NewPanopto().Extract(context.Background(), Request{
			URL: "https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?id=" + panoptoTestVideo1, Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		formats, ok := result.Info.Formats()
		if !ok || len(formats) != 2 {
			t.Fatalf("formats=%v ok=%t", formats, ok)
		}
		if title, ok := result.Info.Title(); !ok || title != "Panopto for Business - Use Cases" {
			t.Fatalf("title=%q ok=%t", title, ok)
		}
	})

	t.Run("panopto_playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://" + panoptoTestHost + "/Panopto/Api/Playlists/" + panoptoTestPID: {
				body: familyFixture(t, "panopto_playlist", "playlist.json"),
			},
			"https://" + panoptoTestHost + "/Panopto/Api/SessionLists/" + panoptoTestSList + "?collections[0].maxCount=500&collections[0].name=items": {
				body: familyFixture(t, "panopto_playlist", "sessionlist.json"),
			},
		}}
		result, err := NewPanoptoPlaylist().Extract(context.Background(), Request{
			URL: "https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?pid=" + panoptoTestPID, Transport: transport,
		})
		if err != nil || !result.IsPlaylist() {
			t.Fatalf("%#v %v", result, err)
		}
		if transport.requestCount() != 0 {
			t.Fatalf("lazy playlist fetched before iteration: %d", transport.requestCount())
		}

		entries, err := CollectEntries(context.Background(), result.Entries, panoptoMaxEntries)
		if err != nil {
			t.Fatal(err)
		}
		// Third session item duplicates video1's id with a hostile ViewerUri
		// and the fourth item is a Folder: both must be skipped, leaving
		// exactly the two genuine Session entries in document order.
		if len(entries) != 2 || entries[0].ID != panoptoTestVideo1 || entries[1].ID != panoptoTestVideo2 {
			t.Fatalf("entries=%v err=%v", entries, err)
		}
		for _, entry := range entries {
			if entry.ExtractorKey != "panopto" {
				t.Fatalf("entry.ExtractorKey=%q", entry.ExtractorKey)
			}
			if want := "https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?id=" + entry.ID; entry.URL != want {
				t.Fatalf("entry.URL=%q want %q", entry.URL, want)
			}
		}

		// URLResult re-entry: the emitted entry resolves via the registry and
		// the minimal video IE to real formats.
		selected, err := registry.SelectFor(entries[0].URL, entries[0].ExtractorKey)
		if err != nil || selected.Name() != "panopto" {
			t.Fatalf("SelectFor=%v err=%v", selected, err)
		}
		videoTransport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			panoptoDeliveryEndpoint(panoptoTestHost, panoptoTestVideo1): {body: familyFixture(t, "panopto", "deliveryinfo.json")},
		}}
		media, err := selected.Extract(context.Background(), Request{URL: entries[0].URL, Transport: videoTransport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats after re-entry")
		}

		// Ordered + reusable iteration: a fresh Iterator over the same
		// Entries must reproduce the identical order without refetching.
		again, err := CollectEntries(context.Background(), result.Entries, panoptoMaxEntries)
		if err != nil || len(again) != len(entries) {
			t.Fatalf("reusable iteration failed: %v err=%v", again, err)
		}
		for i := range entries {
			if entries[i].ID != again[i].ID || entries[i].URL != again[i].URL {
				t.Fatalf("order/reuse mismatch at %d: %#v vs %#v", i, entries[i], again[i])
			}
		}
	})

	for _, test := range []struct {
		rawURL, want string
	}{
		{"https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?id=" + panoptoTestVideo1, "panopto"},
		{"https://" + panoptoTestHost + "/Panopto/Pages/Embed.aspx?id=" + panoptoTestVideo1, "panopto"},
		{"https://howtovideos.hosted.panopto.com/Panopto/Pages/Viewer.aspx?pid=" + panoptoTestPID + "&id=" + panoptoTestVideo1 + "&advance=true", "panopto_playlist"},
		{"https://utsa.panopto.eu/Panopto/Pages/Viewer.aspx?pid=" + panoptoTestPID, "panopto_playlist"},
	} {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q)=%v err=%v want %q", test.rawURL, selected, err, test.want)
		}
	}

	// Non-stealing: a plain video URL (no pid) must never route to the
	// playlist extractor, and a playlist URL (pid present) must defer from
	// the video extractor, mirroring the reference's explicit precedence.
	videoOnly, _ := url.Parse("https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?id=" + panoptoTestVideo1)
	if NewPanoptoPlaylist().Suitable(videoOnly) {
		t.Fatal("PanoptoPlaylist must not steal plain video URLs")
	}
	playlistOnly, _ := url.Parse("https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?pid=" + panoptoTestPID)
	if NewPanopto().Suitable(playlistOnly) {
		t.Fatal("Panopto must defer to PanoptoPlaylist when pid is present")
	}
}

func TestPanoptoFamilyNegatives(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewPanopto().Extract(canceled, Request{
		URL: "https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?id=" + panoptoTestVideo1, Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if _, err := NewPanoptoPlaylist().Extract(canceled, Request{
		URL: "https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?pid=" + panoptoTestPID, Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("playlist cancel=%v", err)
	}

	// Secret-safe: ErrorCode 2 means auth required, and the ErrorMessage body
	// text must never leak into the returned error.
	auth := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		panoptoDeliveryEndpoint(panoptoTestHost, panoptoTestVideo1): {
			body: []byte(`{"ErrorCode":2,"ErrorMessage":"token=must-not-leak"}`),
		},
	}}
	_, err := NewPanopto().Extract(context.Background(), Request{
		URL: "https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?id=" + panoptoTestVideo1, Transport: auth,
	})
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("auth=%v", err)
	}
	if strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("leaked secret: %v", err)
	}

	// Hostile URL rejection: userinfo, unsafe ports, suffix-confusable hosts,
	// and bare apex domains (no tenant subdomain) must all be rejected. Note
	// plain http:// input is still routable at Suitable (this codebase's
	// convention of accepting http/https there and forcing https internally),
	// so it is intentionally not included here.
	for _, raw := range []string{
		"https://user:pass@" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?id=" + panoptoTestVideo1,
		"https://" + panoptoTestHost + ":8443/Panopto/Pages/Viewer.aspx?id=" + panoptoTestVideo1,
		"https://evilpanopto.com/Panopto/Pages/Viewer.aspx?id=" + panoptoTestVideo1,
		"https://panopto.com/Panopto/Pages/Viewer.aspx?id=" + panoptoTestVideo1,
	} {
		parsed, parseErr := url.Parse(raw)
		if parseErr != nil {
			t.Fatalf("parse %q: %v", raw, parseErr)
		}
		if NewPanopto().Suitable(parsed) {
			t.Fatalf("Panopto must reject hostile URL %q", raw)
		}
	}
	for _, raw := range []string{
		"https://user:pass@" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?pid=" + panoptoTestPID,
		"https://" + panoptoTestHost + ":8443/Panopto/Pages/Viewer.aspx?pid=" + panoptoTestPID,
		"https://evilpanopto.com/Panopto/Pages/Viewer.aspx?pid=" + panoptoTestPID,
		"https://panopto.eu/Panopto/Pages/Viewer.aspx?pid=" + panoptoTestPID,
	} {
		parsed, parseErr := url.Parse(raw)
		if parseErr != nil {
			t.Fatalf("parse %q: %v", raw, parseErr)
		}
		if NewPanoptoPlaylist().Suitable(parsed) {
			t.Fatalf("PanoptoPlaylist must reject hostile URL %q", raw)
		}
	}

	// A session list where every ViewerUri is off-family must yield an empty
	// (rejected) playlist rather than trusting the hostile redirect.
	hostile := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://" + panoptoTestHost + "/Panopto/Api/Playlists/" + panoptoTestPID: {
			body: familyFixture(t, "panopto_playlist", "playlist.json"),
		},
		"https://" + panoptoTestHost + "/Panopto/Api/SessionLists/" + panoptoTestSList + "?collections[0].maxCount=500&collections[0].name=items": {
			body: []byte(`{"Items":[{"TypeName":"Session","Id":"` + panoptoTestVideo1 + `","Name":"Evil","ViewerUri":"https://evil.example/steal"}]}`),
		},
	}}
	hostileResult, err := NewPanoptoPlaylist().Extract(context.Background(), Request{
		URL: "https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?pid=" + panoptoTestPID, Transport: hostile,
	})
	if err != nil || !hostileResult.IsPlaylist() {
		t.Fatalf("hostile extract=%v %#v", err, hostileResult)
	}
	if _, err := CollectEntries(context.Background(), hostileResult.Entries, panoptoMaxEntries); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("hostile ViewerUri should yield empty-playlist error, got %v", err)
	}

	// Cross-tenant ViewerUri (another panopto-family host) must not be trusted
	// or rewritten onto the foreign tenant; skip the item entirely.
	crossTenant := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://" + panoptoTestHost + "/Panopto/Api/Playlists/" + panoptoTestPID: {
			body: familyFixture(t, "panopto_playlist", "playlist.json"),
		},
		"https://" + panoptoTestHost + "/Panopto/Api/SessionLists/" + panoptoTestSList + "?collections[0].maxCount=500&collections[0].name=items": {
			body: []byte(`{"Items":[{"TypeName":"Session","Id":"` + panoptoTestVideo1 + `","Name":"X","ViewerUri":"https://other.hosted.panopto.com/Panopto/Pages/Viewer.aspx?id=` + panoptoTestVideo1 + `"}]}`),
		},
	}}
	crossResult, err := NewPanoptoPlaylist().Extract(context.Background(), Request{
		URL: "https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?pid=" + panoptoTestPID, Transport: crossTenant,
	})
	if err != nil || !crossResult.IsPlaylist() {
		t.Fatalf("cross-tenant extract=%v %#v", err, crossResult)
	}
	if _, err := CollectEntries(context.Background(), crossResult.Entries, panoptoMaxEntries); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("cross-tenant ViewerUri should be rejected, got %v", err)
	}

	// ViewerUri id that conflicts with the Session Id must be rejected.
	conflictURI := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://" + panoptoTestHost + "/Panopto/Api/Playlists/" + panoptoTestPID: {
			body: familyFixture(t, "panopto_playlist", "playlist.json"),
		},
		"https://" + panoptoTestHost + "/Panopto/Api/SessionLists/" + panoptoTestSList + "?collections[0].maxCount=500&collections[0].name=items": {
			body: []byte(`{"Items":[{"TypeName":"Session","Id":"` + panoptoTestVideo1 + `","Name":"X","ViewerUri":"https://` + panoptoTestHost + `/Panopto/Pages/Viewer.aspx?id=` + panoptoTestVideo2 + `"}]}`),
		},
	}}
	conflictResult, err := NewPanoptoPlaylist().Extract(context.Background(), Request{
		URL: "https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?pid=" + panoptoTestPID, Transport: conflictURI,
	})
	if err != nil || !conflictResult.IsPlaylist() {
		t.Fatalf("conflicting ViewerUri extract=%v %#v", err, conflictResult)
	}
	if _, err := CollectEntries(context.Background(), conflictResult.Entries, panoptoMaxEntries); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("conflicting ViewerUri id should be rejected, got %v", err)
	}

	// Duplicate/conflicting id or pid query values fail at routing boundaries.
	for _, raw := range []string{
		"https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?id=" + panoptoTestVideo1 + "&id=" + panoptoTestVideo2,
		"https://" + panoptoTestHost + "/Panopto/Pages/Embed.aspx?id=" + panoptoTestVideo1 + "&id=" + panoptoTestVideo2,
	} {
		parsed, parseErr := url.Parse(raw)
		if parseErr != nil {
			t.Fatalf("parse %q: %v", raw, parseErr)
		}
		if NewPanopto().Suitable(parsed) {
			t.Fatalf("Panopto must reject conflicting id values: %q", raw)
		}
	}
	for _, raw := range []string{
		"https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?pid=" + panoptoTestPID + "&pid=bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		"https://" + panoptoTestHost + "/Panopto/Pages/Embed.aspx?pid=" + panoptoTestPID + "&pid=bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
	} {
		parsed, parseErr := url.Parse(raw)
		if parseErr != nil {
			t.Fatalf("parse %q: %v", raw, parseErr)
		}
		if NewPanoptoPlaylist().Suitable(parsed) {
			t.Fatalf("PanoptoPlaylist must reject conflicting pid values: %q", raw)
		}
		if NewPanopto().Suitable(parsed) {
			t.Fatalf("Panopto must not fall through on conflicting pid values: %q", raw)
		}
	}
	// Identical repeated values remain acceptable.
	sameID, _ := url.Parse("https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?id=" + panoptoTestVideo1 + "&id=" + panoptoTestVideo1)
	if !NewPanopto().Suitable(sameID) {
		t.Fatal("identical repeated id values must remain Suitable")
	}
	samePID, _ := url.Parse("https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?pid=" + panoptoTestPID + "&pid=" + panoptoTestPID)
	if !NewPanoptoPlaylist().Suitable(samePID) {
		t.Fatal("identical repeated pid values must remain Suitable")
	}

	// A playlist with only non-Session items yields the same empty-playlist error.
	empty := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://" + panoptoTestHost + "/Panopto/Api/Playlists/" + panoptoTestPID: {
			body: familyFixture(t, "panopto_playlist", "playlist.json"),
		},
		"https://" + panoptoTestHost + "/Panopto/Api/SessionLists/" + panoptoTestSList + "?collections[0].maxCount=500&collections[0].name=items": {
			body: []byte(`{"Items":[{"TypeName":"Folder","Id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","Name":"Empty"}]}`),
		},
	}}
	emptyResult, err := NewPanoptoPlaylist().Extract(context.Background(), Request{
		URL: "https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?pid=" + panoptoTestPID, Transport: empty,
	})
	if err != nil || !emptyResult.IsPlaylist() {
		t.Fatalf("empty extract=%v %#v", err, emptyResult)
	}
	if _, err := CollectEntries(context.Background(), emptyResult.Entries, panoptoMaxEntries); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("empty playlist=%v", err)
	}
}

func FuzzParsePanoptoURL(f *testing.F) {
	f.Add("https://" + panoptoTestHost + "/Panopto/Pages/Viewer.aspx?id=" + panoptoTestVideo1)
	f.Add("https://howtovideos.hosted.panopto.com/Panopto/Pages/Viewer.aspx?pid=" + panoptoTestPID)
	f.Add("https://evilpanopto.com/Panopto/Pages/Viewer.aspx?id=x")
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
