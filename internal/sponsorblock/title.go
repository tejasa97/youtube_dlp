package sponsorblock

import "strings"

const (
	// DefaultChapterTitleTemplate matches the pinned yt-dlp default.
	DefaultChapterTitleTemplate = "[SponsorBlock]: %(category_names)l"
	MaxChapterTitleBytes        = 1 << 20
)

// ChapterTitleFields is the bounded metadata made available to a caller's
// chapter-title renderer after timeline arrangement.
type ChapterTitleFields struct {
	StartTime     float64
	EndTime       float64
	Category      string
	Categories    []string
	Name          string
	CategoryNames []string
}

// ChapterTitleRenderer renders one arranged SponsorBlock chapter title.
// Callers must not retain or mutate the supplied slices.
type ChapterTitleRenderer func(ChapterTitleFields) (string, error)

func renderChapterTitle(renderer ChapterTitleRenderer, fields ChapterTitleFields) (string, error) {
	fields.Categories = append([]string(nil), fields.Categories...)
	fields.CategoryNames = append([]string(nil), fields.CategoryNames...)
	var (
		title string
		err   error
	)
	if renderer == nil {
		title = "[SponsorBlock]: " + strings.Join(fields.CategoryNames, ", ")
	} else {
		title, err = renderer(fields)
	}
	if err != nil {
		return "", errorf(ErrInvalidInput, "chapter title template")
	}
	if len(title) > MaxChapterTitleBytes {
		return "", errorf(ErrInvalidInput, "chapter title too long")
	}
	return title, nil
}
