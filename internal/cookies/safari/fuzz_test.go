package safari

import (
	"bytes"
	"context"
	"testing"
)

func FuzzParse(f *testing.F) {
	f.Add(fixtureDatabase(fixturePage(
		fixtureCookie{domain: "localhost", name: "name", path: "/", value: "value", expiry: 1, creation: 1},
	)))
	f.Add([]byte("cook\x00\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, input []byte) {
		original := append([]byte(nil), input...)
		first, firstErr := Parse(context.Background(), input, Options{MaxCookies: 10_000, MaxBytes: 1 << 20})
		second, secondErr := Parse(context.Background(), input, Options{MaxCookies: 10_000, MaxBytes: 1 << 20})
		if !bytes.Equal(input, original) {
			t.Fatal("Parse mutated input")
		}
		if (firstErr == nil) != (secondErr == nil) || first.Total != second.Total ||
			first.Imported != second.Imported || first.Failed != second.Failed ||
			len(first.Cookies) != len(second.Cookies) {
			t.Fatal("Parse is not deterministic")
		}
		if first.Imported > first.Total || first.Failed > first.Total ||
			first.Imported+first.Failed != first.Total || len(first.Cookies) != first.Imported {
			t.Fatalf("invalid counts: %#v", first)
		}
		for _, cookie := range first.Cookies {
			assertValidCookie(t, cookie)
		}
	})
}
