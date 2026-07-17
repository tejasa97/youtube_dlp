// Command paritycheck validates the capability manifest.
package main

import (
	"fmt"
	"os"

	"github.com/ytdlp-go/ytdlp/internal/conformance"
)

func main() {
	path := "conformance/parity_manifest.yaml"
	if len(os.Args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: paritycheck [manifest]")
		os.Exit(2)
	}
	if len(os.Args) == 2 {
		path = os.Args[1]
	}

	manifest, err := conformance.LoadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "paritycheck: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("validated %d capabilities in %s\n", len(manifest.Capabilities), path)
}
