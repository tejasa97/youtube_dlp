package netscape

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseSemanticsAndRoundTrip(t *testing.T) {
	input := "# Netscape HTTP Cookie File\n" +
		"#HttpOnly_.example.com\tTRUE\t/secure\tTRUE\t0\tsession\tsecret\n" +
		"example.org\tFALSE\t/\tFALSE\t1893456000\tpersistent\tvalue\n"
	result, err := Parse(context.Background(), strings.NewReader(input), Options{Now: time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 2 || result.Session != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !result.Entries[0].Cookie.HttpOnly || !result.Entries[0].IncludeSubdomains || !result.Entries[0].Cookie.Secure {
		t.Fatalf("lost flags: %+v", result.Entries[0])
	}
	if result.Entries[1].IncludeSubdomains {
		t.Fatal("host-only cookie became domain cookie")
	}
	var encoded bytes.Buffer
	if err := Write(context.Background(), &encoded, result.Entries); err != nil {
		t.Fatal(err)
	}
	again, err := Parse(context.Background(), &encoded, Options{})
	if err != nil || again.Imported != 2 || !again.Entries[0].Cookie.HttpOnly {
		t.Fatalf("round trip failed: %+v %v", again, err)
	}
}

func TestParsePartialMalformedAndSecretRedaction(t *testing.T) {
	secret := "super-secret-cookie-value"
	input := "example.com\tFALSE\t/\tFALSE\t0\tok\tvalue\nexample.com\tMAYBE\t/\tFALSE\t0\tbad\t" + secret + "\n"
	result, err := Parse(context.Background(), strings.NewReader(input), Options{})
	if !errors.Is(err, ErrMalformed) || result.Imported != 1 || result.Skipped != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("error exposed cookie value")
	}
}

func TestParseRejectsJSONAndSupportsExpiryPolicy(t *testing.T) {
	if _, err := Parse(context.Background(), strings.NewReader(`[{"domain":"example.com"}]`), Options{}); !errors.Is(err, ErrWrongFormat) {
		t.Fatalf("got %v", err)
	}
	line := "example.com\tFALSE\t/\tFALSE\t1\told\tvalue\n"
	result, err := Parse(context.Background(), strings.NewReader(line), Options{DropExpired: true, Now: time.Unix(2, 0)})
	if err != nil || result.Imported != 0 || result.Expired != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestParseCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Parse(ctx, strings.NewReader("example.com\tFALSE\t/\tFALSE\t0\tn\tv\n"), Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("example.com\tFALSE\t/\tFALSE\t0\tn\tv\n"))
	f.Add([]byte("#HttpOnly_.example.com\tTRUE\t/\tTRUE\t1.5\tn\tv\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 2<<20 {
			t.Skip()
		}
		result, _ := Parse(context.Background(), bytes.NewReader(data), Options{})
		if result.Imported > result.Total || result.Skipped > result.Total {
			t.Fatalf("invalid counters: %+v", result)
		}
	})
}
