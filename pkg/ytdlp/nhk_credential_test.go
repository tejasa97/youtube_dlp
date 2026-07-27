package ytdlp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestNHKCredentialIsolatedMediaDownloadStripsAmbientCredentials(t *testing.T) {
	var mu sync.Mutex
	var captured http.Header
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		captured = request.Header.Clone()
		mu.Unlock()
		_, _ = writer.Write([]byte("audio-bytes"))
	}))
	defer server.Close()

	transport, err := network.New(network.Config{
		DefaultHeaders: http.Header{
			"Cookie":              {"ambient-session=secret"},
			"Authorization":       {"Bearer ambient-token"},
			"Proxy-Authorization": {"Basic proxy-secret"},
			"Referer":             {"https://www.nhk.or.jp/radio/player/ondemand.html"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("nhk-test")},
		value.Field{Key: "title", Value: value.String("NHK Test")},
		value.Field{Key: "ext", Value: value.String("m4a")},
	))
	info.Set("formats", value.List(value.ObjectValue(value.NewObject(
		value.Field{Key: "format_id", Value: value.String("direct")},
		value.Field{Key: "url", Value: value.String(server.URL + "/media.bin")},
		value.Field{Key: "ext", Value: value.String("bin")},
		value.Field{Key: "protocol", Value: value.String("https")},
		value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
	))))

	operation := &operation{
		client:    NewClient(),
		request:   Request{OutputDir: root},
		transport: transport,
	}
	selected, err := operation.selectFormats(info)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || !selected[0].CredentialIsolated {
		t.Fatalf("selection=%#v", selected)
	}
	_, _, err = operation.downloadSelection(context.Background(), selected[0], root, root+"/out.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		if v := captured.Get(key); v != "" {
			t.Fatalf("isolated media download leaked %s: %s", key, v)
		}
	}
}
