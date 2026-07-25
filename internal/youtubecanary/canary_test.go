package youtubecanary

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestYouTubeCanaryDisabledByDefault(t *testing.T) {
	t.Setenv(EnvEnable, "")
	if Enabled() {
		t.Fatal("enabled")
	}
	_, err := Run(context.Background(), Config{Class: "public", TargetRef: "youtube.public.fixture", Timeout: time.Second, MaxBytes: 1024, MaxRequests: 1})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubeCanaryValidateAndRedact(t *testing.T) {
	t.Setenv(EnvEnable, "1")
	if !Enabled() {
		t.Fatal("not enabled")
	}
	result, err := Run(context.Background(), Config{
		Class: "public", TargetRef: "youtube.public.fixture",
		Timeout: time.Second, MaxBytes: 1024, MaxRequests: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "not_executed" || result.TargetRef != "youtube.public.fixture" {
		t.Fatalf("%#v", result)
	}
	_, err = Run(context.Background(), Config{
		Class: "credential", TargetRef: "youtube.members.fixture", SecretHandle: "keychain:youtube.fixture",
		Timeout: time.Second, MaxBytes: 1024, MaxRequests: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), Config{
		Class: "public", TargetRef: "https://rr1---sn.googlevideo.com/videoplayback?sig=1",
		Timeout: time.Second, MaxBytes: 1024, MaxRequests: 1,
	})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err=%v", err)
	}
	_, err = Run(context.Background(), Config{
		Class: "credential", TargetRef: "youtube.members.fixture", SecretHandle: "cookie=SAPISID",
		Timeout: time.Second, MaxBytes: 1024, MaxRequests: 1,
	})
	if !errors.Is(err, ErrSecretRequired) && !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubeCanaryCancellation(t *testing.T) {
	t.Setenv(EnvEnable, "true")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, Config{Class: "public", TargetRef: "youtube.public.fixture", Timeout: time.Second, MaxBytes: 1024, MaxRequests: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubeCanaryNotInvokedByOrdinaryTestMain(t *testing.T) {
	if os.Getenv(EnvEnable) != "" && os.Getenv("YTDLP_YOUTUBE_CANARY_FORCE_TEST") == "" {
		t.Fatal("ordinary tests must not export live canary enablement")
	}
}
