package extractor

import (
	"errors"
	"net/url"
	"testing"
)

func FuzzParseApplePodcastsURL(f *testing.F) {
	f.Add("https://podcasts.apple.com/us/podcast/title/id1?i=1000482637777")
	f.Add("https://podcasts.apple.com/podcast/id1?i=1000482637777")
	f.Add("https://podcasts.apple.com/podcast/title?i=1")
	f.Add("https://evil.test/podcast/id1?i=1")
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		first, ok1 := parseApplePodcastsURL(parsed)
		second, ok2 := parseApplePodcastsURL(parsed)
		if ok1 != ok2 || first != second {
			t.Fatalf("non-deterministic parse: %#v %#v", first, second)
		}
		if NewApplePodcasts().Suitable(parsed) != ok1 {
			t.Fatal("Suitable diverged from parser")
		}
		if !ok1 {
			return
		}
		if !applePodcastsEpisodeIDPattern.MatchString(first.id) {
			t.Fatalf("invalid id %#v", first)
		}
		if first.webpageURL == "" || len(first.webpageURL) > applePodcastsMaxURLBytes {
			t.Fatalf("invalid webpage %#v", first)
		}
	})
}

func FuzzApplePodcastsSerializedData(f *testing.F) {
	f.Add([]byte(`<script id="serialized-server-data">{"data":[{"data":{"headerButtonItems":[{"$kind":"share","modelType":"EpisodeLockup","model":{"title":"T","playAction":{"episodeOffer":{"streamUrl":"https://cdn.example.test/a.mp3"}}}}]}}]}</script>`))
	f.Add([]byte(`<script type="application/json" id='serialized-server-data'>[{"data":{"headerButtonItems":[]}}]</script>`))
	f.Add([]byte(`<html></html>`))
	f.Add([]byte(`{`))
	f.Fuzz(func(t *testing.T, page []byte) {
		if len(page) > 1<<20 {
			page = page[:1<<20]
		}
		target := applePodcastsTarget{id: "1", webpageURL: "https://podcasts.apple.com/podcast/id1?i=1"}
		first, err1 := parseApplePodcastsPage(page, target)
		second, err2 := parseApplePodcastsPage(page, target)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("error presence diverged: %v %v", err1, err2)
		}
		if err1 != nil {
			if !(errors.Is(err1, ErrInvalidMetadata) || errors.Is(err1, ErrUnavailable) ||
				errors.Is(err1, ErrJSONResponseTooLarge) || errors.Is(err1, ErrUnsupported) ||
				errors.Is(err1, ErrAuthentication)) {
				t.Fatalf("uncategorized error: %v", err1)
			}
			if (errors.Is(err1, ErrInvalidMetadata) != errors.Is(err2, ErrInvalidMetadata)) ||
				(errors.Is(err1, ErrJSONResponseTooLarge) != errors.Is(err2, ErrJSONResponseTooLarge)) {
				t.Fatalf("error category diverged: %v %v", err1, err2)
			}
			return
		}
		title1, _ := first.Info.Lookup("title").StringValue()
		title2, _ := second.Info.Lookup("title").StringValue()
		if title1 == "" || title1 != title2 {
			t.Fatalf("titles %#v %#v", title1, title2)
		}
		formats, ok := first.Info.Lookup("formats").ListValue()
		if !ok || len(formats) != 1 {
			t.Fatalf("formats %#v", first.Info.Lookup("formats"))
		}
		format, _ := formats[0].Object()
		stream, _ := format.Lookup("url").StringValue()
		if cleaned, ok := cleanApplePodcastsURL(stream); !ok || cleaned != stream {
			t.Fatalf("unsafe stream retained: %q", stream)
		}
	})
}
