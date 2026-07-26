package dash

import "testing"

func TestParseTextRepresentations(t *testing.T) {
	manifest := []byte(`<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011">
  <Period>
    <AdaptationSet contentType="text" mimeType="text/vtt" lang="en" label="English">
      <Representation id="en" mimeType="text/vtt">
        <BaseURL>subs_en.vtt</BaseURL>
      </Representation>
    </AdaptationSet>
    <AdaptationSet mimeType="application/ttml+xml" lang="fr">
      <BaseURL>subs_fr.ttml</BaseURL>
    </AdaptationSet>
  </Period>
</MPD>`)
	representations, err := ParseTextRepresentations("https://cdn.example.test/master.mpd", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(representations) != 2 || representations[0].Language != "en" || representations[1].Language != "fr" {
		t.Fatalf("representations = %#v", representations)
	}
	if representations[0].URL != "https://cdn.example.test/subs_en.vtt" || representations[1].URL != "https://cdn.example.test/subs_fr.ttml" {
		t.Fatalf("resolved URLs = %#v", representations)
	}
}

func TestParseTextRepresentationsIgnoresVideoAdaptationSets(t *testing.T) {
	manifest := []byte(`<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011">
  <Period>
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <Representation id="video"><BaseURL>video.mp4</BaseURL></Representation>
    </AdaptationSet>
  </Period>
</MPD>`)
	representations, err := ParseTextRepresentations("https://cdn.example.test/master.mpd", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(representations) != 0 {
		t.Fatalf("representations = %#v", representations)
	}
}

func TestParseTextRepresentationsResolvesHierarchicalBaseURLs(t *testing.T) {
	manifest := []byte(`<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011">
  <BaseURL>https://cdn.example.test/</BaseURL>
  <Period>
    <BaseURL>period/</BaseURL>
    <AdaptationSet contentType="text" lang="en">
      <BaseURL>subs/</BaseURL>
      <Representation id="en"><BaseURL>en.vtt</BaseURL></Representation>
    </AdaptationSet>
  </Period>
</MPD>`)
	representations, err := ParseTextRepresentations("https://cdn.example.test/master.mpd", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(representations) != 1 || representations[0].URL != "https://cdn.example.test/period/subs/en.vtt" {
		t.Fatalf("representations = %#v", representations)
	}
}

func TestParseTextRepresentationsOmitsAdaptationBaseWhenRepresentationsExist(t *testing.T) {
	manifest := []byte(`<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011">
  <Period>
    <AdaptationSet contentType="text" lang="en">
      <BaseURL>subs_en.vtt</BaseURL>
      <Representation id="en"><BaseURL>ignored.vtt</BaseURL></Representation>
    </AdaptationSet>
  </Period>
</MPD>`)
	representations, err := ParseTextRepresentations("https://cdn.example.test/master.mpd", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(representations) != 1 || representations[0].URL != "https://cdn.example.test/ignored.vtt" {
		t.Fatalf("representations = %#v", representations)
	}
}

func FuzzParseTextRepresentations(f *testing.F) {
	f.Add("https://cdn.example.test/master.mpd", []byte(`<?xml version="1.0"?><MPD><Period><AdaptationSet contentType="text"><BaseURL>subs.vtt</BaseURL></AdaptationSet></Period></MPD>`))
	f.Fuzz(func(t *testing.T, rawURL string, data []byte) {
		if len(data) > maxPlaylistBytes {
			t.Skip()
		}
		representations, err := ParseTextRepresentations(rawURL, data)
		if err != nil {
			return
		}
		if len(representations) > maxTextRepresentations {
			t.Fatalf("representation overflow: %d", len(representations))
		}
		for _, representation := range representations {
			if representation.URL == "" {
				t.Fatalf("empty representation URL: %#v", representation)
			}
		}
	})
}
