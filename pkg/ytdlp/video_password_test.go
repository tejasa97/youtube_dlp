package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/extractor"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

type videoPasswordCaptureExtractor struct {
	password string
	url      string
	result   extractor.Extraction
	err      error
}

func (*videoPasswordCaptureExtractor) Name() string           { return "video-password-capture" }
func (*videoPasswordCaptureExtractor) Suitable(*url.URL) bool { return true }
func (capture *videoPasswordCaptureExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	capture.password = request.VideoPassword
	capture.url = request.URL
	return capture.result, capture.err
}

func videoPasswordCaptureMedia() extractor.Extraction {
	return extractor.Media(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("password-capture")},
		value.Field{Key: "title", Value: value.String("Password capture")},
	)))
}

func TestProductVideoPasswordPropagationAndLeakage(t *testing.T) {
	const secret = "  VideoPass-AAAAA-12345-內部-密碼  "
	request := Request{
		URL:           "https://fixture.invalid/locked",
		SkipDownload:  true,
		VideoPassword: secret,
	}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("success", func(t *testing.T) {
		capture := &videoPasswordCaptureExtractor{result: videoPasswordCaptureMedia()}
		var events []Event
		operation := &operation{
			client: NewClient(WithEventHandler(func(_ context.Context, event Event) error {
				events = append(events, event)
				return nil
			})),
			request:       request,
			registry:      extractor.NewRegistry(capture),
			compatibility: compatibility,
		}
		result, err := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
		if err != nil {
			t.Fatalf("operation.process error = %v", err)
		}
		if capture.password != secret {
			t.Fatalf("extractor received %q, want exact caller value", capture.password)
		}
		if capture.url != request.URL {
			t.Fatalf("extractor URL = %q, want %q", capture.url, request.URL)
		}
		assertVideoPasswordAbsent(t, secret, string(result.InfoJSON), fmt.Sprintf("%+v", events))
	})

	t.Run("error", func(t *testing.T) {
		capture := &videoPasswordCaptureExtractor{err: errors.New("fixed extractor rejection")}
		var events []Event
		operation := &operation{
			client: NewClient(WithEventHandler(func(_ context.Context, event Event) error {
				events = append(events, event)
				return nil
			})),
			request:       request,
			registry:      extractor.NewRegistry(capture),
			compatibility: compatibility,
		}
		_, err := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
		if err == nil {
			t.Fatal("operation.process succeeded, want extractor error")
		}
		if capture.password != secret {
			t.Fatalf("extractor received %q, want exact caller value", capture.password)
		}
		assertVideoPasswordAbsent(t, secret, err.Error(), fmt.Sprintf("%+v", events))
	})
}

func TestCategorizedWrongPassword(t *testing.T) {
	const secret = "VideoPwdCategorized-AAAAA-12345"
	err := categorized("vimeo extraction", extractor.ErrWrongPassword)
	if !IsCategory(err, ErrorAuthentication) {
		t.Fatalf("categorized error = %v, want ErrorAuthentication", err)
	}
	if !errors.Is(err, extractor.ErrWrongPassword) {
		t.Fatalf("categorized error = %v, want ErrWrongPassword", err)
	}
	if errors.Is(err, extractor.ErrAuthentication) {
		t.Fatalf("categorized error = %v, must not match ErrAuthentication", err)
	}
	assertVideoPasswordAbsent(t, secret, err.Error())
}

func TestVideoPasswordValidation(t *testing.T) {
	valid := []string{
		"",
		strings.Repeat("a", maxVideoPasswordBytes),
		"  密碼 with exact spaces  ",
		"!@#$%^&*()_+-={}[]|:;<>,.?/`~",
	}
	for _, password := range valid {
		if err := validateRequestOptions(Request{VideoPassword: password}); err != nil {
			t.Fatalf("valid password rejected (bytes=%d): %v", len(password), err)
		}
	}

	const marker = "VideoPasswordValidation-AAAAA-12345"
	invalid := []struct {
		name     string
		password string
	}{
		{name: "overlong", password: marker + strings.Repeat("x", maxVideoPasswordBytes)},
		{name: "NUL", password: marker + "\x00suffix"},
		{name: "invalid UTF-8", password: marker + string([]byte{0xff})},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			err := validateRequestOptions(Request{VideoPassword: test.password})
			if !errors.Is(err, errInvalidRequestOptions) {
				t.Fatalf("validateRequestOptions error = %v, want errInvalidRequestOptions", err)
			}
			assertVideoPasswordAbsent(t, marker, err.Error())

			_, runErr := NewClient().Run(context.Background(), Request{
				URL:           "https://fixture.invalid/locked",
				SkipDownload:  true,
				VideoPassword: test.password,
			})
			if !IsCategory(runErr, ErrorInvalidInput) || !errors.Is(runErr, errInvalidRequestOptions) {
				t.Fatalf("Client.Run error = %v, want categorized invalid request options", runErr)
			}
			assertVideoPasswordAbsent(t, marker, runErr.Error())
		})
	}
}

func assertVideoPasswordAbsent(t *testing.T, secret string, surfaces ...string) {
	t.Helper()
	for _, surface := range surfaces {
		if strings.Contains(surface, secret) {
			t.Fatalf("video password leaked through product surface: %q", surface)
		}
	}
}
