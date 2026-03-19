package api

import (
	"io"
	"log/slog"
	"net/http"
)

const defaultActionsResultsURL = "https://results-receiver.actions.githubusercontent.com"

func handleCatchAll() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slog.Info("proxying unknown path", "path", r.URL.Path, "target", defaultActionsResultsURL)

		proxyURL := defaultActionsResultsURL + r.URL.Path
		if r.URL.RawQuery != "" {
			proxyURL += "?" + r.URL.RawQuery
		}

		proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, proxyURL, r.Body)
		if err != nil {
			http.Error(w, "failed to create proxy request", http.StatusInternalServerError)
			return
		}

		// Copy headers
		for key, values := range r.Header {
			for _, value := range values {
				proxyReq.Header.Add(key, value)
			}
		}

		resp, err := http.DefaultClient.Do(proxyReq)
		if err != nil {
			slog.Error("proxy error", "path", r.URL.Path, "error", err)
			http.Error(w, "proxy error", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// Copy response headers
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}

		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}
