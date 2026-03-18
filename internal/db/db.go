package db

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"time"
)

// CacheKey represents a row in the cache_keys table.
type CacheKey struct {
	ID         string
	Key        string
	Version    string
	UpdatedAt  string
	AccessedAt string
}

// Upload represents a row in the uploads table.
type Upload struct {
	ID        string
	CreatedAt string
	Key       string
	Version   string
}

// UploadPart represents a row in the upload_parts table.
type UploadPart struct {
	UploadID   string
	PartNumber int
}

// DB defines the database interface for cache operations.
type DB interface {
	// FindKeyMatch searches for a cache key match using the GitHub Actions
	// matching algorithm: exact primary, prefixed primary, exact restore,
	// prefixed restore — ordered by updated_at desc.
	FindKeyMatch(ctx context.Context, key, version string, restoreKeys []string) (*CacheKey, error)

	// ListEntriesByKey returns all cache entries with the given key.
	ListEntriesByKey(ctx context.Context, key string) ([]CacheKey, error)

	// UpdateOrCreateKey updates an existing key's timestamps or creates it.
	UpdateOrCreateKey(ctx context.Context, key, version string) error

	// TouchKey updates the accessed_at timestamp.
	TouchKey(ctx context.Context, key, version string) error

	// FindStaleKeys returns keys not accessed within olderThanDays.
	// If olderThanDays is 0, returns all keys.
	FindStaleKeys(ctx context.Context, olderThanDays int) ([]CacheKey, error)

	// CreateKey inserts a new cache key.
	CreateKey(ctx context.Context, key, version string) error

	// PruneKeys deletes the given keys. If keys is nil, deletes all.
	PruneKeys(ctx context.Context, keys []CacheKey) error

	// GetUpload returns an upload by key and version.
	GetUpload(ctx context.Context, key, version string) (*Upload, error)

	// GetUploadByID returns an upload by its ID.
	GetUploadByID(ctx context.Context, id string) (*Upload, error)

	// CreateUpload inserts a new upload record.
	CreateUpload(ctx context.Context, upload Upload) error

	// DeleteUpload deletes an upload and its parts (via cascade).
	DeleteUpload(ctx context.Context, id string) error

	// CreateUploadPart inserts a new upload part.
	CreateUploadPart(ctx context.Context, part UploadPart) error

	// ListUploadParts returns all parts for an upload, ordered by part_number.
	ListUploadParts(ctx context.Context, uploadID string) ([]UploadPart, error)

	// ListStaleUploads returns uploads older than the given duration.
	ListStaleUploads(ctx context.Context, olderThan time.Duration) ([]Upload, error)

	// Migrate runs database migrations.
	Migrate(ctx context.Context) error

	// GetMeta returns a value from the meta table.
	GetMeta(ctx context.Context, key string) (*string, error)

	// SetMeta sets a value in the meta table (upsert).
	SetMeta(ctx context.Context, key, value string) error

	// Close closes the database connection.
	Close() error
}

// CacheKeyID returns the sha256 hash of key + "-" + version.
func CacheKeyID(key, version string) string {
	h := sha256.Sum256([]byte(key + "-" + version))
	return fmt.Sprintf("%x", h)
}

// CacheFileName returns the sha1 hash of key + "-" + version.
func CacheFileName(key, version string) string {
	h := sha1.Sum([]byte(key + "-" + version))
	return fmt.Sprintf("%x", h)
}
