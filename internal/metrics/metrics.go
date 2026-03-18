package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	CacheOperationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cache_operations_total",
			Help: "Total number of cache operations",
		},
		[]string{"operation", "storage_driver"},
	)

	CacheUploadChunks = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cache_upload_chunks_total",
			Help: "Total number of uploaded chunks",
		},
		[]string{"storage_driver"},
	)

	CacheSizeBytes = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "cache_size_bytes",
			Help:    "Size of cache chunks in bytes",
			Buckets: prometheus.ExponentialBuckets(1024, 4, 10), // 1KB to ~256MB
		},
		[]string{"storage_driver"},
	)
)
