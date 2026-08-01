package validate

import "testing"

func TestCookieFieldsBoundsAndHeaderSafety(t *testing.T) {
	tests := []struct {
		name  string
		host  string
		key   string
		value string
		path  string
		want  bool
	}{
		{name: "valid", host: ".example.com", key: "sid", value: "value", path: "/", want: true},
		{name: "empty host", host: "", key: "sid", value: "value", path: "/"},
		{name: "missing leading slash", host: "example.com", key: "sid", value: "value", path: "tmp"},
		{name: "control host", host: "example.com\nforged", key: "sid", value: "value", path: "/"},
		{name: "oversized value", host: "example.com", key: "sid", value: string(make([]byte, MaxValueBytes+1)), path: "/"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CookieFields(test.host, test.key, test.value, test.path); got != test.want {
				t.Fatalf("CookieFields() = %v, want %v", got, test.want)
			}
		})
	}
}

func FuzzCookieFields(f *testing.F) {
	f.Add(".example.com", "sid", "value", "/")
	f.Fuzz(func(t *testing.T, host, name, value, path string) {
		if len(host)+len(name)+len(value)+len(path) > 2<<20 {
			t.Skip()
		}
		_ = CookieFields(host, name, value, path)
	})
}
