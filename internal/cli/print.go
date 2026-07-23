package cli

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

var printFieldListPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]*(?:,[A-Za-z_][A-Za-z0-9_.:-]*)*$`)

func parsePrintRules(values []string) ([]ytdlp.PrintRule, error) {
	rules := make([]ytdlp.PrintRule, 0, len(values))
	for _, input := range values {
		stage, template := ytdlp.PrintVideo, input
		if prefix, remainder, found := strings.Cut(input, ":"); found {
			if parsed, ok := parsePrintStage(prefix); ok {
				stage, template = parsed, remainder
			}
		}
		if template == "" {
			return nil, fmt.Errorf("empty --print template")
		}
		if printFieldListPattern.MatchString(template) {
			fields := strings.Split(template, ",")
			rendered := make([]string, len(fields))
			for index, field := range fields {
				rendered[index] = "%(" + field + ")s"
			}
			template = strings.Join(rendered, "\n")
		}
		rules = append(rules, ytdlp.PrintRule{Stage: stage, Template: template})
	}
	return rules, nil
}

func parsePrintStage(input string) (ytdlp.PrintStage, bool) {
	stage := ytdlp.PrintStage(input)
	switch stage {
	case ytdlp.PrintPreProcess, ytdlp.PrintAfterFilter, ytdlp.PrintVideo, ytdlp.PrintBeforeDL,
		ytdlp.PrintPostProcess, ytdlp.PrintAfterMove, ytdlp.PrintAfterVideo, ytdlp.PrintPlaylist:
		return stage, true
	default:
		return "", false
	}
}

func printRulesImplySimulation(rules []ytdlp.PrintRule) bool {
	if len(rules) == 0 {
		return false
	}
	for _, rule := range rules {
		switch rule.Stage {
		case ytdlp.PrintPreProcess, ytdlp.PrintAfterFilter, ytdlp.PrintVideo:
		default:
			return false
		}
	}
	return true
}

func appendLegacyPrintRules(
	rules []ytdlp.PrintRule,
	getURL, getTitle, getID, getThumbnail, getDescription, getDuration, getFilename, getFormat bool,
) []ytdlp.PrintRule {
	add := func(enabled bool, field, template string, optional bool) {
		if !enabled {
			return
		}
		rule := ytdlp.PrintRule{Stage: ytdlp.PrintVideo, Template: template}
		if optional {
			rule.OmitIfMissing = field
		}
		rules = append(rules, rule)
	}
	add(getTitle, "title", "%(title)s", false)
	add(getID, "id", "%(id)s", false)
	add(getURL, "urls", "%(urls)s", false)
	add(getThumbnail, "thumbnail", "%(thumbnail)s", true)
	add(getDescription, "description", "%(description)s", true)
	add(getFilename, "filename", "%(filename)s", false)
	add(getDuration, "duration_string", "%(duration_string)s", true)
	add(getFormat, "format", "%(format)s", false)
	return rules
}

func writePrintOutputs(ctx context.Context, result ytdlp.Result, writer io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, entry := range result.Entries {
		if err := writePrintOutputs(ctx, entry, writer); err != nil {
			return err
		}
	}
	for _, output := range result.Prints {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := output.Text + "\n"
		if written, err := io.WriteString(writer, line); err != nil {
			return fmt.Errorf("write print output: %w", err)
		} else if written != len(line) {
			return fmt.Errorf("write print output: %w", io.ErrShortWrite)
		}
	}
	return nil
}
