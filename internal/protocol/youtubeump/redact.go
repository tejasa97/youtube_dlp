package youtubeump

import (
	"strings"

	"github.com/tejasa97/youtube_dlp/internal/network"
)

func redactURL(raw string) string {
	return network.RedactRawURL(raw)
}

func redactError(err error) error {
	if err == nil {
		return nil
	}
	return &redactedError{err: err}
}

type redactedError struct {
	err error
}

func (err *redactedError) Error() string {
	return redactMessage(err.err.Error())
}

func (err *redactedError) Unwrap() error { return err.err }

func redactMessage(message string) string {
	if message == "" {
		return message
	}
	parts := strings.Fields(message)
	for index, part := range parts {
		if strings.Contains(part, "googlevideo.com") || strings.Contains(part, "sig=") || strings.Contains(part, "pot=") {
			parts[index] = network.RedactRawURL(part)
		}
	}
	return strings.Join(parts, " ")
}
