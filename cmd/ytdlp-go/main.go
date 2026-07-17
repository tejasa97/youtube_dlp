// Command ytdlp-go is the command-line entry point for the Go port.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/ytdlp-go/ytdlp/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.RunContext(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
