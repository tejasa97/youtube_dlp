package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

type Fixture struct{}

func NewFixture() Fixture { return Fixture{} }

func (Fixture) Name() string { return "fixture" }

func (Fixture) Suitable(parsed *url.URL) bool {
	return strings.HasSuffix(parsed.Path, "/page")
}

func (Fixture) Extract(ctx context.Context, request Request) (value.Info, error) {
	body, _, err := request.Transport.ReadPage(ctx, request.URL)
	if err != nil {
		return value.Info{}, err
	}
	var root value.Value
	if err := json.Unmarshal(body, &root); err != nil {
		return value.Info{}, fmt.Errorf("%w: decode fixture JSON: %v", ErrInvalidMetadata, err)
	}
	object, ok := root.Object()
	if !ok {
		return value.Info{}, fmt.Errorf("%w: root must be an object", ErrInvalidMetadata)
	}
	info := value.NewInfo(object)
	if _, ok := info.ID(); !ok {
		return value.Info{}, fmt.Errorf("%w: missing string id", ErrInvalidMetadata)
	}
	if _, ok := info.Title(); !ok {
		return value.Info{}, fmt.Errorf("%w: missing string title", ErrInvalidMetadata)
	}
	formats, ok := info.Formats()
	if !ok || len(formats) == 0 {
		return value.Info{}, fmt.Errorf("%w: missing formats", ErrInvalidMetadata)
	}
	return info, nil
}
