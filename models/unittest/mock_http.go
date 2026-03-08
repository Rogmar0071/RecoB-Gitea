// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package unittest

import (
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"code.gitea.io/gitea/modules/log"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockServerOptions configures optional behavior for NewMockWebServer.
type MockServerOptions struct {
	// ExtraRoutes registers additional handlers on the server's ServeMux before the
	// default fixture handler. More specific patterns take precedence over the catch-all.
	ExtraRoutes func(mux *http.ServeMux)
}

// NewMockWebServer creates a mock HTTP server that either records responses from a live
// service or replays previously recorded responses from fixture files.
//
//   - liveMode=true: proxies requests to liveServerBaseURL and saves responses as fixture files
//   - liveMode=false: serves responses from previously saved fixture files
//
// Fixture files use a simple format: HTTP headers (one per line), an empty line, then the
// response body. The liveServerBaseURL in responses is replaced with the mock server URL.
//
// The convention for activating live mode is to check for an environment variable containing
// an API token for the service, e.g.:
//
//	token := os.Getenv("GITEA_TOKEN")
//	server := NewMockWebServer(t, "https://gitea.com", fixturePath, token != "")
func NewMockWebServer(t *testing.T, liveServerBaseURL, testDataDir string, liveMode bool, opts ...MockServerOptions) *httptest.Server {
	t.Helper()
	mockServerBaseURL := ""
	ignoredHeaders := []string{"cf-ray", "server", "date", "report-to", "nel", "x-request-id", "set-cookie"}

	var options MockServerOptions
	if len(opts) > 0 {
		options = opts[0]
	}

	fixtureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := NormalizedFullPath(r.URL)
		log.Info("Mock HTTP Server: %s %s", r.Method, path)

		fixturePath := fmt.Sprintf("%s/%s_%s", testDataDir, r.Method, url.QueryEscape(path))
		if strings.Contains(r.URL.Path, ".git/") {
			fixturePath = fmt.Sprintf("%s/%s_%s", testDataDir, r.Method, url.QueryEscape(r.URL.Path))
		}

		if liveMode {
			require.NoError(t, os.MkdirAll(testDataDir, 0o755))

			liveURL := fmt.Sprintf("%s%s", liveServerBaseURL, path)
			request, err := http.NewRequest(r.Method, liveURL, r.Body)
			require.NoError(t, err, "constructing an HTTP request to %s failed", liveURL)
			for headerName, headerValues := range r.Header {
				if !strings.EqualFold(headerName, "accept-encoding") {
					for _, headerValue := range headerValues {
						request.Header.Add(headerName, headerValue)
					}
				}
			}

			response, err := http.DefaultClient.Do(request)
			require.NoError(t, err, "HTTP request to %s failed", liveURL)
			defer response.Body.Close()
			assert.Less(t, response.StatusCode, 400, "unexpected status code for %s", liveURL)

			fixture, err := os.Create(fixturePath)
			require.NoError(t, err, "failed to open the fixture file %s for writing", fixturePath)
			defer fixture.Close()

			for _, headerName := range slices.Sorted(maps.Keys(response.Header)) {
				for _, headerValue := range response.Header[headerName] {
					if !slices.Contains(ignoredHeaders, strings.ToLower(headerName)) {
						_, err := fmt.Fprintf(fixture, "%s: %s\n", headerName, headerValue)
						require.NoError(t, err)
					}
				}
			}
			_, err = fixture.WriteString("\n")
			require.NoError(t, err)

			_, err = io.Copy(fixture, response.Body)
			require.NoError(t, err, "writing response body for %s failed", liveURL)

			require.NoError(t, fixture.Sync())
		}

		fixture, err := os.ReadFile(fixturePath)
		require.NoError(t, err, "missing fixture: %s", fixturePath)

		stringFixture := strings.ReplaceAll(string(fixture), liveServerBaseURL, mockServerBaseURL)

		headerSection, responseBody, _ := strings.Cut(stringFixture, "\n\n")
		for line := range strings.SplitSeq(headerSection, "\n") {
			if header, value, ok := strings.Cut(line, ": "); ok {
				if !strings.EqualFold(header, "Content-Length") {
					w.Header().Set(header, value)
				}
			}
		}
		w.WriteHeader(http.StatusOK)
		_, err = w.Write([]byte(responseBody))
		require.NoError(t, err)
	})

	mux := http.NewServeMux()
	if options.ExtraRoutes != nil {
		options.ExtraRoutes(mux)
	}
	mux.Handle("/", fixtureHandler)

	server := httptest.NewServer(mux)
	mockServerBaseURL = server.URL
	t.Cleanup(server.Close)
	return server
}

// NormalizedFullPath returns the URL path including query parameters.
func NormalizedFullPath(u *url.URL) string {
	if u.RawQuery == "" {
		return u.EscapedPath()
	}
	return u.EscapedPath() + "?" + u.RawQuery
}
