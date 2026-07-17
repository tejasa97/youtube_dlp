// Command jscheck verifies the packaged JavaScript helper boundary.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/javascript/protocol"
	"github.com/ytdlp-go/ytdlp/internal/javascript/supervisor"
)

func main() {
	flags := flag.NewFlagSet("jscheck", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	helper := flags.String("helper", "ytdlp-js-helper", "path to the isolated JavaScript helper")
	if err := flags.Parse(os.Args[1:]); err != nil || flags.NArg() != 0 {
		os.Exit(2)
	}
	client, err := supervisor.New(supervisor.Config{Path: *helper, MemoryBytes: 64 << 20})
	if err != nil {
		fmt.Fprintln(os.Stderr, "jscheck: helper unavailable")
		os.Exit(1)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response := client.Execute(ctx, protocol.Request{
		Version: protocol.Version, ID: "python-free-probe", Operation: protocol.OperationCall,
		Script:   "function reverse(value) { return value.split('').reverse().join(''); }",
		Function: "reverse", Arguments: []json.RawMessage{json.RawMessage(`"golang"`)},
	})
	if response.Error != nil || string(response.Result) != `"gnalog"` {
		fmt.Fprintln(os.Stderr, "jscheck: isolated execution failed")
		os.Exit(1)
	}
	fmt.Printf("isolated JavaScript OK (%s)\n", response.Stats.Engine)
}
