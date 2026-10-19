package microsoft

import (
	"io"
	"net/http"
	"strings"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mockResponse(r *http.Request, contentType, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func countCacheMarkers(value any) int {
	count := 0
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "cache_control" {
				count++
			} else {
				count += countCacheMarkers(child)
			}
		}
	case []any:
		for _, child := range value {
			count += countCacheMarkers(child)
		}
	}
	return count
}
