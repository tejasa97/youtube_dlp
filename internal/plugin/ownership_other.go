//go:build !unix

package plugin

import (
	"fmt"
	"os"
)

// FileMode does not prove owner/ACL security on these platforms. Secure
// discovery therefore fails closed until a platform ACL verifier is provided.
func verifyTrustedOwner(os.FileInfo) error {
	return fmt.Errorf("%w: platform ownership verification unavailable", ErrUntrustedPath)
}
