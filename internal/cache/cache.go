package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"time"

	"github.com/promisenetwork/github-actions-cache-server/internal/db"
	"github.com/promisenetwork/github-actions-cache-server/internal/storage"
)

// Service defines the cache service interface for use by API handlers.
type Service interface {
	ReserveCache(ctx context.Context, key, version string) (int64, error)
	UploadChunk(ctx context.Context, uploadID string, chunkStream io.Reader, chunkIndex int) error
	CommitCache(ctx context.Context, uploadID string) error
	GetCacheEntry(ctx context.Context, keys []string, version string) (*CacheEntry, error)
	Download(cacheFileName string) (io.ReadCloser, error)
	PruneCaches(ctx context.Context, olderThanDays int) error
	PruneUploads(ctx context.Context, olderThan time.Duration) error
	GetDB() db.DB
}

// CacheEntry represents a resolved cache entry for download.
type CacheEntry struct {
	ArchiveLocation string `json:"archiveLocation"`
	CacheKey        string `json:"cacheKey"`
}

// CacheService provides cache operations backed by a DB and storage driver.
type CacheService struct {
	DB                    db.DB
	Storage               storage.StorageDriver
	APIBaseURL            string
	EnableDirectDownloads bool
}

// ReserveCache creates a new upload reservation. Returns the upload ID,
// or 0 if the cache is already reserved.
func (s *CacheService) ReserveCache(ctx context.Context, key, version string) (int64, error) {
	existing, err := s.DB.GetUpload(ctx, key, version)
	if err != nil {
		return 0, fmt.Errorf("check existing upload: %w", err)
	}
	if existing != nil {
		return 0, nil
	}

	uploadID, err := randomUploadID()
	if err != nil {
		return 0, fmt.Errorf("generate upload id: %w", err)
	}

	err = s.DB.CreateUpload(ctx, db.Upload{
		ID:        fmt.Sprintf("%d", uploadID),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Key:       key,
		Version:   version,
	})
	if err != nil {
		return 0, fmt.Errorf("create upload: %w", err)
	}

	return uploadID, nil
}

// UploadChunk uploads a chunk of data as a part of a multipart upload.
func (s *CacheService) UploadChunk(ctx context.Context, uploadID string, chunkStream io.Reader, chunkIndex int) error {
	partNumber := chunkIndex + 1

	if err := s.Storage.UploadPart(uploadID, partNumber, chunkStream); err != nil {
		return fmt.Errorf("upload part %d: %w", partNumber, err)
	}

	if err := s.DB.CreateUploadPart(ctx, db.UploadPart{
		UploadID:   uploadID,
		PartNumber: partNumber,
	}); err != nil {
		return fmt.Errorf("record upload part: %w", err)
	}

	return nil
}

// CommitCache finalizes an upload by combining parts and creating the cache key.
func (s *CacheService) CommitCache(ctx context.Context, uploadID string) error {
	upload, err := s.DB.GetUploadByID(ctx, uploadID)
	if err != nil {
		return fmt.Errorf("get upload: %w", err)
	}
	if upload == nil {
		return nil
	}

	parts, err := s.DB.ListUploadParts(ctx, upload.ID)
	if err != nil {
		return fmt.Errorf("list upload parts: %w", err)
	}

	partNumbers := make([]int, len(parts))
	for i, p := range parts {
		partNumbers[i] = p.PartNumber
	}

	cacheFileName := db.CacheFileName(upload.Key, upload.Version)

	if err := s.Storage.CompleteMultipartUpload(cacheFileName, upload.ID, partNumbers); err != nil {
		return fmt.Errorf("complete multipart upload: %w", err)
	}

	if err := s.DB.DeleteUpload(ctx, upload.ID); err != nil {
		return fmt.Errorf("delete upload: %w", err)
	}

	if err := s.DB.UpdateOrCreateKey(ctx, upload.Key, upload.Version); err != nil {
		return fmt.Errorf("update or create key: %w", err)
	}

	return nil
}

// GetCacheEntry looks up a cache entry by keys and version.
func (s *CacheService) GetCacheEntry(ctx context.Context, keys []string, version string) (*CacheEntry, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	primaryKey := keys[0]
	var restoreKeys []string
	if len(keys) > 1 {
		restoreKeys = keys[1:]
	}

	cacheKey, err := s.DB.FindKeyMatch(ctx, primaryKey, version, restoreKeys)
	if err != nil {
		return nil, fmt.Errorf("find key match: %w", err)
	}
	if cacheKey == nil {
		return nil, nil
	}

	if err := s.DB.TouchKey(ctx, cacheKey.Key, cacheKey.Version); err != nil {
		slog.Warn("failed to touch key", "key", cacheKey.Key, "error", err)
	}

	cacheFileName := db.CacheFileName(cacheKey.Key, cacheKey.Version)

	var archiveLocation string
	if s.EnableDirectDownloads {
		url, err := s.Storage.CreateDownloadURL(cacheFileName)
		if err == nil && url != "" {
			archiveLocation = url
		}
	}
	if archiveLocation == "" {
		archiveLocation = createLocalDownloadURL(s.APIBaseURL, cacheFileName)
	}

	return &CacheEntry{
		ArchiveLocation: archiveLocation,
		CacheKey:        cacheKey.Key,
	}, nil
}

// Download returns a reader for the given cache file.
func (s *CacheService) Download(cacheFileName string) (io.ReadCloser, error) {
	return s.Storage.CreateReadStream(cacheFileName)
}

// PruneCaches removes stale cache entries.
func (s *CacheService) PruneCaches(ctx context.Context, olderThanDays int) error {
	keys, err := s.DB.FindStaleKeys(ctx, olderThanDays)
	if err != nil {
		return fmt.Errorf("find stale keys: %w", err)
	}
	if len(keys) == 0 {
		return nil
	}

	fileNames := make([]string, len(keys))
	for i, k := range keys {
		fileNames[i] = db.CacheFileName(k.Key, k.Version)
	}

	if err := s.Storage.Delete(fileNames); err != nil {
		return fmt.Errorf("delete storage files: %w", err)
	}

	return s.DB.PruneKeys(ctx, keys)
}

// PruneUploads removes stale uploads older than the given duration.
func (s *CacheService) PruneUploads(ctx context.Context, olderThan time.Duration) error {
	uploads, err := s.DB.ListStaleUploads(ctx, olderThan)
	if err != nil {
		return fmt.Errorf("list stale uploads: %w", err)
	}

	for _, u := range uploads {
		if err := s.Storage.CleanupMultipartUpload(u.ID); err != nil {
			slog.Warn("failed to cleanup upload", "upload_id", u.ID, "error", err)
		}
		if err := s.DB.DeleteUpload(ctx, u.ID); err != nil {
			slog.Warn("failed to delete upload", "upload_id", u.ID, "error", err)
		}
	}

	return nil
}

func createLocalDownloadURL(baseURL, cacheFileName string) string {
	b := make([]byte, 64)
	rand.Read(b)
	return fmt.Sprintf("%s/download/%s/%s", baseURL, hex.EncodeToString(b), cacheFileName)
}

// GetDB returns the underlying database.
func (s *CacheService) GetDB() db.DB {
	return s.DB
}

func randomUploadID() (int64, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(8_999_999_999))
	if err != nil {
		return 0, err
	}
	return n.Int64() + 1_000_000_000, nil
}
