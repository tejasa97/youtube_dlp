package plugin

import (
	"errors"
	"strings"
	"testing"
)

func TestNegotiateAndPermissions(t *testing.T) {
	version, err := Negotiate([]uint32{1, 2}, []uint32{2, 1})
	if err != nil || version != 2 {
		t.Fatalf("Negotiate() = %d, %v", version, err)
	}
	if _, err := Negotiate([]uint32{1}, []uint32{2}); !errors.Is(err, ErrIncompatibleVersion) {
		t.Fatalf("Negotiate incompatible error = %v", err)
	}
	if err := CheckPermissions([]Permission{PermissionNetwork}, []Permission{PermissionNetwork}); err != nil {
		t.Fatal(err)
	}
	if err := CheckPermissions([]Permission{PermissionSecrets}, nil); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("CheckPermissions() error = %v", err)
	}
}

func TestRemoteFailureDiagnosticRedaction(t *testing.T) {
	failure := &RemoteFailure{Detail: RemoteError{Category: RemoteNetwork, Message: "token=fixture-secret visible=yes"}}
	if rendered := failure.Error(); strings.Contains(rendered, "fixture-secret") || !strings.Contains(rendered, "visible=yes") {
		t.Fatalf("RemoteFailure.Error() = %q", rendered)
	}
}
