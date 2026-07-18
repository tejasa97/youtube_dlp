package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/upstreamdelta"
)

func main() {
	repository := flag.String("reference", "", "path to a read-only yt-dlp Git checkout")
	commit := flag.String("commit", "", "pinned reference commit")
	timeout := flag.Duration("timeout", 30*time.Second, "maximum replay inventory time")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	report, err := upstreamdelta.Replay(ctx, upstreamdelta.Config{Repository: *repository, Commit: *commit})
	if err != nil {
		fmt.Fprintln(os.Stderr, "delta replay:", err)
		os.Exit(1)
	}
	encoded, err := upstreamdelta.Marshal(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode delta replay:", err)
		os.Exit(1)
	}
	_, _ = os.Stdout.Write(append(encoded, '\n'))
}
