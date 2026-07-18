package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestExampleExchange(t *testing.T) {
	var input bytes.Buffer
	if err := write(&input, message{Type: "hello", Versions: []uint32{1}}); err != nil {
		t.Fatal(err)
	}
	if err := write(&input, message{Type: "extract", Version: 1, Request: &request{ID: "one", URL: "https://fixture.invalid/video"}}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(&input, &output); err != nil {
		t.Fatal(err)
	}
	var hello, result message
	if err := read(&output, &hello); err != nil {
		t.Fatal(err)
	}
	if err := read(&output, &result); err != nil {
		t.Fatal(err)
	}
	if hello.Manifest == nil || hello.Manifest.Versions[0] != 1 {
		t.Fatalf("hello = %#v", hello)
	}
	if result.Response == nil || result.Response.ID != "one" || result.Response.Metadata["title"] != "RPC example" {
		t.Fatalf("result = %#v", result)
	}
	expectedBytes, err := os.ReadFile("../../conformance/plugins/rpc/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected response
	if err := json.Unmarshal(expectedBytes, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*result.Response, expected) {
		t.Fatalf("fixture drift: result=%#v expected=%#v", *result.Response, expected)
	}
}
