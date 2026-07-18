package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type alias struct {
	template string
	nargs    int
	defined  Token
}

var aliasNamePattern = regexp.MustCompile(`^--?[A-Za-z0-9][A-Za-z0-9_-]*$`)

var presetAliases = map[string][]string{
	"mp3":   {"-f", "ba[acodec^=mp3]/ba/b", "-x", "--audio-format", "mp3"},
	"aac":   {"-f", "ba[acodec^=aac]/ba[acodec^=mp4a.40.]/ba/b", "-x", "--audio-format", "aac"},
	"mp4":   {"--merge-output-format", "mp4", "--remux-video", "mp4", "-S", "vcodec:h264,lang,quality,res,fps,hdr:12,acodec:aac"},
	"mkv":   {"--merge-output-format", "mkv", "--remux-video", "mkv"},
	"sleep": {"--sleep-subtitles", "5", "--sleep-requests", "0.75", "--sleep-interval", "10", "--max-sleep-interval", "20"},
}

// ExpandAliases resolves dynamic --alias definitions and preset aliases.
func ExpandAliases(tokens []Token, limits Limits) ([]Token, error) {
	limits = normalizeLimits(limits)
	if len(tokens) > limits.MaxTokens {
		return nil, configError(ErrorResource, "expand", "", "input token count exceeds limit", nil)
	}
	for _, token := range tokens {
		if len(token.Value) > limits.MaxTokenBytes {
			return nil, tokenError(ErrorResource, "expand", token, "token exceeds byte limit")
		}
	}
	definitions := make(map[string]alias)
	triggers := make(map[string]int)
	queue := append([]Token(nil), tokens...)
	result := make([]Token, 0, len(tokens))

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.Value == "--alias" {
			if len(queue) < 2 {
				return nil, tokenError(ErrorAlias, "expand", current, "--alias requires ALIASES and OPTIONS")
			}
			names, template := queue[0], queue[1]
			queue = queue[2:]
			nargs, err := aliasArgumentCount(template.Value)
			if err != nil {
				return nil, tokenError(ErrorAlias, "expand", template, err.Error())
			}
			for _, name := range strings.Split(names.Value, ",") {
				name = strings.TrimSpace(name)
				if !strings.HasPrefix(name, "-") {
					name = "--" + name
				}
				if !aliasNamePattern.MatchString(name) {
					return nil, tokenError(ErrorAlias, "expand", names, "invalid alias name")
				}
				definitions[name] = alias{template: template.Value, nargs: nargs, defined: names}
			}
			continue
		}
		if current.Value == "--preset-alias" || current.Value == "-t" {
			if len(queue) == 0 {
				return nil, tokenError(ErrorAlias, "expand", current, "preset alias requires a name")
			}
			name := queue[0]
			queue = queue[1:]
			values, ok := presetAliases[name.Value]
			if !ok {
				return nil, tokenError(ErrorAlias, "expand", name, "unknown preset alias")
			}
			if len(queue)+len(result)+len(values) > limits.MaxTokens {
				return nil, tokenError(ErrorResource, "expand", current, "expanded token count exceeds limit")
			}
			queue = append(stringTokens(values, current), queue...)
			continue
		}

		name, inline := splitLongOption(current.Value)
		definition, ok := definitions[name]
		if !ok {
			result = append(result, current)
			if len(result) > limits.MaxTokens {
				return nil, configError(ErrorResource, "expand", current.Source, "expanded token count exceeds limit", nil)
			}
			continue
		}
		triggers[name]++
		if triggers[name] > limits.MaxAliasTriggers {
			return nil, tokenError(ErrorRecursion, "expand", current, "alias invocation limit exceeded")
		}
		arguments := make([]string, 0, definition.nargs)
		if inline != "" {
			arguments = append(arguments, inline)
		}
		for len(arguments) < definition.nargs {
			if len(queue) == 0 {
				return nil, tokenError(ErrorAlias, "expand", current, "alias is missing an argument")
			}
			arguments = append(arguments, queue[0].Value)
			queue = queue[1:]
		}
		if definition.nargs == 0 && inline != "" {
			return nil, tokenError(ErrorAlias, "expand", current, "alias does not accept an argument")
		}
		expandedText, err := formatAlias(definition.template, arguments)
		if err != nil {
			return nil, tokenError(ErrorAlias, "expand", definition.defined, err.Error())
		}
		expanded, err := Tokenize(expandedText, current.Source, limits)
		if err != nil {
			return nil, err
		}
		for index := range expanded {
			expanded[index].Line, expanded[index].Column = current.Line, current.Column
		}
		if len(queue)+len(result)+len(expanded) > limits.MaxTokens {
			return nil, tokenError(ErrorResource, "expand", current, "expanded token count exceeds limit")
		}
		queue = append(expanded, queue...)
	}
	return result, nil
}

func splitLongOption(value string) (string, string) {
	if strings.HasPrefix(value, "--") {
		if index := strings.IndexByte(value, '='); index > 0 {
			return value[:index], value[index+1:]
		}
	}
	return value, ""
}

func aliasArgumentCount(template string) (int, error) {
	maximum := -1
	for index := 0; index < len(template); index++ {
		if template[index] == '{' {
			if index+1 < len(template) && template[index+1] == '{' {
				index++
				continue
			}
			end := strings.IndexByte(template[index+1:], '}')
			if end < 0 {
				return 0, fmt.Errorf("unclosed alias placeholder")
			}
			field := template[index+1 : index+1+end]
			value, err := strconv.Atoi(field)
			if err != nil || value < 0 || value > 99 {
				return 0, fmt.Errorf("unsupported alias placeholder")
			}
			if value > maximum {
				maximum = value
			}
			index += end + 1
		} else if template[index] == '}' {
			if index+1 < len(template) && template[index+1] == '}' {
				index++
				continue
			}
			return 0, fmt.Errorf("unmatched alias brace")
		}
	}
	return maximum + 1, nil
}

func formatAlias(template string, arguments []string) (string, error) {
	var result strings.Builder
	for index := 0; index < len(template); index++ {
		switch template[index] {
		case '{':
			if index+1 < len(template) && template[index+1] == '{' {
				result.WriteByte('{')
				index++
				continue
			}
			end := strings.IndexByte(template[index+1:], '}')
			if end < 0 {
				return "", fmt.Errorf("unclosed alias placeholder")
			}
			field := template[index+1 : index+1+end]
			argument, err := strconv.Atoi(field)
			if err != nil || argument >= len(arguments) {
				return "", fmt.Errorf("invalid alias placeholder")
			}
			result.WriteString(shellQuote(arguments[argument]))
			index += end + 1
		case '}':
			if index+1 >= len(template) || template[index+1] != '}' {
				return "", fmt.Errorf("unmatched alias brace")
			}
			result.WriteByte('}')
			index++
		default:
			result.WriteByte(template[index])
		}
	}
	return result.String(), nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'" }

func stringTokens(values []string, origin Token) []Token {
	result := make([]Token, len(values))
	for index, value := range values {
		result[index] = Token{Value: value, Source: origin.Source, Line: origin.Line, Column: origin.Column}
	}
	return result
}
