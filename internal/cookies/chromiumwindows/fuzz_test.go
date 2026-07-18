package chromiumwindows

import (
	"context"
	"testing"
)

func FuzzDecryptBoundaries(f *testing.F) {
	f.Add([]byte("v10short"), "example.test", uint16(24))
	f.Add([]byte("v20payload"), "example.test", uint16(23))
	f.Add([]byte("legacy"), "x.test", uint16(24))
	f.Fuzz(func(t *testing.T, blob []byte, host string, version uint16) {
		if len(blob) > 1<<20 || len(host) > 1024 {
			t.Skip()
		}
		decryptor := cookieDecryptor{protector: fakeProtector{wrapped: testKey, legacy: []byte("value")}, appBound: fakeAppBound{value: []byte("value")}, masterKey: append([]byte(nil), testKey...), masterLoaded: true}
		_, _, _ = decryptor.decrypt(context.Background(), blob, host, int(version))
		decryptor.close()
	})
}
