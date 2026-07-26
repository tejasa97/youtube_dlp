package hls

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestSampleAESEligibilityIsFailClosed(t *testing.T) {
	tests := []struct {
		name, keyLine string
		eligible      bool
	}{
		{"implicit identity", `#EXT-X-KEY:METHOD=SAMPLE-AES,URI="key.bin"`, true},
		{"explicit identity", `#EXT-X-KEY:METHOD=SAMPLE-AES,URI="https://keys.example/key",KEYFORMAT="identity"`, true},
		{"fairplay format", `#EXT-X-KEY:METHOD=SAMPLE-AES,URI="https://keys.example/key",KEYFORMAT="com.apple.streamingkeydelivery"`, false},
		{"playready format", `#EXT-X-KEY:METHOD=SAMPLE-AES,URI="https://keys.example/key",KEYFORMAT="com.microsoft.playready"`, false},
		{"fairplay URI", `#EXT-X-KEY:METHOD=SAMPLE-AES,URI="skd://asset"`, false},
		{"userinfo URI", `#EXT-X-KEY:METHOD=SAMPLE-AES,URI="https://user@keys.example/key"`, false},
		{"sample aes ctr", `#EXT-X-KEY:METHOD=SAMPLE-AES-CTR,URI="https://keys.example/key"`, false},
		{"unknown method", `#EXT-X-KEY:METHOD=PRIVATE,URI="https://keys.example/key"`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse("https://media.example/video/index.m3u8", []byte(
				"#EXTM3U\n"+test.keyLine+"\n#EXTINF:1,\nsegment.ts\n#EXT-X-ENDLIST\n",
			))
			var encryption *EncryptionError
			if !errors.As(err, &encryption) || !errors.Is(err, ErrUnsupportedEncryption) {
				t.Fatalf("error=%v", err)
			}
			if encryption.FFmpegEligible != test.eligible {
				t.Fatalf("eligible=%t error=%#v", encryption.FFmpegEligible, encryption)
			}
			if strings.Contains(err.Error(), "keys.example") || strings.Contains(err.Error(), "asset") {
				t.Fatalf("error leaks key location: %v", err)
			}
		})
	}
}

func TestSessionKeyAndFAXSAreClassified(t *testing.T) {
	clear, err := Parse("https://media.example/master.m3u8", []byte(
		"#EXTM3U\n#EXT-X-SESSION-KEY:METHOD=SAMPLE-AES,URI=\"key.bin\"\n#EXT-X-STREAM-INF:BANDWIDTH=1\nmedia.m3u8\n",
	))
	if err != nil || len(clear.Variants) != 1 {
		t.Fatalf("clear session key playlist=%#v error=%v", clear, err)
	}

	tests := []struct {
		name, manifest string
	}{
		{
			"fairplay session key",
			"#EXTM3U\n#EXT-X-SESSION-KEY:METHOD=SAMPLE-AES,URI=\"skd://asset\",KEYFORMAT=\"com.apple.streamingkeydelivery\"\n#EXT-X-STREAM-INF:BANDWIDTH=1\nmedia.m3u8\n",
		},
		{
			"adobe faxs",
			"#EXTM3U\n#EXT-X-FAXS-CM:opaque\n#EXTINF:1,\nsegment.ts\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse("https://media.example/master.m3u8", []byte(test.manifest))
			var encryption *EncryptionError
			if !errors.As(err, &encryption) || encryption.FFmpegEligible {
				t.Fatalf("error=%v encryption=%#v", err, encryption)
			}
		})
	}
}

func TestAES128IdentityStillParsesNatively(t *testing.T) {
	playlist, err := Parse("https://media.example/index.m3u8", []byte(
		"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\",KEYFORMAT=\"identity\"\n#EXTINF:1,\nsegment.ts\n#EXT-X-ENDLIST\n",
	))
	if err != nil || playlist.Media == nil || len(playlist.Media.Segments) != 1 ||
		playlist.Media.Segments[0].Key == nil {
		t.Fatalf("playlist=%#v error=%v", playlist, err)
	}
}

func TestSampleAESMissingURIIsNeverDelegated(t *testing.T) {
	_, err := Parse("https://media.example/index.m3u8", []byte(
		"#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES\n#EXTINF:1,\nsegment.ts\n",
	))
	var encryption *EncryptionError
	if err == nil || errors.As(err, &encryption) {
		t.Fatalf("error=%v encryption=%#v", err, encryption)
	}
}

func TestDownloaderReportsSelectedMediaForSampleAES(t *testing.T) {
	transport := sampleAESTransport{pages: map[string]string{
		"https://media.example/master.m3u8":             "#EXTM3U\n#EXT-X-SESSION-KEY:METHOD=SAMPLE-AES,URI=\"session-key.bin\"\n#EXT-X-STREAM-INF:BANDWIDTH=9\nmedia.m3u8?token=secret\n",
		"https://media.example/media.m3u8?token=secret": "#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"key.bin\"\n#EXTINF:1,\nsegment.ts\n#EXT-X-ENDLIST\n",
	}}
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), "https://media.example/master.m3u8",
		t.TempDir(), t.TempDir()+"/output.mp4", false, nil,
	)
	var encryption *EncryptionError
	if !errors.As(err, &encryption) || !encryption.FFmpegEligible ||
		encryption.MediaURL != "https://media.example/media.m3u8?token=secret" {
		t.Fatalf("error=%v encryption=%#v", err, encryption)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaks selected URL: %v", err)
	}
}

type sampleAESTransport struct {
	pages map[string]string
}

func (transport sampleAESTransport) ReadPage(_ context.Context, rawURL string) ([]byte, http.Header, error) {
	return []byte(transport.pages[rawURL]), nil, nil
}

func (sampleAESTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected fragment request")
}

func FuzzSampleAESEligibility(f *testing.F) {
	f.Add("SAMPLE-AES", "identity", "https://keys.example/key")
	f.Add("SAMPLE-AES", "com.apple.streamingkeydelivery", "skd://asset")
	f.Add("AES-128", "identity", "key.bin")
	f.Fuzz(func(t *testing.T, method, keyFormat, keyURI string) {
		if len(method)+len(keyFormat)+len(keyURI) > 4096 ||
			strings.ContainsAny(method+keyFormat+keyURI, "\"\r\n\x00") {
			t.Skip()
		}
		manifest := "#EXTM3U\n#EXT-X-KEY:METHOD=" + method + ",URI=\"" + keyURI + "\""
		if keyFormat != "" {
			manifest += ",KEYFORMAT=\"" + keyFormat + "\""
		}
		manifest += "\n#EXTINF:1,\nsegment.ts\n"
		_, err := Parse("https://media.example/index.m3u8", []byte(manifest))
		var encryption *EncryptionError
		if !errors.As(err, &encryption) || !encryption.FFmpegEligible {
			return
		}
		if encryption.Method != "SAMPLE-AES" ||
			(encryption.KeyFormat != "" && encryption.KeyFormat != "identity") {
			t.Fatalf("unsafe delegation: %#v", encryption)
		}
		if strings.Contains(err.Error(), "keys.example") {
			t.Fatalf("error leaks key host: %v", err)
		}
	})
}
