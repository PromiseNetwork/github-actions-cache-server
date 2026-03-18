package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func newTestDB(t *testing.T) *SQLiteDB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := NewSQLite(dbPath)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Enable foreign keys for cascade tests.
	if _, err := s.db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign_keys: %v", err)
	}

	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

// insertKeyAt inserts a cache key with explicit timestamps.
func insertKeyAt(t *testing.T, db *SQLiteDB, key, version, updatedAt, accessedAt string) {
	t.Helper()
	id := CacheKeyID(key, version)
	_, err := db.db.Exec(
		`INSERT INTO cache_keys (id, key, version, updated_at, accessed_at) VALUES (?, ?, ?, ?, ?)`,
		id, key, version, updatedAt, accessedAt)
	if err != nil {
		t.Fatalf("insertKeyAt(%s, %s): %v", key, version, err)
	}
}

// --- Migrate ---

func TestMigrate(t *testing.T) {
	s := newTestDB(t)
	ctx := context.Background()

	// Tables should exist.
	for _, table := range []string{"cache_keys", "uploads", "upload_parts", "meta"} {
		var name string
		err := s.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s not found: %v", table, err)
		}
	}

	// Idempotent: running again should not error.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

// --- FindKeyMatch ---

func TestFindKeyMatch(t *testing.T) {
	ctx := context.Background()
	version := "v1"

	t.Run("exact primary match", func(t *testing.T) {
		s := newTestDB(t)
		insertKeyAt(t, s, "go-build-abc", version, "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z")

		ck, err := s.FindKeyMatch(ctx, "go-build-abc", version, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ck == nil || ck.Key != "go-build-abc" {
			t.Fatalf("expected exact match, got %+v", ck)
		}
	})

	t.Run("prefixed primary match", func(t *testing.T) {
		s := newTestDB(t)
		insertKeyAt(t, s, "go-build-abc123", version, "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z")

		ck, err := s.FindKeyMatch(ctx, "go-build-abc", version, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ck == nil || ck.Key != "go-build-abc123" {
			t.Fatalf("expected prefixed match, got %+v", ck)
		}
	})

	t.Run("exact restore key match", func(t *testing.T) {
		s := newTestDB(t)
		insertKeyAt(t, s, "restore-exact", version, "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z")

		ck, err := s.FindKeyMatch(ctx, "no-match-primary", version, []string{"restore-exact"})
		if err != nil {
			t.Fatal(err)
		}
		if ck == nil || ck.Key != "restore-exact" {
			t.Fatalf("expected restore exact match, got %+v", ck)
		}
	})

	t.Run("prefixed restore key match", func(t *testing.T) {
		s := newTestDB(t)
		insertKeyAt(t, s, "restore-prefix-xyz", version, "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z")

		ck, err := s.FindKeyMatch(ctx, "no-match-primary", version, []string{"restore-prefix-"})
		if err != nil {
			t.Fatal(err)
		}
		if ck == nil || ck.Key != "restore-prefix-xyz" {
			t.Fatalf("expected restore prefixed match, got %+v", ck)
		}
	})

	t.Run("prefixed match returns newest by updated_at", func(t *testing.T) {
		s := newTestDB(t)
		insertKeyAt(t, s, "build-old", version, "2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z")
		insertKeyAt(t, s, "build-new", version, "2025-06-01T00:00:00Z", "2025-06-01T00:00:00Z")
		insertKeyAt(t, s, "build-mid", version, "2025-03-01T00:00:00Z", "2025-03-01T00:00:00Z")

		ck, err := s.FindKeyMatch(ctx, "build-", version, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ck == nil || ck.Key != "build-new" {
			t.Fatalf("expected newest match, got %+v", ck)
		}
	})

	t.Run("exact match preferred over prefixed", func(t *testing.T) {
		s := newTestDB(t)
		// Insert a prefixed match with newer timestamp.
		insertKeyAt(t, s, "go-build-extended", version, "2025-06-01T00:00:00Z", "2025-06-01T00:00:00Z")
		// Insert exact match with older timestamp.
		insertKeyAt(t, s, "go-build", version, "2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z")

		ck, err := s.FindKeyMatch(ctx, "go-build", version, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ck == nil || ck.Key != "go-build" {
			t.Fatalf("expected exact match preferred, got %+v", ck)
		}
	})

	t.Run("no match returns nil", func(t *testing.T) {
		s := newTestDB(t)

		ck, err := s.FindKeyMatch(ctx, "nonexistent", version, []string{"also-no"})
		if err != nil {
			t.Fatal(err)
		}
		if ck != nil {
			t.Fatalf("expected nil, got %+v", ck)
		}
	})
}

// --- UpdateOrCreateKey ---

func TestUpdateOrCreateKey(t *testing.T) {
	ctx := context.Background()
	s := newTestDB(t)

	t.Run("creates new key", func(t *testing.T) {
		err := s.UpdateOrCreateKey(ctx, "new-key", "v1")
		if err != nil {
			t.Fatal(err)
		}
		ck, err := s.FindKeyMatch(ctx, "new-key", "v1", nil)
		if err != nil {
			t.Fatal(err)
		}
		if ck == nil || ck.Key != "new-key" {
			t.Fatalf("expected created key, got %+v", ck)
		}
	})

	t.Run("updates existing key timestamps", func(t *testing.T) {
		// Set key to a known old timestamp via direct SQL.
		oldTime := "2024-01-01T00:00:00Z"
		id := CacheKeyID("new-key", "v1")
		s.db.Exec(`UPDATE cache_keys SET updated_at = ?, accessed_at = ? WHERE id = ?`, oldTime, oldTime, id)

		err := s.UpdateOrCreateKey(ctx, "new-key", "v1")
		if err != nil {
			t.Fatal(err)
		}

		ck2, _ := s.FindKeyMatch(ctx, "new-key", "v1", nil)
		if ck2.UpdatedAt == oldTime {
			t.Fatal("expected updated_at to advance")
		}
		if ck2.AccessedAt == oldTime {
			t.Fatal("expected accessed_at to advance")
		}
	})
}

// --- TouchKey ---

func TestTouchKey(t *testing.T) {
	ctx := context.Background()
	s := newTestDB(t)

	oldTime := "2024-01-01T00:00:00Z"
	insertKeyAt(t, s, "touch-me", "v1", oldTime, oldTime)

	time.Sleep(10 * time.Millisecond)
	if err := s.TouchKey(ctx, "touch-me", "v1"); err != nil {
		t.Fatal(err)
	}

	ck, _ := s.FindKeyMatch(ctx, "touch-me", "v1", nil)
	if ck.AccessedAt == oldTime {
		t.Fatal("expected accessed_at to be updated")
	}
	if ck.UpdatedAt != oldTime {
		t.Fatalf("expected updated_at unchanged, got %s", ck.UpdatedAt)
	}
}

// --- FindStaleKeys ---

func TestFindStaleKeys(t *testing.T) {
	ctx := context.Background()

	t.Run("returns keys older than threshold", func(t *testing.T) {
		s := newTestDB(t)
		oldAccess := time.Now().UTC().Add(-10 * 24 * time.Hour).Format(time.RFC3339)
		newAccess := time.Now().UTC().Format(time.RFC3339)
		insertKeyAt(t, s, "stale-key", "v1", oldAccess, oldAccess)
		insertKeyAt(t, s, "fresh-key", "v1", newAccess, newAccess)

		stale, err := s.FindStaleKeys(ctx, 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(stale) != 1 || stale[0].Key != "stale-key" {
			t.Fatalf("expected 1 stale key, got %+v", stale)
		}
	})

	t.Run("returns all when olderThanDays is 0", func(t *testing.T) {
		s := newTestDB(t)
		insertKeyAt(t, s, "key-a", "v1", "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z")
		insertKeyAt(t, s, "key-b", "v1", "2025-06-01T00:00:00Z", "2025-06-01T00:00:00Z")

		all, err := s.FindStaleKeys(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != 2 {
			t.Fatalf("expected 2 keys, got %d", len(all))
		}
	})
}

// --- PruneKeys ---

func TestPruneKeys(t *testing.T) {
	ctx := context.Background()

	t.Run("deletes specified keys", func(t *testing.T) {
		s := newTestDB(t)
		insertKeyAt(t, s, "prune-a", "v1", "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z")
		insertKeyAt(t, s, "prune-b", "v1", "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z")
		insertKeyAt(t, s, "keep-c", "v1", "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z")

		err := s.PruneKeys(ctx, []CacheKey{
			{Key: "prune-a", Version: "v1"},
			{Key: "prune-b", Version: "v1"},
		})
		if err != nil {
			t.Fatal(err)
		}

		all, _ := s.FindStaleKeys(ctx, 0)
		if len(all) != 1 || all[0].Key != "keep-c" {
			t.Fatalf("expected only keep-c, got %+v", all)
		}
	})

	t.Run("deletes all when nil", func(t *testing.T) {
		s := newTestDB(t)
		insertKeyAt(t, s, "all-a", "v1", "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z")
		insertKeyAt(t, s, "all-b", "v1", "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z")

		err := s.PruneKeys(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}

		all, _ := s.FindStaleKeys(ctx, 0)
		if len(all) != 0 {
			t.Fatalf("expected 0, got %d", len(all))
		}
	})
}

// --- Upload CRUD ---

func TestUploadCRUD(t *testing.T) {
	ctx := context.Background()
	s := newTestDB(t)

	upload := Upload{
		ID:        "upload-1",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Key:       "cache-key",
		Version:   "v1",
	}

	t.Run("CreateUpload", func(t *testing.T) {
		if err := s.CreateUpload(ctx, upload); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("GetUpload by key and version", func(t *testing.T) {
		u, err := s.GetUpload(ctx, "cache-key", "v1")
		if err != nil {
			t.Fatal(err)
		}
		if u == nil || u.ID != "upload-1" {
			t.Fatalf("expected upload-1, got %+v", u)
		}
	})

	t.Run("GetUpload returns nil for missing", func(t *testing.T) {
		u, err := s.GetUpload(ctx, "nope", "v1")
		if err != nil {
			t.Fatal(err)
		}
		if u != nil {
			t.Fatalf("expected nil, got %+v", u)
		}
	})

	t.Run("GetUploadByID", func(t *testing.T) {
		u, err := s.GetUploadByID(ctx, "upload-1")
		if err != nil {
			t.Fatal(err)
		}
		if u == nil || u.Key != "cache-key" {
			t.Fatalf("expected cache-key, got %+v", u)
		}
	})

	t.Run("GetUploadByID returns nil for missing", func(t *testing.T) {
		u, err := s.GetUploadByID(ctx, "nonexistent")
		if err != nil {
			t.Fatal(err)
		}
		if u != nil {
			t.Fatalf("expected nil, got %+v", u)
		}
	})

	t.Run("DeleteUpload cascades parts", func(t *testing.T) {
		// Add parts first.
		s.CreateUploadPart(ctx, UploadPart{UploadID: "upload-1", PartNumber: 1})
		s.CreateUploadPart(ctx, UploadPart{UploadID: "upload-1", PartNumber: 2})

		if err := s.DeleteUpload(ctx, "upload-1"); err != nil {
			t.Fatal(err)
		}

		// Upload gone.
		u, _ := s.GetUploadByID(ctx, "upload-1")
		if u != nil {
			t.Fatal("expected upload deleted")
		}

		// Parts gone via cascade.
		parts, _ := s.ListUploadParts(ctx, "upload-1")
		if len(parts) != 0 {
			t.Fatalf("expected 0 parts after cascade, got %d", len(parts))
		}
	})
}

// --- UploadParts ---

func TestUploadParts(t *testing.T) {
	ctx := context.Background()
	s := newTestDB(t)

	// Create parent upload.
	s.CreateUpload(ctx, Upload{ID: "up-parts", CreatedAt: time.Now().UTC().Format(time.RFC3339), Key: "k", Version: "v"})

	t.Run("CreateUploadPart and ListUploadParts ordered", func(t *testing.T) {
		s.CreateUploadPart(ctx, UploadPart{UploadID: "up-parts", PartNumber: 3})
		s.CreateUploadPart(ctx, UploadPart{UploadID: "up-parts", PartNumber: 1})
		s.CreateUploadPart(ctx, UploadPart{UploadID: "up-parts", PartNumber: 2})

		parts, err := s.ListUploadParts(ctx, "up-parts")
		if err != nil {
			t.Fatal(err)
		}
		if len(parts) != 3 {
			t.Fatalf("expected 3 parts, got %d", len(parts))
		}
		for i, expected := range []int{1, 2, 3} {
			if parts[i].PartNumber != expected {
				t.Errorf("part[%d] = %d, want %d", i, parts[i].PartNumber, expected)
			}
		}
	})
}

// --- ListStaleUploads ---

func TestListStaleUploads(t *testing.T) {
	ctx := context.Background()
	s := newTestDB(t)

	oldTime := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	newTime := time.Now().UTC().Format(time.RFC3339)

	s.CreateUpload(ctx, Upload{ID: "stale-up", CreatedAt: oldTime, Key: "k1", Version: "v1"})
	s.CreateUpload(ctx, Upload{ID: "fresh-up", CreatedAt: newTime, Key: "k2", Version: "v1"})

	stale, err := s.ListStaleUploads(ctx, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].ID != "stale-up" {
		t.Fatalf("expected 1 stale upload, got %+v", stale)
	}
}

// --- GetMeta / SetMeta ---

func TestMeta(t *testing.T) {
	ctx := context.Background()
	s := newTestDB(t)

	t.Run("GetMeta returns nil when missing", func(t *testing.T) {
		val, err := s.GetMeta(ctx, "nonexistent")
		if err != nil {
			t.Fatal(err)
		}
		if val != nil {
			t.Fatalf("expected nil, got %q", *val)
		}
	})

	t.Run("SetMeta creates", func(t *testing.T) {
		if err := s.SetMeta(ctx, "schema_version", "1"); err != nil {
			t.Fatal(err)
		}
		val, err := s.GetMeta(ctx, "schema_version")
		if err != nil {
			t.Fatal(err)
		}
		if val == nil || *val != "1" {
			t.Fatalf("expected '1', got %+v", val)
		}
	})

	t.Run("SetMeta updates (upsert)", func(t *testing.T) {
		if err := s.SetMeta(ctx, "schema_version", "2"); err != nil {
			t.Fatal(err)
		}
		val, _ := s.GetMeta(ctx, "schema_version")
		if val == nil || *val != "2" {
			t.Fatalf("expected '2', got %+v", val)
		}

		// Verify only one row exists.
		var count int
		s.db.QueryRow(`SELECT COUNT(*) FROM meta WHERE key = 'schema_version'`).Scan(&count)
		if count != 1 {
			t.Fatalf("expected 1 row, got %d", count)
		}
	})
}

// --- CacheKeyID / CacheFileName ---

func TestHelpers(t *testing.T) {
	// Deterministic.
	id1 := CacheKeyID("key", "v1")
	id2 := CacheKeyID("key", "v1")
	if id1 != id2 {
		t.Fatal("CacheKeyID not deterministic")
	}

	// Different inputs produce different IDs.
	id3 := CacheKeyID("key", "v2")
	if id1 == id3 {
		t.Fatal("different inputs should produce different IDs")
	}

	fn1 := CacheFileName("key", "v1")
	fn2 := CacheFileName("key", "v1")
	if fn1 != fn2 {
		t.Fatal("CacheFileName not deterministic")
	}

	// CacheKeyID and CacheFileName use different hashes.
	if id1 == fn1 {
		t.Fatal("CacheKeyID and CacheFileName should differ")
	}
}

// --- ListEntriesByKey ---

func TestListEntriesByKey(t *testing.T) {
	ctx := context.Background()
	s := newTestDB(t)

	insertKeyAt(t, s, "shared-key", "v1", "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z")
	insertKeyAt(t, s, "shared-key", "v2", "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z")
	insertKeyAt(t, s, "other-key", "v1", "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z")

	entries, err := s.ListEntriesByKey(ctx, "shared-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Empty result.
	entries, err = s.ListEntriesByKey(ctx, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}

	_ = sql.ErrNoRows // ensure sql import used
}
