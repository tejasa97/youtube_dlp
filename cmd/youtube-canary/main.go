// Command youtube-canary validates the opt-in YouTube interoperability harness.
// It never runs during ordinary go test and refuses to start without
// YTDLP_YOUTUBE_CANARY enablement.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/youtubecanary"
)

func main() {
	flags := flag.NewFlagSet("youtube-canary", flag.ExitOnError)
	class := flags.String("class", "public", "public or credential")
	target := flags.String("target-ref", "youtube.public.fixture", "opaque target handle")
	secret := flags.String("secret-handle", "", "secret handle for credential class")
	timeout := flags.Duration("timeout", 5*time.Second, "canary timeout")
	maxBytes := flags.Int64("max-bytes", 1<<20, "response byte budget")
	maxRequests := flags.Int("max-requests", 4, "request budget")
	_ = flags.Parse(os.Args[1:])

	result, err := youtubecanary.Run(context.Background(), youtubecanary.Config{
		Class: *class, TargetRef: *target, SecretHandle: *secret,
		Timeout: *timeout, MaxBytes: *maxBytes, MaxRequests: *maxRequests,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "youtube-canary:", err)
		os.Exit(2)
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "youtube-canary: encode failed")
		os.Exit(1)
	}
	if len(encoded) > youtubecanary.MaxOutputBytes {
		fmt.Fprintln(os.Stderr, "youtube-canary: output too large")
		os.Exit(1)
	}
	fmt.Println(string(encoded))
}
