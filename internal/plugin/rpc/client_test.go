package rpc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/plugin"
)

func TestRPCExtract(t *testing.T) {
	response, err := (Client{}).Extract(context.Background(), helperConfig("success"), plugin.ExtractRequest{ID: "one", URL: "https://fixture.invalid/video"})
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != "one" || response.Metadata["title"] != "RPC fixture" {
		t.Fatalf("response = %#v", response)
	}
}

func TestRPCVersionAndPermissionRejection(t *testing.T) {
	if _, err := (Client{}).Extract(context.Background(), helperConfig("version"), request()); !errors.Is(err, plugin.ErrIncompatibleVersion) {
		t.Fatalf("version error = %v", err)
	}
	if _, err := (Client{}).Extract(context.Background(), helperConfig("permission"), request()); !errors.Is(err, plugin.ErrPermissionDenied) {
		t.Fatalf("permission error = %v", err)
	}
}

func TestRPCStructuredRemoteError(t *testing.T) {
	response, err := (Client{}).Extract(context.Background(), helperConfig("remote"), request())
	var remote *plugin.RemoteFailure
	if !errors.As(err, &remote) || remote.Detail.Category != plugin.RemoteUnavailable || response.ID != "one" {
		t.Fatalf("response, error = %#v, %v", response, err)
	}
}

func TestRPCMalformedCrashAndOversize(t *testing.T) {
	tests := []struct {
		mode string
		want error
	}{
		{"malformed", plugin.ErrMalformedMessage},
		{"crash", plugin.ErrCrashed},
		{"oversize", plugin.ErrResourceLimit},
	}
	for _, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			if _, err := (Client{}).Extract(context.Background(), helperConfig(test.mode), request()); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRPCCrashDoesNotExposeStderrSecrets(t *testing.T) {
	_, err := (Client{}).Extract(context.Background(), helperConfig("crash-secret"), request())
	if !errors.Is(err, plugin.ErrCrashed) || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("crash error = %v", err)
	}
}

func TestRPCCancellationAndTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := (Client{}).Extract(ctx, helperConfig("hang"), request()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	config := helperConfig("hang")
	config.Limits.Timeout = 20 * time.Millisecond
	if _, err := (Client{}).Extract(context.Background(), config, request()); !errors.Is(err, plugin.ErrTimeout) {
		t.Fatalf("timeout error = %v", err)
	}
}

func FuzzReadFrame(f *testing.F) {
	f.Add([]byte{0, 0, 0, 2, '{', '}'})
	f.Add([]byte{0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		var message envelope
		_ = readFrame(bytesReader(data), 1024, &message)
	})
}

func helperConfig(mode string) Config {
	return Config{
		Executable: os.Args[0],
		Args:       []string{"-test.run=TestRPCPluginHelper", "--", mode},
		Limits: plugin.Limits{
			Timeout:         time.Second,
			CancelGrace:     20 * time.Millisecond,
			MaxMessageBytes: 1024,
		},
	}
}

func request() plugin.ExtractRequest {
	return plugin.ExtractRequest{ID: "one", URL: "https://fixture.invalid/video"}
}

func TestRPCPluginHelper(t *testing.T) {
	mode := ""
	for index, argument := range os.Args {
		if argument == "--" && index+1 < len(os.Args) {
			mode = os.Args[index+1]
			break
		}
	}
	if mode == "" {
		return
	}
	var hello envelope
	if err := readFrame(os.Stdin, 1<<20, &hello); err != nil {
		os.Exit(10)
	}
	if mode == "crash" || mode == "crash-secret" {
		message := "fixture crash"
		if mode == "crash-secret" {
			message = "token=fixture-secret"
		}
		_, _ = fmt.Fprint(os.Stderr, message)
		os.Exit(12)
	}
	if mode == "malformed" {
		_, _ = os.Stdout.Write([]byte{0, 0, 0, 1, '{'})
		return
	}
	if mode == "oversize" {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], 1025)
		_, _ = os.Stdout.Write(header[:])
		return
	}
	manifest := &plugin.Manifest{Name: "fixture", Versions: []uint32{plugin.ProtocolVersion}}
	if mode == "version" {
		manifest.Versions = []uint32{99}
	}
	if mode == "permission" {
		manifest.Permissions = []plugin.Permission{plugin.PermissionSecrets}
	}
	if err := writeFrame(os.Stdout, envelope{Type: "hello", Manifest: manifest}, 1<<20); err != nil {
		os.Exit(11)
	}
	if mode == "version" || mode == "permission" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	var extract envelope
	if err := readFrame(os.Stdin, 1<<20, &extract); err != nil {
		os.Exit(13)
	}
	if mode == "hang" {
		var cancel envelope
		_ = readFrame(os.Stdin, 1<<20, &cancel)
		return
	}
	if mode == "remote" {
		response := plugin.ExtractResponse{ID: extract.Request.ID, Error: &plugin.RemoteError{Category: plugin.RemoteUnavailable, Message: "fixture unavailable"}}
		_ = writeFrame(os.Stdout, envelope{Type: "result", Response: &response}, 1<<20)
		return
	}
	response := plugin.ExtractResponse{ID: extract.Request.ID, Metadata: map[string]any{"id": "fixture", "title": "RPC fixture"}}
	if err := writeFrame(os.Stdout, envelope{Type: "result", Response: &response}, 1<<20); err != nil {
		os.Exit(14)
	}
}
