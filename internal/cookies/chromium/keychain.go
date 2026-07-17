package chromium

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"runtime"
)

const maxKeychainPasswordBytes = 16 << 10

// MacOSKeychain invokes Apple's security tool directly, without a shell. The
// caller must opt into import; macOS controls any authorization prompt.
type MacOSKeychain struct{}

func (MacOSKeychain) Password(ctx context.Context, item KeychainItem) ([]byte, error) {
	if runtime.GOOS != "darwin" {
		return nil, ErrUnsupportedPlatform
	}
	command := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-w", "-a", item.Account, "-s", item.Service)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil || output.Len() == 0 || output.Len() > maxKeychainPasswordBytes {
		zero(output.Bytes())
		return nil, ErrKeyUnavailable
	}
	password := append([]byte(nil), output.Bytes()...)
	zero(output.Bytes())
	password = bytes.TrimSuffix(password, []byte("\n"))
	password = bytes.TrimSuffix(password, []byte("\r"))
	if len(password) == 0 {
		return nil, ErrKeyUnavailable
	}
	return password, nil
}
