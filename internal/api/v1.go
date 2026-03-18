package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/promisenetwork/github-actions-cache-server/internal/cache"
)

func handleGetCacheEntry(svc cache.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		keysParam := r.URL.Query().Get("keys")
		version := r.URL.Query().Get("version")

		if keysParam == "" || version == "" {
			http.Error(w, "keys and version are required", http.StatusBadRequest)
			return
		}

		keys := strings.Split(keysParam, ",")

		entry, err := svc.GetCacheEntry(r.Context(), keys, version)
		if err != nil {
			log.Printf("error getting cache entry: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if entry == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		writeJSON(w, http.StatusOK, entry)
	}
}

func handleListEntries(svc cache.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if key == "" {
			http.Error(w, "key is required", http.StatusBadRequest)
			return
		}

		entries, err := svc.GetDB().ListEntriesByKey(r.Context(), key)
		if err != nil {
			log.Printf("error listing entries: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		type cacheEntry struct {
			CacheKey     string `json:"cacheKey"`
			CacheVersion string `json:"cacheVersion"`
		}

		result := struct {
			TotalCount     int          `json:"totalCount"`
			ArtifactCaches []cacheEntry `json:"artifactCaches"`
		}{
			TotalCount:     len(entries),
			ArtifactCaches: make([]cacheEntry, len(entries)),
		}

		for i, e := range entries {
			result.ArtifactCaches[i] = cacheEntry{
				CacheKey:     e.Key,
				CacheVersion: e.Version,
			}
		}

		writeJSON(w, http.StatusOK, result)
	}
}

func handleReserveCache(svc cache.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Key     string `json:"key"`
			Version string `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if body.Key == "" {
			http.Error(w, "key is required", http.StatusBadRequest)
			return
		}

		cacheID, err := svc.ReserveCache(r.Context(), body.Key, body.Version)
		if err != nil {
			log.Printf("error reserving cache: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		result := struct {
			CacheID *int64 `json:"cacheId"`
		}{}
		if cacheID != 0 {
			result.CacheID = &cacheID
		}

		writeJSON(w, http.StatusOK, result)
	}
}

func handleUploadChunk(svc cache.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cacheID := r.PathValue("cacheId")

		contentRange := r.Header.Get("Content-Range")
		if contentRange == "" {
			http.Error(w, "'content-range' header is required", http.StatusBadRequest)
			return
		}

		start, end, err := parseContentRange(contentRange)
		if err != nil {
			http.Error(w, "invalid content-range header", http.StatusBadRequest)
			return
		}

		chunkSize := end - start
		chunkIndex := 0
		if chunkSize > 0 {
			chunkIndex = min(start/chunkSize, 9999)
		}

		if err := svc.UploadChunk(r.Context(), cacheID, r.Body, chunkIndex); err != nil {
			log.Printf("error uploading chunk: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func handleCommitCache(svc cache.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cacheID := r.PathValue("cacheId")

		if err := svc.CommitCache(r.Context(), cacheID); err != nil {
			log.Printf("error committing cache: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func parseContentRange(header string) (start, end int, err error) {
	// Format: "bytes START-END/*"
	s := strings.TrimPrefix(header, "bytes")
	s = strings.TrimSuffix(s, "/*")
	s = strings.TrimSpace(s)

	parts := strings.Split(s, "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid format")
	}

	start, err = strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, err
	}
	end, err = strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
