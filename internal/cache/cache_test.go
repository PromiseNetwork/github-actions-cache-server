package cache

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/promisenetwork/github-actions-cache-server/internal/db"
	"github.com/promisenetwork/github-actions-cache-server/internal/storage"
)

func newTestService(t *testing.T) *CacheService {
	t.Helper()
	dir := t.TempDir()

	sqliteDB, err := db.NewSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { sqliteDB.Close() })

	if err := sqliteDB.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	storageDir := filepath.Join(dir, "storage")
	fs, err := storage.NewFilesystem(storageDir)
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}

	return &CacheService{
		DB:         sqliteDB,
		Storage:    fs,
		APIBaseURL: "http://localhost:8080",
	}
}

func TestReserveCache(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	id, err := svc.ReserveCache(ctx, "key1", "v1")
	if err != nil {
		t.Fatalf("ReserveCache: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero upload ID")
	}

	// Second reserve with same key+version should return 0 (upload is still fresh)
	id2, err := svc.ReserveCache(ctx, "key1", "v1")
	if err != nil {
		t.Fatalf("ReserveCache second: %v", err)
	}
	if id2 != 0 {
		t.Errorf("expected 0 for duplicate reserve, got %d", id2)
	}
}

func TestReserveCacheReplacesStaleUpload(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Create an upload with an old timestamp to simulate a stale upload
	oldTime := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	svc.DB.CreateUpload(ctx, db.Upload{
		ID:        "stale-reserve",
		CreatedAt: oldTime,
		Key:       "stale-key",
		Version:   "v1",
	})

	// Reserve with same key+version should replace the stale upload
	id, err := svc.ReserveCache(ctx, "stale-key", "v1")
	if err != nil {
		t.Fatalf("ReserveCache: %v", err)
	}
	if id == 0 {
		t.Error("expected non-zero ID, stale upload should have been replaced")
	}
}

func TestUploadChunk(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	id, err := svc.ReserveCache(ctx, "key-upload", "v1")
	if err != nil {
		t.Fatalf("ReserveCache: %v", err)
	}

	uploadID := fmt.Sprintf("%d", id)
	err = svc.UploadChunk(ctx, uploadID, strings.NewReader("chunk data"), 0)
	if err != nil {
		t.Fatalf("UploadChunk: %v", err)
	}
}

func TestCommitCache(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	id, err := svc.ReserveCache(ctx, "key-commit", "v1")
	if err != nil {
		t.Fatalf("ReserveCache: %v", err)
	}

	uploadID := fmt.Sprintf("%d", id)
	if err := svc.UploadChunk(ctx, uploadID, strings.NewReader("data"), 0); err != nil {
		t.Fatalf("UploadChunk: %v", err)
	}

	if err := svc.CommitCache(ctx, uploadID); err != nil {
		t.Fatalf("CommitCache: %v", err)
	}

	// Verify key exists in DB
	entry, err := svc.GetCacheEntry(ctx, []string{"key-commit"}, "v1")
	if err != nil {
		t.Fatalf("GetCacheEntry: %v", err)
	}
	if entry == nil {
		t.Fatal("expected cache entry after commit")
	}
}

func TestGetCacheEntry(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Full flow: reserve, upload, commit
	id, _ := svc.ReserveCache(ctx, "get-key", "v1")
	uploadID := fmt.Sprintf("%d", id)
	svc.UploadChunk(ctx, uploadID, strings.NewReader("data"), 0)
	svc.CommitCache(ctx, uploadID)

	entry, err := svc.GetCacheEntry(ctx, []string{"get-key"}, "v1")
	if err != nil {
		t.Fatalf("GetCacheEntry: %v", err)
	}
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.CacheKey != "get-key" {
		t.Errorf("CacheKey = %q, want %q", entry.CacheKey, "get-key")
	}
	if entry.ArchiveLocation == "" {
		t.Error("expected non-empty ArchiveLocation")
	}
}

func TestGetCacheEntryMiss(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	entry, err := svc.GetCacheEntry(ctx, []string{"nonexistent"}, "v1")
	if err != nil {
		t.Fatalf("GetCacheEntry: %v", err)
	}
	if entry != nil {
		t.Error("expected nil for cache miss")
	}
}

func TestGetCacheEntryRestoreKeys(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Create entry with key "build-linux-abc123"
	id, _ := svc.ReserveCache(ctx, "build-linux-abc123", "v1")
	uploadID := fmt.Sprintf("%d", id)
	svc.UploadChunk(ctx, uploadID, strings.NewReader("data"), 0)
	svc.CommitCache(ctx, uploadID)

	// Lookup with restore key prefix
	entry, err := svc.GetCacheEntry(ctx, []string{"build-linux-exact"}, "v1")
	if err != nil {
		t.Fatalf("GetCacheEntry: %v", err)
	}
	if entry != nil {
		t.Error("exact miss should return nil without restore keys")
	}

	// With restore key prefix "build-linux-" should match
	entry, err = svc.GetCacheEntry(ctx, []string{"build-linux-exact", "build-linux-"}, "v1")
	if err != nil {
		t.Fatalf("GetCacheEntry with restore keys: %v", err)
	}
	if entry == nil {
		t.Fatal("expected entry via restore key prefix match")
	}
	if entry.CacheKey != "build-linux-abc123" {
		t.Errorf("CacheKey = %q, want %q", entry.CacheKey, "build-linux-abc123")
	}
}

func TestPruneCaches(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Create two entries
	for _, key := range []string{"prune-a", "prune-b"} {
		id, _ := svc.ReserveCache(ctx, key, "v1")
		uploadID := fmt.Sprintf("%d", id)
		svc.UploadChunk(ctx, uploadID, strings.NewReader("data"), 0)
		svc.CommitCache(ctx, uploadID)
	}

	// Prune with 0 days (all entries)
	if err := svc.PruneCaches(ctx, 0); err != nil {
		t.Fatalf("PruneCaches: %v", err)
	}

	// Both should be gone
	for _, key := range []string{"prune-a", "prune-b"} {
		entry, err := svc.GetCacheEntry(ctx, []string{key}, "v1")
		if err != nil {
			t.Fatalf("GetCacheEntry: %v", err)
		}
		if entry != nil {
			t.Errorf("expected %s to be pruned", key)
		}
	}
}

func TestPruneUploads(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Create an upload with old timestamp
	oldTime := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	svc.DB.CreateUpload(ctx, db.Upload{
		ID:        "stale-upload",
		CreatedAt: oldTime,
		Key:       "stale-key",
		Version:   "v1",
	})

	// Upload a part so there's something to clean up
	svc.Storage.UploadPart("stale-upload", 1, strings.NewReader("stale"))

	if err := svc.PruneUploads(ctx, 24*time.Hour); err != nil {
		t.Fatalf("PruneUploads: %v", err)
	}

	// Upload should be removed from DB
	upload, err := svc.DB.GetUploadByID(ctx, "stale-upload")
	if err != nil {
		t.Fatalf("GetUploadByID: %v", err)
	}
	if upload != nil {
		t.Error("expected stale upload to be pruned")
	}
}

func TestFullRoundTrip(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	chunks := []string{"chunk-AAA-", "chunk-BBB-", "chunk-CCC"}

	id, err := svc.ReserveCache(ctx, "roundtrip-key", "v1")
	if err != nil {
		t.Fatalf("ReserveCache: %v", err)
	}
	uploadID := fmt.Sprintf("%d", id)

	for i, chunk := range chunks {
		if err := svc.UploadChunk(ctx, uploadID, strings.NewReader(chunk), i); err != nil {
			t.Fatalf("UploadChunk %d: %v", i, err)
		}
	}

	if err := svc.CommitCache(ctx, uploadID); err != nil {
		t.Fatalf("CommitCache: %v", err)
	}

	// Get entry
	entry, err := svc.GetCacheEntry(ctx, []string{"roundtrip-key"}, "v1")
	if err != nil {
		t.Fatalf("GetCacheEntry: %v", err)
	}
	if entry == nil {
		t.Fatal("expected entry after round trip")
	}

	// Download and verify content
	cacheFileName := db.CacheFileName("roundtrip-key", "v1")
	rc, err := svc.Download(cacheFileName)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if rc == nil {
		t.Fatal("expected non-nil reader")
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read download: %v", err)
	}

	want := []byte("chunk-AAA-chunk-BBB-chunk-CCC")
	if !bytes.Equal(got, want) {
		t.Errorf("downloaded content = %q, want %q", got, want)
	}
}
