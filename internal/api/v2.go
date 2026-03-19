package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/promisenetwork/github-actions-cache-server/internal/cache"
	"github.com/promisenetwork/github-actions-cache-server/internal/config"
)

func handleV2CreateCacheEntry(svc cache.Service, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Key     string `json:"key"`
			Version string `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		cacheID, err := svc.ReserveCache(r.Context(), body.Key, body.Version)
		if err != nil {
			slog.Error("error reserving cache", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if cacheID == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "cache already reserved"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                true,
			"signed_upload_url": fmt.Sprintf("%s/upload/%d", cfg.APIBaseURL, cacheID),
			"message":           "",
		})
	}
}

func handleV2FinalizeCacheEntry(svc cache.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Key     string `json:"key"`
			Version string `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		upload, err := svc.GetDB().GetUpload(r.Context(), body.Key, body.Version)
		if err != nil {
			slog.Error("error getting upload", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if upload == nil {
			http.Error(w, "Upload not found", http.StatusNotFound)
			return
		}

		if err := svc.CommitCache(r.Context(), upload.ID); err != nil {
			slog.Error("error committing cache", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"entry_id": upload.ID,
			"message":  "",
		})
	}
}

func handleV2GetCacheEntryDownloadURL(svc cache.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Key         string   `json:"key"`
			RestoreKeys []string `json:"restore_keys"`
			Version     string   `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		keys := []string{body.Key}
		keys = append(keys, body.RestoreKeys...)

		entry, err := svc.GetCacheEntry(r.Context(), keys, body.Version)
		if err != nil {
			slog.Error("error getting cache entry", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if entry == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                  true,
			"signed_download_url": entry.ArchiveLocation,
			"matched_key":        entry.CacheKey,
		})
	}
}
