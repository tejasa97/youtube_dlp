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
		{name: "valid name punctuation", host: "example.com", key: "__Host-session_id.v2|~", value: "value", path: "/", want: true},
		{name: "valid value whitespace and comma", host: "example.com", key: "sid", value: "value with spaces,comma", path: "/path with spaces", want: true},
		{name: "valid empty value", host: "example.com", key: "sid", value: "", path: "/", want: true},
		{name: "empty host", host: "", key: "sid", value: "value", path: "/"},
		{name: "empty name", host: "example.com", key: "", value: "value", path: "/"},
		{name: "missing leading slash", host: "example.com", key: "sid", value: "value", path: "tmp"},
		{name: "host whitespace", host: "example.com forged", key: "sid", value: "value", path: "/"},
		{name: "control host", host: "example.com\nforged", key: "sid", value: "value", path: "/"},
		{name: "control name", host: "example.com", key: "sid\x00forged", value: "value", path: "/"},
		{name: "name whitespace", host: "example.com", key: "sid name", value: "value", path: "/"},
		{name: "name tab", host: "example.com", key: "sid\tname", value: "value", path: "/"},
		{name: "name separator", host: "example.com", key: "sid;extra", value: "value", path: "/"},
		{name: "value tab", host: "example.com", key: "sid", value: "value\tforged", path: "/"},
		{name: "value controls", host: "example.com", key: "sid", value: "value\r\nforged", path: "/"},
		{name: "value delete", host: "example.com", key: "sid", value: "value\x7fforged", path: "/"},
		{name: "value quote", host: "example.com", key: "sid", value: `value"forged`, path: "/"},
		{name: "value separator", host: "example.com", key: "sid", value: "value;forged", path: "/"},
		{name: "value backslash", host: "example.com", key: "sid", value: `value\forged`, path: "/"},
		{name: "path control", host: "example.com", key: "sid", value: "value", path: "/\x00"},
		{name: "path separator", host: "example.com", key: "sid", value: "value", path: "/;param"},
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
