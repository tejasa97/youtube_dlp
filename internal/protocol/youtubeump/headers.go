package youtubeump

import "net/http"

var sabrProtectedHeaders = map[string]struct{}{
	"Accept":              {},
	"Accept-Encoding":     {},
	"Accept-Language":     {},
	"Authorization":       {},
	"Connection":          {},
	"Content-Length":      {},
	"Content-Type":        {},
	"Cookie":              {},
	"Host":                {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
	"User-Agent":          {},
	"X-Goog-Visitor-Id":   {},
}

func isSABRProtectedHeader(name string) bool {
	_, ok := sabrProtectedHeaders[http.CanonicalHeaderKey(name)]
	return ok
}

func applySABRCallerHeaders(request *http.Request, caller http.Header) {
	if len(caller) == 0 {
		return
	}
	for key, values := range caller {
		if isSABRProtectedHeader(key) {
			continue
		}
		canonical := http.CanonicalHeaderKey(key)
		for _, value := range values {
			if value == "" {
				continue
			}
			request.Header.Add(canonical, value)
		}
	}
}
