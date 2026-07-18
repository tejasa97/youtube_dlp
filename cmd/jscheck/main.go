// Command jscheck verifies the packaged JavaScript helper boundary.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/javascript/ejs"
	"github.com/ytdlp-go/ytdlp/internal/javascript/protocol"
	"github.com/ytdlp-go/ytdlp/internal/javascript/supervisor"
)

func main() {
	flags := flag.NewFlagSet("jscheck", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	helper := flags.String("helper", "", "absolute path to the isolated JavaScript helper (default: beside this executable)")
	if err := flags.Parse(os.Args[1:]); err != nil || flags.NArg() != 0 {
		os.Exit(2)
	}
	client, err := supervisor.New(supervisor.Config{Path: *helper, MemoryBytes: ejs.SolverMemoryBytes})
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
	solver, err := ejs.New(client)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jscheck: EJS assets invalid")
		os.Exit(1)
	}
	result, err := solver.SolvePlayer(ctx, "ejs-probe", syntheticPlayer, []ejs.ChallengeRequest{
		{Type: ejs.ChallengeN, Challenges: []string{"abc"}},
		{Type: ejs.ChallengeSig, Challenges: []string{"abcdef"}},
	}, false)
	if err != nil || len(result.Responses) != 2 || result.Responses[0].Data["abc"] != "cba-n" || result.Responses[1].Data["abcdef"] != "fedcba" {
		fmt.Fprintln(os.Stderr, "jscheck: EJS challenge execution failed")
		os.Exit(1)
	}
	fmt.Printf("isolated JavaScript and EJS %s OK (%s)\n", ejs.Version, response.Stats.Engine)
}

const syntheticPlayer = `(function(){
var helper={alr:function(){}};
function Params(url,key,value){this.values={s:value,n:null}}
Params.prototype.set=function(key,value){this.values[key]=value};
Params.prototype.get=function(key){return this.values[key]};
Params.prototype.clone=function(){return this};
Params.prototype.transform=function(){
if(this.values.n)this.values.n=this.values.n.split("").reverse().join("")+"-n";
if(this.values.s)this.values.s=this.values.s.split("").reverse().join("")};
function candidate(url,key,value){helper.alr("alr","yes");return new Params(url,key,value)}
}).call(this);`
