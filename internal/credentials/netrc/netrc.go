// Package netrc parses and securely loads bounded native netrc credential
// stores. It never executes macros or external commands.
package netrc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

const (
	defaultMaxBytes      = int64(1 << 20)
	defaultMaxEntries    = 1024
	defaultMaxTokenBytes = 8 << 10
	defaultMaxMacros     = 32
	defaultMaxMacroBytes = 64 << 10
	hardMaxBytes         = int64(16 << 20)
	hardMaxEntries       = 10_000
	hardMaxTokenBytes    = 64 << 10
	hardMaxMacros        = 256
	hardMaxMacroBytes    = 1 << 20
)

var (
	ErrSyntax      = errors.New("invalid netrc syntax")
	ErrLimit       = errors.New("netrc resource limit exceeded")
	ErrUnsafeFile  = errors.New("unsafe netrc file")
	ErrInvalidHost = errors.New("invalid netrc lookup host")
	ErrIO          = errors.New("netrc I/O failure")
)

type Limits struct {
	MaxBytes      int64
	MaxEntries    int
	MaxTokenBytes int
	MaxMacros     int
	MaxMacroBytes int
}

func (limits Limits) normalized() (Limits, error) {
	if limits.MaxBytes < 0 || limits.MaxEntries < 0 || limits.MaxTokenBytes < 0 || limits.MaxMacros < 0 || limits.MaxMacroBytes < 0 {
		return Limits{}, ErrLimit
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = defaultMaxBytes
	}
	if limits.MaxEntries == 0 {
		limits.MaxEntries = defaultMaxEntries
	}
	if limits.MaxTokenBytes == 0 {
		limits.MaxTokenBytes = defaultMaxTokenBytes
	}
	if limits.MaxMacros == 0 {
		limits.MaxMacros = defaultMaxMacros
	}
	if limits.MaxMacroBytes == 0 {
		limits.MaxMacroBytes = defaultMaxMacroBytes
	}
	if limits.MaxBytes > hardMaxBytes || limits.MaxEntries > hardMaxEntries || limits.MaxTokenBytes > hardMaxTokenBytes || limits.MaxMacros > hardMaxMacros || limits.MaxMacroBytes > hardMaxMacroBytes {
		return Limits{}, ErrLimit
	}
	return limits, nil
}

// Credential contains one authentication tuple. String and GoString are
// deliberately redacted to prevent accidental diagnostic disclosure.
type Credential struct {
	Login    string
	Account  string
	Password string
}

func (Credential) String() string   { return "[redacted netrc credential]" }
func (Credential) GoString() string { return "netrc.Credential{[redacted]}" }

type Store struct {
	machines          map[string]Credential
	defaultCredential Credential
	hasDefault        bool
}

func (store *Store) Count() int {
	if store == nil {
		return 0
	}
	return len(store.machines)
}

// Lookup tries an explicit canonical host:port, then the canonical host, then
// the default entry. Service aliases that are not hostnames remain exact and
// case-sensitive, matching yt-dlp extractor machine keys.
func (store *Store) Lookup(ctx context.Context, authority string) (Credential, bool, error) {
	if err := ctx.Err(); err != nil {
		return Credential{}, false, err
	}
	if store == nil {
		return Credential{}, false, nil
	}
	candidates, err := lookupCandidates(authority)
	if err != nil {
		return Credential{}, false, err
	}
	for _, candidate := range candidates {
		if credential, ok := store.machines[candidate]; ok {
			return credential, true, nil
		}
	}
	if store.hasDefault {
		return store.defaultCredential, true, nil
	}
	return Credential{}, false, nil
}

func Parse(ctx context.Context, reader io.Reader, limits Limits) (*Store, error) {
	normalized, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	payload, err := readBounded(ctx, reader, normalized.MaxBytes)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(payload) {
		return nil, syntaxError(1, "invalid encoding")
	}
	parser := parser{
		lexer:  lexer{data: payload, line: 1, maxTokenBytes: normalized.MaxTokenBytes},
		limits: normalized,
		store:  &Store{machines: make(map[string]Credential)},
		ctx:    ctx,
	}
	if err := parser.parse(); err != nil {
		return nil, err
	}
	return parser.store, nil
}

func readBounded(ctx context.Context, reader io.Reader, maximum int64) ([]byte, error) {
	if reader == nil {
		return nil, syntaxError(1, "missing input")
	}
	var output bytes.Buffer
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		read, err := reader.Read(buffer)
		if read > 0 {
			if int64(output.Len()+read) > maximum {
				return nil, ErrLimit
			}
			_, _ = output.Write(buffer[:read])
		}
		if errors.Is(err, io.EOF) {
			return output.Bytes(), nil
		}
		if err != nil {
			return nil, ErrIO
		}
		if read == 0 {
			return nil, fmt.Errorf("%w: reader made no progress", ErrSyntax)
		}
	}
}

type parser struct {
	lexer   lexer
	limits  Limits
	store   *Store
	ctx     context.Context
	pushed  *token
	entries int
	macros  int
}

func (parser *parser) parse() error {
	for {
		if err := parser.ctx.Err(); err != nil {
			return err
		}
		top, ok, err := parser.next()
		if err != nil || !ok {
			return err
		}
		switch top.text {
		case "machine":
			name, ok, err := parser.next()
			if err != nil {
				return err
			}
			if !ok {
				return syntaxError(top.line, "missing machine name")
			}
			key, err := normalizeMachine(name.text)
			if err != nil {
				return syntaxError(name.line, "invalid machine name")
			}
			if err := parser.parseEntry(key, false, top.line); err != nil {
				return err
			}
		case "default":
			if err := parser.parseEntry("default", true, top.line); err != nil {
				return err
			}
		case "macdef":
			name, ok, err := parser.next()
			if err != nil {
				return err
			}
			if !ok || name.text == "" {
				return syntaxError(top.line, "missing macro name")
			}
			parser.macros++
			if parser.macros > parser.limits.MaxMacros {
				return ErrLimit
			}
			if err := parser.lexer.skipMacro(parser.limits.MaxMacroBytes); err != nil {
				return err
			}
		default:
			return syntaxError(top.line, "unexpected top-level field")
		}
	}
}

func (parser *parser) parseEntry(key string, isDefault bool, line int) error {
	parser.entries++
	if parser.entries > parser.limits.MaxEntries {
		return ErrLimit
	}
	credential := Credential{}
	passwordSet := false
	for {
		field, ok, err := parser.next()
		if err != nil {
			return err
		}
		if !ok || field.text == "machine" || field.text == "default" || field.text == "macdef" {
			if !passwordSet {
				return syntaxError(line, "entry has no password")
			}
			if ok {
				parser.pushed = &field
			}
			if isDefault {
				parser.store.defaultCredential = credential
				parser.store.hasDefault = true
			} else {
				// Python netrc and yt-dlp use the final duplicate definition.
				parser.store.machines[key] = credential
			}
			return nil
		}
		var destination *string
		switch field.text {
		case "login", "user":
			destination = &credential.Login
		case "account":
			destination = &credential.Account
		case "password":
			destination = &credential.Password
			passwordSet = true
		default:
			return syntaxError(field.line, "unknown entry field")
		}
		value, ok, err := parser.next()
		if err != nil {
			return err
		}
		if !ok {
			return syntaxError(field.line, "missing entry value")
		}
		*destination = value.text
	}
}

func (parser *parser) next() (token, bool, error) {
	if parser.pushed != nil {
		result := *parser.pushed
		parser.pushed = nil
		return result, true, nil
	}
	return parser.lexer.next()
}

type token struct {
	text string
	line int
}

type lexer struct {
	data          []byte
	index         int
	line          int
	maxTokenBytes int
}

func (lexer *lexer) next() (token, bool, error) {
	for lexer.index < len(lexer.data) {
		character := lexer.data[lexer.index]
		if isNetrcWhitespace(character) {
			lexer.advance(character)
			continue
		}
		if character == '#' {
			lexer.skipComment()
			continue
		}
		break
	}
	if lexer.index >= len(lexer.data) {
		return token{}, false, nil
	}
	line := lexer.line
	var result strings.Builder
	quote := byte(0)
	started := false
	for lexer.index < len(lexer.data) {
		character := lexer.data[lexer.index]
		if quote == 0 && isNetrcWhitespace(character) {
			break
		}
		started = true
		if quote == 0 && (character == '\'' || character == '"') {
			quote = character
			lexer.index++
			continue
		}
		if quote != 0 && character == quote {
			quote = 0
			lexer.index++
			continue
		}
		if character == '\\' && quote != '\'' {
			lexer.index++
			if lexer.index >= len(lexer.data) {
				return token{}, false, syntaxError(line, "trailing escape")
			}
			character = lexer.data[lexer.index]
			if character == '\n' {
				lexer.advance(character)
				continue
			}
		}
		if character == 0 {
			return token{}, false, syntaxError(line, "NUL in token")
		}
		result.WriteByte(character)
		if result.Len() > lexer.maxTokenBytes {
			return token{}, false, ErrLimit
		}
		lexer.advance(character)
	}
	if quote != 0 {
		return token{}, false, syntaxError(line, "unterminated quote")
	}
	if !started {
		return token{}, false, syntaxError(line, "empty lexer state")
	}
	return token{text: result.String(), line: line}, true, nil
}

func (lexer *lexer) skipComment() {
	for lexer.index < len(lexer.data) {
		character := lexer.data[lexer.index]
		lexer.advance(character)
		if character == '\n' {
			return
		}
	}
}

// skipMacro ignores a macdef body through the first blank or whitespace-only
// physical line. Macro bytes are never tokenized, stored, or executed.
func (lexer *lexer) skipMacro(maximum int) error {
	start := lexer.index
	for lexer.index < len(lexer.data) && lexer.data[lexer.index] != '\n' {
		lexer.advance(lexer.data[lexer.index])
	}
	if lexer.index < len(lexer.data) {
		lexer.advance('\n')
	}
	for lexer.index < len(lexer.data) {
		lineStart := lexer.index
		for lexer.index < len(lexer.data) && lexer.data[lexer.index] != '\n' {
			lexer.advance(lexer.data[lexer.index])
		}
		line := bytes.TrimSpace(lexer.data[lineStart:lexer.index])
		if lexer.index < len(lexer.data) {
			lexer.advance('\n')
		}
		if lexer.index-start > maximum {
			return ErrLimit
		}
		if len(line) == 0 {
			return nil
		}
	}
	return nil
}

func (lexer *lexer) advance(character byte) {
	lexer.index++
	if character == '\n' {
		lexer.line++
	}
}

func isNetrcWhitespace(character byte) bool {
	return character == ' ' || character == '\t' || character == '\r' || character == '\n'
}

func syntaxError(line int, reason string) error {
	return fmt.Errorf("%w at line %d: %s", ErrSyntax, line, reason)
}

func normalizeMachine(machine string) (string, error) {
	if machine == "" || len(machine) > hardMaxTokenBytes || strings.IndexFunc(machine, func(r rune) bool { return r == 0 || r == '\r' || r == '\n' }) >= 0 {
		return "", ErrInvalidHost
	}
	if candidates, err := canonicalAuthority(machine); err == nil {
		return candidates[0], nil
	}
	if machine[0] == '-' || machine[0] == '_' {
		return "", ErrInvalidHost
	}
	for _, character := range machine {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune(".-_", character)) {
			return "", ErrInvalidHost
		}
	}
	return machine, nil
}

func lookupCandidates(authority string) ([]string, error) {
	if authority == "" || strings.TrimSpace(authority) != authority || strings.ContainsAny(authority, "/?#@\r\n\x00") {
		return nil, ErrInvalidHost
	}
	if canonical, err := canonicalAuthority(authority); err == nil {
		return canonical, nil
	}
	alias, err := normalizeMachine(authority)
	if err != nil {
		return nil, ErrInvalidHost
	}
	return []string{alias}, nil
}

func canonicalAuthority(authority string) ([]string, error) {
	host := authority
	port := ""
	if strings.HasPrefix(authority, "[") {
		var err error
		host, port, err = net.SplitHostPort(authority)
		if err != nil {
			return nil, err
		}
	} else if strings.Count(authority, ":") == 1 {
		separator := strings.LastIndexByte(authority, ':')
		if separator <= 0 {
			return nil, ErrInvalidHost
		}
		host, port = authority[:separator], authority[separator+1:]
	} else if strings.Count(authority, ":") > 1 {
		if parsed := net.ParseIP(authority); parsed != nil {
			return []string{parsed.String()}, nil
		}
		return nil, ErrInvalidHost
	}
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
			return nil, ErrInvalidHost
		}
	}
	host = strings.TrimSuffix(host, ".")
	if parsed := net.ParseIP(host); parsed != nil {
		host = parsed.String()
	} else {
		ascii, err := idna.Lookup.ToASCII(host)
		if err != nil || ascii == "" || !strings.Contains(ascii, ".") {
			return nil, ErrInvalidHost
		}
		host = strings.ToLower(ascii)
	}
	if port == "" {
		return []string{host}, nil
	}
	exact := net.JoinHostPort(host, port)
	if net.ParseIP(host) == nil {
		exact = host + ":" + port
	}
	return []string{exact, host}, nil
}
