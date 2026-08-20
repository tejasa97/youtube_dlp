package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/tejasa97/ytdlp-go/pkg/ytdlp"
)

func runExtractorDiscovery(ctx context.Context, listOnly bool, metadata []ytdlp.ExtractorMetadata, stdout, stderr io.Writer) int {
	if err := writeExtractorDiscovery(ctx, listOnly, metadata, stdout); err != nil {
		if errorsIsContext(err) {
			fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
			return 130
		}
		fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
		return 1
	}
	return 0
}

func writeExtractorDiscovery(ctx context.Context, listOnly bool, metadata []ytdlp.ExtractorMetadata, writer io.Writer) error {
	for _, entry := range metadata {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := entry.Name
		if listOnly {
			if err := writeExtractorDiscoveryLine(writer, line); err != nil {
				return err
			}
			for _, rawURL := range entry.URLs {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := writeExtractorDiscoveryLine(writer, "  "+rawURL); err != nil {
					return err
				}
			}
			continue
		}
		if !listOnly && entry.Description != "" {
			line += ": " + entry.Description
		}
		if err := writeExtractorDiscoveryLine(writer, line); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func writeExtractorDiscoveryLine(writer io.Writer, line string) error {
	if written, err := io.WriteString(writer, line+"\n"); err != nil {
		return fmt.Errorf("write extractor discovery: %w", err)
	} else if written != len(line)+1 {
		return fmt.Errorf("write extractor discovery: %w", io.ErrShortWrite)
	}
	return nil
}

func errorsIsContext(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
