package api

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/promisenetwork/github-actions-cache-server/internal/cache"
	"github.com/promisenetwork/github-actions-cache-server/internal/config"
)

func NewRouter(svc cache.Service, cfg *config.Config) http.Handler {
	mux := http.NewServeMux()

	// Health checks
	healthHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}
	mux.HandleFunc("GET /{$}", healthHandler)
	mux.HandleFunc("GET /healthz", healthHandler)
	mux.HandleFunc("GET /readyz", healthHandler)

	// Prometheus metrics
	if cfg.MetricsEnabled {
		mux.Handle("GET /metrics", promhttp.Handler())
	}

	// V1 Legacy API (/_apis/artifactcache/)
	mux.HandleFunc("GET /_apis/artifactcache/cache", handleGetCacheEntry(svc))
	mux.HandleFunc("GET /_apis/artifactcache/caches", handleListEntries(svc))
	mux.HandleFunc("POST /_apis/artifactcache/caches", handleReserveCache(svc))
	mux.HandleFunc("PATCH /_apis/artifactcache/caches/{cacheId}", handleUploadChunk(svc))
	mux.HandleFunc("POST /_apis/artifactcache/caches/{cacheId}", handleCommitCache(svc))

	// V2 Twirp-style API
	mux.HandleFunc("POST /twirp/github.actions.results.api.v1.CacheService/CreateCacheEntry", handleV2CreateCacheEntry(svc, cfg))
	mux.HandleFunc("POST /twirp/github.actions.results.api.v1.CacheService/FinalizeCacheEntryUpload", handleV2FinalizeCacheEntry(svc))
	mux.HandleFunc("POST /twirp/github.actions.results.api.v1.CacheService/GetCacheEntryDownloadURL", handleV2GetCacheEntryDownloadURL(svc))

	// Upload (Azure Blob style)
	mux.HandleFunc("PUT /upload/{cacheId}", handleBlobUpload(svc))

	// Download
	mux.HandleFunc("GET /download/{random}/{cacheFileName}", handleDownload(svc))

	// Catch-all proxy
	mux.HandleFunc("/", handleCatchAll())

	// Wrap with middleware
	var handler http.Handler = mux
	if cfg.Debug {
		handler = debugLogging(handler)
	}
	if cfg.MetricsEnabled {
		handler = metricsMiddleware(handler)
	}
	handler = loggingMiddleware(handler)

	return handler
}
