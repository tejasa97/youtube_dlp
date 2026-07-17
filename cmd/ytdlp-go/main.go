// Command ytdlp-go is the command-line entry point for the Go port.
package main

import (
	"os"

	"github.com/ytdlp-go/ytdlp/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
