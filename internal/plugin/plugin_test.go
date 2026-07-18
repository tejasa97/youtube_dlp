package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type approvalFunc func(context.Context, ApprovalRequest) (Approval, error)

func (function approvalFunc) Approve(ctx context.Context, request ApprovalRequest) (Approval, error) {
	return function(ctx, request)
}

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

func TestApproveRequiresExactPermissionSet(t *testing.T) {
	request := ApprovalRequest{Requested: []Permission{PermissionNetwork}}
	approver := approvalFunc(func(context.Context, ApprovalRequest) (Approval, error) {
		return Approval{Granted: []Permission{PermissionNetwork, PermissionCookies}}, nil
	})
	if err := Approve(context.Background(), approver, request, nil); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("superset approval error = %v", err)
	}
	approver = func(context.Context, ApprovalRequest) (Approval, error) {
		return Approval{Granted: []Permission{PermissionNetwork}}, nil
	}
	if err := Approve(context.Background(), approver, request, nil); err != nil {
		t.Fatalf("exact approval error = %v", err)
	}
	if err := Approve(context.Background(), nil, request, []Permission{PermissionNetwork, PermissionCookies}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("static superset error = %v", err)
	}
	tests := []struct {
		name      string
		requested []Permission
		granted   []Permission
	}{
		{"duplicate requested", []Permission{PermissionNetwork, PermissionNetwork}, []Permission{PermissionNetwork}},
		{"duplicate granted", []Permission{PermissionNetwork}, []Permission{PermissionNetwork, PermissionNetwork}},
		{"unknown requested", []Permission{"unknown"}, []Permission{"unknown"}},
		{"unknown granted", []Permission{PermissionNetwork}, []Permission{"unknown"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			approver := approvalFunc(func(context.Context, ApprovalRequest) (Approval, error) {
				return Approval{Granted: test.granted}, nil
			})
			if err := Approve(context.Background(), approver, ApprovalRequest{Requested: test.requested}, nil); !errors.Is(err, ErrPermissionDenied) {
				t.Fatalf("approval error = %v", err)
			}
		})
	}
}

func TestRemoteFailureDiagnosticRedaction(t *testing.T) {
	err := ResponseError(&RemoteError{Category: RemoteNetwork, Message: "token=fixture-secret visible=yes"})
	failure := new(RemoteFailure)
	if !errors.As(err, &failure) || strings.Contains(failure.Detail.Message, "fixture-secret") {
		t.Fatalf("ResponseError() = %#v, %v", failure, err)
	}
	if rendered := failure.Error(); strings.Contains(rendered, "fixture-secret") || !strings.Contains(rendered, "visible=yes") {
		t.Fatalf("RemoteFailure.Error() = %q", rendered)
	}
}

func TestCheckPayloadRejectsSecretsAndBounds(t *testing.T) {
	if err := CheckPayload(map[string]any{"title": "fixture", "formats": []any{map[string]any{"url": "https://fixture.invalid/video"}}}); err != nil {
		t.Fatal(err)
	}
	if err := CheckPayload(map[string]any{"http_headers": map[string]any{"Authorization": "fixture"}}); !errors.Is(err, ErrSecretExposure) {
		t.Fatalf("secret-key error = %v", err)
	}
	if err := CheckPayload(map[string]any{"title": "token=fixture-secret"}); !errors.Is(err, ErrSecretExposure) {
		t.Fatalf("secret-value error = %v", err)
	}
	if err := CheckPayload(map[string]any{"nested": map[string]string{"token": "fixture-secret"}}); !errors.Is(err, ErrSecretExposure) {
		t.Fatalf("typed nested-map error = %v", err)
	}
	var nested any = "leaf"
	for range 66 {
		nested = []any{nested}
	}
	if err := CheckPayload(nested); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("nesting error = %v", err)
	}
}
