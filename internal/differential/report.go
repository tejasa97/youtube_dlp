package differential

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// WriteJSON writes the stable machine-readable report.
func WriteJSON(writer io.Writer, report Report) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

// WriteMarkdown writes a concise human review report.
func WriteMarkdown(writer io.Writer, report Report) error {
	status := "PASS"
	if !report.Equal {
		status = "FAIL"
	}
	if _, err := fmt.Fprintf(writer, "# Differential Report\n\nStatus: **%s**; differences: %d.\n", status, report.DifferenceCount); err != nil {
		return err
	}
	if len(report.AppliedRules) > 0 {
		if _, err := io.WriteString(writer, "\n## Applied rules\n\n| Path | Mode | Owner | Reason |\n| --- | --- | --- | --- |\n"); err != nil {
			return err
		}
		for _, rule := range report.AppliedRules {
			if _, err := fmt.Fprintf(writer, "| `%s` | %s | %s | %s |\n",
				escapeMarkdown(rule.Path), rule.Mode, escapeMarkdown(rule.Owner), escapeMarkdown(rule.Reason)); err != nil {
				return err
			}
		}
	}
	if report.Equal {
		return nil
	}
	if _, err := io.WriteString(writer, "\n## Differences\n\n| Path | Mode | Reason | Expected | Actual |\n| --- | --- | --- | --- | --- |\n"); err != nil {
		return err
	}
	for _, difference := range report.Differences {
		if _, err := fmt.Fprintf(writer, "| `%s` | %s | %s | `%s` | `%s` |\n",
			escapeMarkdown(difference.Path), difference.Mode, escapeMarkdown(difference.Reason),
			escapeMarkdown(difference.Expected), escapeMarkdown(difference.Actual)); err != nil {
			return err
		}
	}
	return nil
}

func escapeMarkdown(input string) string {
	input = strings.ReplaceAll(input, "|", "\\|")
	input = strings.ReplaceAll(input, "`", "\\`")
	input = strings.ReplaceAll(input, "\n", " ")
	return input
}
