package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisCacheLayer wraps a CacheService with Redis read-through caching
// on GetCacheEntry and write-through invalidation on CommitCache.
type RedisCacheLayer struct {
	*CacheService
	rdb *redis.Client
	ttl time.Duration
}

// NewRedisCacheLayer creates a Redis-backed cache layer.
// If redisURL is empty, returns the base CacheService without Redis.
func NewRedisCacheLayer(svc *CacheService, redisURL string, ttl time.Duration) (*RedisCacheLayer, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}

	rdb := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return &RedisCacheLayer{
		CacheService: svc,
		rdb:          rdb,
		ttl:          ttl,
	}, nil
}

func redisCacheKey(keys []string, version string) string {
	return fmt.Sprintf("cache_entry:%s:%s", strings.Join(keys, ","), version)
}

// GetCacheEntry checks Redis first, falls back to DB, and populates Redis on miss.
func (r *RedisCacheLayer) GetCacheEntry(ctx context.Context, keys []string, version string) (*CacheEntry, error) {
	rKey := redisCacheKey(keys, version)

	// Try Redis first
	val, err := r.rdb.Get(ctx, rKey).Result()
	if err == nil {
		var entry CacheEntry
		if json.Unmarshal([]byte(val), &entry) == nil {
			return &entry, nil
		}
	}

	// Fall back to DB
	entry, err := r.CacheService.GetCacheEntry(ctx, keys, version)
	if err != nil {
		return nil, err
	}

	// Populate Redis on hit
	if entry != nil {
		if data, err := json.Marshal(entry); err == nil {
			r.rdb.Set(ctx, rKey, data, r.ttl)
		}
	}

	return entry, nil
}

// CommitCache finalizes the upload and invalidates relevant Redis keys.
func (r *RedisCacheLayer) CommitCache(ctx context.Context, uploadID string) error {
	if err := r.CacheService.CommitCache(ctx, uploadID); err != nil {
		return err
	}

	// Invalidate cache entries matching this upload's key pattern.
	// Use a scan to find and delete matching keys.
	iter := r.rdb.Scan(ctx, 0, "cache_entry:*", 0).Iterator()
	for iter.Next(ctx) {
		r.rdb.Del(ctx, iter.Val())
	}

	return nil
}

// Close closes the Redis connection.
func (r *RedisCacheLayer) Close() error {
	return r.rdb.Close()
}
