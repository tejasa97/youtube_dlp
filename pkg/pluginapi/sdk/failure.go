package sdk

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode"

	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

var (
	codePattern       = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_.-]{0,127})$`)
	sensitivePattern  = regexp.MustCompile(`(?i)\b(authorization|cookie|password|secret|signature|token|api[_-]?key|key)([=:][^&[:space:]]+)`)
	allowedCategories = map[pluginapi.RemoteCategory]struct{}{
		pluginapi.RemoteAuthentication: {}, pluginapi.RemoteUnavailable: {}, pluginapi.RemoteInvalidMetadata: {},
		pluginapi.RemoteNetwork: {}, pluginapi.RemotePermission: {}, pluginapi.RemoteInvalidInput: {}, pluginapi.RemoteInternal: {},
	}
)

// Failure is an explicitly public, categorized error safe to return to the
// host. Cause is retained for errors.Is/errors.As inside the plugin process but
// is never serialized or included in Error's text.
type Failure struct {
	Category  pluginapi.RemoteCategory
	Code      string
	Message   string
	Retryable bool
	Cause     error
}

func (failure *Failure) Error() string {
	if failure == nil {
		return "plugin operation failed"
	}
	return "plugin operation failed: " + string(failure.Category)
}

func (failure *Failure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.Cause
}

func remoteError(err error) *pluginapi.RemoteError {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &pluginapi.RemoteError{Category: pluginapi.RemoteUnavailable, Code: "canceled", Message: "plugin operation canceled", Retryable: true}
	}
	var failure *Failure
	if errors.As(err, &failure) && failure != nil {
		remote := &pluginapi.RemoteError{Category: failure.Category, Code: failure.Code, Message: failure.Message, Retryable: failure.Retryable}
		if validateRemoteError(remote) == nil {
			return remote
		}
	}
	return internalFailure("handler_error")
}

func internalFailure(code string) *pluginapi.RemoteError {
	return &pluginapi.RemoteError{Category: pluginapi.RemoteInternal, Code: code, Message: "plugin operation failed"}
}

func validateRemoteError(remote *pluginapi.RemoteError) error {
	if remote == nil {
		return nil
	}
	if _, allowed := allowedCategories[remote.Category]; !allowed || remote.Code != "" && !codePattern.MatchString(remote.Code) ||
		strings.TrimSpace(remote.Message) == "" || len(remote.Message) > 512 || strings.IndexFunc(remote.Message, unicode.IsControl) >= 0 ||
		sensitivePattern.MatchString(remote.Message) {
		return ErrProtocol
	}
	return nil
}
