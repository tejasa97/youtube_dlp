package cli

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

var (
	printFieldListPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]*(?:,[A-Za-z_][A-Za-z0-9_.:-]*)*$`)
	printDictPattern      = regexp.MustCompile(`^\{([A-Za-z_][A-Za-z0-9_.:-]*(?:,[A-Za-z_][A-Za-z0-9_.:-]*)*)\}$`)
)

type printFileSpec struct {
	template     string
	fileTemplate string
}

func parsePrintRules(values []string) ([]ytdlp.PrintRule, error) {
	rules := make([]ytdlp.PrintRule, 0, len(values))
	for _, input := range values {
		rule, err := parsePrintRule(input, "")
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func parsePrintFileRules(specifications []printFileSpec) ([]ytdlp.PrintRule, error) {
	rules := make([]ytdlp.PrintRule, 0, len(specifications))
	for _, specification := range specifications {
		if specification.fileTemplate == "" {
			return nil, fmt.Errorf("empty --print-to-file filename template")
		}
		rule, err := parsePrintRule(specification.template, specification.fileTemplate)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func parsePrintRule(input, fileTemplate string) (ytdlp.PrintRule, error) {
	stage, template := ytdlp.PrintVideo, input
	if prefix, remainder, found := strings.Cut(input, ":"); found {
		if parsed, ok := parsePrintStage(prefix); ok {
			stage, template = parsed, remainder
		}
	}
	if template == "" {
		return ytdlp.PrintRule{}, fmt.Errorf("empty print template")
	}
	template = normalizePrintShorthand(template)
	return ytdlp.PrintRule{Stage: stage, Template: template, FileTemplate: fileTemplate}, nil
}

func normalizePrintShorthand(input string) string {
	diagnostic := strings.HasSuffix(input, "=")
	template := strings.TrimSuffix(input, "=")
	if matches := printDictPattern.FindStringSubmatch(template); matches != nil {
		expression := ".{" + matches[1] + "}"
		if diagnostic {
			return expression + " = %(" + expression + ")#j"
		}
		return "%(" + expression + ")j"
	}
	if printFieldListPattern.MatchString(template) {
		fields := strings.Split(template, ",")
		rendered := make([]string, len(fields))
		for index, field := range fields {
			if diagnostic {
				rendered[index] = field + " = %(" + field + ")#j"
			} else {
				rendered[index] = "%(" + field + ")s"
			}
		}
		return strings.Join(rendered, "\n")
	}
	return input
}

func extractPrintToFileArgs(arguments []string) ([]string, []printFileSpec, error) {
	cleaned := make([]string, 0, len(arguments))
	specifications := make([]printFileSpec, 0)
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			cleaned = append(cleaned, arguments[index:]...)
			break
		}
		if argument == "--print-to-file" {
			if index+2 >= len(arguments) {
				return nil, nil, fmt.Errorf("--print-to-file requires TEMPLATE and FILE")
			}
			specifications = append(specifications, printFileSpec{
				template: arguments[index+1], fileTemplate: arguments[index+2],
			})
			index += 2
			continue
		}
		if template, found := strings.CutPrefix(argument, "--print-to-file="); found {
			if index+1 >= len(arguments) {
				return nil, nil, fmt.Errorf("--print-to-file requires FILE")
			}
			specifications = append(specifications, printFileSpec{
				template: template, fileTemplate: arguments[index+1],
			})
			index++
			continue
		}
		cleaned = append(cleaned, argument)
	}
	return cleaned, specifications, nil
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
	foundConsole := false
	for _, rule := range rules {
		if rule.FileTemplate != "" {
			continue
		}
		foundConsole = true
		switch rule.Stage {
		case ytdlp.PrintPreProcess, ytdlp.PrintAfterFilter, ytdlp.PrintVideo:
		default:
			return false
		}
	}
	return foundConsole
}

func hasConsolePrintRules(rules []ytdlp.PrintRule) bool {
	for _, rule := range rules {
		if rule.FileTemplate == "" {
			return true
		}
	}
	return false
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
