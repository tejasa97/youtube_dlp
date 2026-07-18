// Package firefox imports cookies from Firefox profiles without Python or cgo.
package firefox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

const maxContainerFile = 4 << 20

var (
	ErrNotFound         = errors.New("Firefox cookie database not found")
	ErrInvalidDatabase  = errors.New("invalid Firefox cookie database")
	ErrUnsafePath       = errors.New("unsafe Firefox cookie database path")
	ErrSnapshot         = errors.New("Firefox cookie database snapshot failed")
	ErrContainerMissing = errors.New("Firefox cookie container not found")
	ErrLimit            = errors.New("Firefox cookie import exceeds safety limits")
)

type Options struct {
	Profile      string
	ProfileDir   string
	DatabasePath string
	Container    string // empty: all; "none": cookies outside containers
	TempRoot     string
	MaxCookies   int
}

type Result struct {
	Cookies       []*http.Cookie
	SchemaVersion int
	Total         int
	Imported      int
	Session       int
	Persistent    int
	ContainerID   *int
}

func Import(ctx context.Context, options Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	path, err := locate(options)
	if err != nil {
		return Result{}, err
	}
	snapshot, cleanup, err := copySnapshot(ctx, path, options.TempRoot)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	db, err := sql.Open("sqlite3", sqliteURI(snapshot))
	if err != nil {
		return Result{}, ErrInvalidDatabase
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return Result{}, categorizeDB(ctx, err)
	}

	var schema int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil || schema < 0 || schema > 100_000 {
		return Result{}, categorizeDB(ctx, err)
	}
	columns, err := tableColumns(ctx, db)
	if err != nil {
		return Result{}, err
	}
	for _, required := range []string{"host", "name", "value", "path", "expiry", "issecure"} {
		if !columns[required] {
			return Result{}, ErrInvalidDatabase
		}
	}
	containerID, err := resolveContainer(ctx, options, filepath.Dir(path))
	if err != nil {
		return Result{}, err
	}
	query, args := firefoxQuery(columns, options.Container, containerID)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return Result{}, categorizeDB(ctx, err)
	}
	defer rows.Close()
	maxCookies := options.MaxCookies
	if maxCookies <= 0 {
		maxCookies = 1_000_000
	}
	result := Result{SchemaVersion: schema, ContainerID: containerID}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		var host, name, value, path string
		var expiry sql.NullInt64
		var secure, httpOnly, sameSite int
		if err := rows.Scan(&host, &name, &value, &path, &expiry, &secure, &httpOnly, &sameSite); err != nil {
			return result, ErrInvalidDatabase
		}
		result.Total++
		if result.Total > maxCookies {
			return result, ErrLimit
		}
		if !validCookie(host, name, value, path) {
			return result, ErrInvalidDatabase
		}
		cookie := &http.Cookie{Name: name, Value: value, Domain: host, Path: path, Secure: secure != 0, HttpOnly: httpOnly != 0, SameSite: firefoxSameSite(sameSite)}
		if !expiry.Valid || expiry.Int64 == 0 {
			result.Session++
		} else {
			seconds := expiry.Int64
			if schema >= 16 {
				seconds /= 1000
			}
			cookie.Expires = time.Unix(seconds, 0)
			result.Persistent++
		}
		result.Cookies = append(result.Cookies, cookie)
		result.Imported++
	}
	if err := rows.Err(); err != nil {
		return result, categorizeDB(ctx, err)
	}
	return result, nil
}

func locate(options Options) (string, error) {
	if options.DatabasePath != "" {
		return validateDatabase(options.DatabasePath)
	}
	var roots []string
	if options.ProfileDir != "" {
		roots = []string{options.ProfileDir}
	} else {
		if options.Profile != "" && (options.Profile == "." || options.Profile == ".." || filepath.Base(options.Profile) != options.Profile) {
			return "", ErrUnsafePath
		}
		for _, root := range defaultRoots() {
			if options.Profile != "" {
				root = filepath.Join(root, options.Profile)
			}
			roots = append(roots, root)
		}
	}
	var candidates []string
	for _, root := range roots {
		candidates = append(candidates, filepath.Join(root, "cookies.sqlite"))
		matches, _ := filepath.Glob(filepath.Join(root, "*", "cookies.sqlite"))
		candidates = append(candidates, matches...)
		matches, _ = filepath.Glob(filepath.Join(root, "Profiles", "*", "cookies.sqlite"))
		candidates = append(candidates, matches...)
	}
	type candidate struct {
		path     string
		modified time.Time
	}
	var valid []candidate
	for _, path := range candidates {
		info, err := os.Lstat(path)
		if err == nil && info.Mode().IsRegular() {
			valid = append(valid, candidate{path, info.ModTime()})
		}
	}
	if len(valid) == 0 {
		return "", ErrNotFound
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].modified.After(valid[j].modified) })
	return valid[0].path, nil
}

func validateDatabase(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", ErrNotFound
	}
	if err != nil || !info.Mode().IsRegular() {
		return "", ErrUnsafePath
	}
	return path, nil
}

func defaultRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support", "Firefox", "Profiles")}
	case "windows":
		var roots []string
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			roots = append(roots, filepath.Join(appdata, "Mozilla", "Firefox", "Profiles"))
		}
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			roots = append(roots, filepath.Join(local, "Packages", "Mozilla.Firefox_n80bbvh6b1yt2", "LocalCache", "Roaming", "Mozilla", "Firefox", "Profiles"))
		}
		return roots
	default:
		config := os.Getenv("XDG_CONFIG_HOME")
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		return []string{filepath.Join(config, "mozilla", "firefox"), filepath.Join(home, ".mozilla", "firefox"), filepath.Join(home, ".var", "app", "org.mozilla.firefox", "config", "mozilla", "firefox"), filepath.Join(home, ".var", "app", "org.mozilla.firefox", ".mozilla", "firefox"), filepath.Join(home, "snap", "firefox", "common", ".mozilla", "firefox")}
	}
}

func copySnapshot(ctx context.Context, source, tempRoot string) (string, func(), error) {
	dir, err := os.MkdirTemp(tempRoot, "ytdlp-firefox-")
	if err != nil {
		return "", func() {}, ErrSnapshot
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	destination := filepath.Join(dir, "cookies.sqlite")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := copyRegular(ctx, source+suffix, destination+suffix, suffix == ""); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	return destination, cleanup, nil
}

func copyRegular(ctx context.Context, source, destination string, required bool) error {
	info, err := os.Lstat(source)
	if os.IsNotExist(err) && !required {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return ErrSnapshot
	}
	input, err := os.Open(source)
	if err != nil {
		return ErrSnapshot
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ErrSnapshot
	}
	defer output.Close()
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := input.Read(buffer)
		if n > 0 {
			if _, err := output.Write(buffer[:n]); err != nil {
				return ErrSnapshot
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return ErrSnapshot
		}
	}
}

func sqliteURI(path string) string {
	u := &url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "rw")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(1000)")
	u.RawQuery = q.Encode()
	return u.String()
}

func tableColumns(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(moz_cookies)")
	if err != nil {
		return nil, categorizeDB(ctx, err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var index, notNull, pk int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&index, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return nil, ErrInvalidDatabase
		}
		columns[strings.ToLower(name)] = true
	}
	if len(columns) == 0 || rows.Err() != nil {
		return nil, ErrInvalidDatabase
	}
	return columns, nil
}

func firefoxQuery(columns map[string]bool, container string, id *int) (string, []any) {
	httpOnly := "0"
	if columns["ishttponly"] {
		httpOnly = "isHttpOnly"
	}
	sameSite := "0"
	if columns["samesite"] {
		sameSite = "sameSite"
	}
	query := "SELECT host,name,value,path,expiry,isSecure," + httpOnly + "," + sameSite + " FROM moz_cookies"
	if columns["originattributes"] {
		if id != nil {
			return query + " WHERE originAttributes LIKE ? OR originAttributes LIKE ?", []any{fmt.Sprintf("%%userContextId=%d", *id), fmt.Sprintf("%%userContextId=%d&%%", *id)}
		}
		if container == "none" {
			return query + " WHERE NOT INSTR(originAttributes,'userContextId=')", nil
		}
	}
	return query, nil
}

func resolveContainer(ctx context.Context, options Options, profileDir string) (*int, error) {
	if options.Container == "" || options.Container == "none" {
		return nil, nil
	}
	file, err := os.Open(filepath.Join(profileDir, "containers.json"))
	if err != nil {
		return nil, ErrContainerMissing
	}
	defer file.Close()
	reader := io.LimitReader(file, maxContainerFile+1)
	raw, err := io.ReadAll(reader)
	if err != nil || len(raw) > maxContainerFile {
		return nil, ErrContainerMissing
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var doc struct {
		Identities []struct {
			Name          string `json:"name"`
			L10nID        string `json:"l10nID"`
			UserContextID int    `json:"userContextId"`
		} `json:"identities"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil, ErrContainerMissing
	}
	for _, identity := range doc.Identities {
		label := strings.TrimSuffix(strings.TrimPrefix(identity.L10nID, "userContext"), ".label")
		if identity.Name == options.Container || label == options.Container {
			id := identity.UserContextID
			return &id, nil
		}
	}
	return nil, ErrContainerMissing
}

func firefoxSameSite(value int) http.SameSite {
	switch value {
	case 1:
		return http.SameSiteLaxMode
	case 2:
		return http.SameSiteStrictMode
	default:
		return http.SameSiteDefaultMode
	}
}
func validCookie(host, name, value, path string) bool {
	return host != "" && len(host) <= 255 && path != "" && strings.HasPrefix(path, "/") && !strings.ContainsAny(host+name+value+path, "\r\n\x00")
}
func categorizeDB(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return ErrInvalidDatabase
}
