package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/ytdlp-go/ytdlp/internal/value"
	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

const maxThumbnailListingJSON = 4 << 20

var errInvalidThumbnailListing = errors.New("invalid thumbnail metadata")

func writeThumbnailListings(ctx context.Context, result ytdlp.Result, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, entry := range result.Entries {
		if err := writeThumbnailListings(ctx, entry, stdout, stderr); err != nil {
			return err
		}
	}
	table, status, err := renderThumbnailListing(result.InfoJSON)
	if err != nil {
		return err
	}
	if status != "" {
		if _, err := io.WriteString(stderr, status); err != nil {
			return fmt.Errorf("write thumbnail listing status: %w", err)
		}
	}
	if table != "" {
		if _, err := io.WriteString(stdout, table); err != nil {
			return fmt.Errorf("write thumbnail listing table: %w", err)
		}
	}
	return nil
}

func renderThumbnailListing(raw json.RawMessage) (string, string, error) {
	if len(raw) == 0 || len(raw) > maxThumbnailListingJSON {
		return "", "", errInvalidThumbnailListing
	}
	var decoded value.Value
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", "", errInvalidThumbnailListing
	}
	object, ok := decoded.Object()
	if !ok {
		return "", "", errInvalidThumbnailListing
	}
	id, _ := object.Lookup("id").StringValue()
	if id == "" {
		id = "unknown"
	}
	thumbnails, _ := object.Lookup("thumbnails").ListValue()
	if len(thumbnails) == 0 {
		return "", fmt.Sprintf("[info] No thumbnails available for %s\n", id), nil
	}
	var output strings.Builder
	table := tabwriter.NewWriter(&output, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "ID\tWidth\tHeight\tURL")
	rows := 0
	for _, item := range thumbnails {
		thumbnail, ok := item.Object()
		if !ok {
			continue
		}
		rawURL, ok := thumbnail.Lookup("url").StringValue()
		if !ok || rawURL == "" {
			continue
		}
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%s\n",
			thumbnailListingValue(thumbnail.Lookup("id"), "unknown"),
			thumbnailListingValue(thumbnail.Lookup("width"), "unknown"),
			thumbnailListingValue(thumbnail.Lookup("height"), "unknown"),
			rawURL,
		)
		rows++
	}
	if err := table.Flush(); err != nil {
		return "", "", err
	}
	if rows == 0 {
		return "", fmt.Sprintf("[info] No thumbnails available for %s\n", id), nil
	}
	return output.String(), fmt.Sprintf("[info] Available thumbnails for %s:\n", id), nil
}

func thumbnailListingValue(input value.Value, fallback string) string {
	if text, ok := input.StringValue(); ok && text != "" {
		return text
	}
	if integer, ok := input.Int(); ok {
		return strconv.FormatInt(integer, 10)
	}
	if number, ok := input.Float(); ok {
		return strconv.FormatFloat(number, 'g', -1, 64)
	}
	return fallback
}
