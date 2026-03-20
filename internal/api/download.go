package api

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/promisenetwork/github-actions-cache-server/internal/cache"
)

func handleDownload(svc cache.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cacheFileName := r.PathValue("cacheFileName")

		reader, err := svc.Download(cacheFileName)
		if err != nil {
			slog.Error("error downloading cache", "file", cacheFileName, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if reader == nil {
			http.Error(w, "cache file not found", http.StatusNotFound)
			return
		}
		defer reader.Close()

		w.Header().Set("Content-Type", "application/octet-stream")
		io.Copy(w, reader)
	}
}
