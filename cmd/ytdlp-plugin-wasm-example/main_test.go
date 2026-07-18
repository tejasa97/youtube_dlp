package main

import (
	"context"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/plugin"
	pluginwasm "github.com/ytdlp-go/ytdlp/internal/plugin/wasm"
)

func TestExampleModule(t *testing.T) {
	fixtureHex, err := os.ReadFile("../../conformance/plugins/wasm/example.hex")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(fixtureHex)) != moduleHex {
		t.Fatal("checked-in WASM fixture drifted from example command")
	}
	module, err := hex.DecodeString(moduleHex)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (pluginwasm.Host{}).Extract(context.Background(), module, pluginwasm.Config{
		Manifest: plugin.Manifest{
			Schema: plugin.ManifestSchema, ID: "example.wasm", Name: "WASM example", Release: "1.0.0",
			Runtime: "wasm", Entrypoint: "example.wasm",
			ABIRange:     plugin.VersionRange{Minimum: plugin.ProtocolV1_0, Maximum: plugin.ProtocolV1_0},
			Capabilities: []plugin.Capability{plugin.CapabilityExtractor},
		},
		Limits: plugin.Limits{Timeout: time.Second, MaxMessageBytes: 1 << 20, MemoryLimitPages: 2},
	}, plugin.ExtractRequest{ID: "one", URL: "https://fixture.invalid/video"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Metadata["title"] != "WASM example" {
		t.Fatalf("response = %#v", response)
	}
}
