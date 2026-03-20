package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteDB struct {
	db *sql.DB
}

func NewSQLite(dbPath string) (*SQLiteDB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	return &SQLiteDB{db: db}, nil
}

func (s *SQLiteDB) Migrate(ctx context.Context) error {
	for _, stmt := range migrations {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	return nil
}

func (s *SQLiteDB) FindKeyMatch(ctx context.Context, key, version string, restoreKeys []string) (*CacheKey, error) {
	// 1. Exact primary match
	ck, err := s.findByID(ctx, CacheKeyID(key, version))
	if err != nil {
		return nil, err
	}
	if ck != nil {
		return ck, nil
	}

	// 2. Prefixed primary match
	ck, err = s.findPrefixed(ctx, key, version)
	if err != nil {
		return nil, err
	}
	if ck != nil {
		return ck, nil
	}

	// 3. Restore keys
	for _, rk := range restoreKeys {
		ck, err = s.findByID(ctx, CacheKeyID(rk, version))
		if err != nil {
			return nil, err
		}
		if ck != nil {
			return ck, nil
		}

		ck, err = s.findPrefixed(ctx, rk, version)
		if err != nil {
			return nil, err
		}
		if ck != nil {
			return ck, nil
		}
	}

	return nil, nil
}

func (s *SQLiteDB) findByID(ctx context.Context, id string) (*CacheKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, key, version, updated_at, accessed_at FROM cache_keys WHERE id = ?`, id)
	return scanCacheKey(row)
}

func (s *SQLiteDB) findPrefixed(ctx context.Context, keyPrefix, version string) (*CacheKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, key, version, updated_at, accessed_at FROM cache_keys
		 WHERE key LIKE ? AND version = ?
		 ORDER BY updated_at DESC LIMIT 1`, keyPrefix+"%", version)
	return scanCacheKey(row)
}

func (s *SQLiteDB) ListEntriesByKey(ctx context.Context, key string) ([]CacheKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, key, version, updated_at, accessed_at FROM cache_keys WHERE key = ?`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCacheKeys(rows)
}

func (s *SQLiteDB) UpdateOrCreateKey(ctx context.Context, key, version string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := CacheKeyID(key, version)

	res, err := s.db.ExecContext(ctx,
		`UPDATE cache_keys SET updated_at = ?, accessed_at = ? WHERE id = ?`, now, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return s.CreateKey(ctx, key, version)
	}
	return nil
}

func (s *SQLiteDB) TouchKey(ctx context.Context, key, version string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`UPDATE cache_keys SET accessed_at = ? WHERE id = ?`, now, CacheKeyID(key, version))
	return err
}

func (s *SQLiteDB) FindStaleKeys(ctx context.Context, olderThanDays int) ([]CacheKey, error) {
	if olderThanDays == 0 {
		rows, err := s.db.QueryContext(ctx,
			`SELECT id, key, version, updated_at, accessed_at FROM cache_keys`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanCacheKeys(rows)
	}

	threshold := time.Now().UTC().Add(-time.Duration(olderThanDays) * 24 * time.Hour).Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, key, version, updated_at, accessed_at FROM cache_keys WHERE accessed_at < ?`, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCacheKeys(rows)
}

func (s *SQLiteDB) CreateKey(ctx context.Context, key, version string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cache_keys (id, key, version, updated_at, accessed_at) VALUES (?, ?, ?, ?, ?)`,
		CacheKeyID(key, version), key, version, now, now)
	return err
}

func (s *SQLiteDB) PruneKeys(ctx context.Context, keys []CacheKey) error {
	if keys == nil {
		_, err := s.db.ExecContext(ctx, `DELETE FROM cache_keys`)
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `DELETE FROM cache_keys WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, k := range keys {
		if _, err := stmt.ExecContext(ctx, CacheKeyID(k.Key, k.Version)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteDB) GetUpload(ctx context.Context, key, version string) (*Upload, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, created_at, key, version FROM uploads WHERE key = ? AND version = ?`, key, version)
	var u Upload
	err := row.Scan(&u.ID, &u.CreatedAt, &u.Key, &u.Version)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *SQLiteDB) GetUploadByID(ctx context.Context, id string) (*Upload, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, created_at, key, version FROM uploads WHERE id = ?`, id)
	var u Upload
	err := row.Scan(&u.ID, &u.CreatedAt, &u.Key, &u.Version)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *SQLiteDB) CreateUpload(ctx context.Context, upload Upload) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO uploads (id, created_at, key, version) VALUES (?, ?, ?, ?)`,
		upload.ID, upload.CreatedAt, upload.Key, upload.Version)
	return err
}

func (s *SQLiteDB) DeleteUpload(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM uploads WHERE id = ?`, id)
	return err
}

func (s *SQLiteDB) CreateUploadPart(ctx context.Context, part UploadPart) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO upload_parts (upload_id, part_number) VALUES (?, ?)`,
		part.UploadID, part.PartNumber)
	return err
}

func (s *SQLiteDB) ListUploadParts(ctx context.Context, uploadID string) ([]UploadPart, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT upload_id, part_number FROM upload_parts WHERE upload_id = ? ORDER BY part_number`, uploadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var parts []UploadPart
	for rows.Next() {
		var p UploadPart
		if err := rows.Scan(&p.UploadID, &p.PartNumber); err != nil {
			return nil, err
		}
		parts = append(parts, p)
	}
	return parts, rows.Err()
}

func (s *SQLiteDB) ListStaleUploads(ctx context.Context, olderThan time.Duration) ([]Upload, error) {
	threshold := time.Now().UTC().Add(-olderThan).Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, created_at, key, version FROM uploads WHERE created_at < ?`, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var uploads []Upload
	for rows.Next() {
		var u Upload
		if err := rows.Scan(&u.ID, &u.CreatedAt, &u.Key, &u.Version); err != nil {
			return nil, err
		}
		uploads = append(uploads, u)
	}
	return uploads, rows.Err()
}

func (s *SQLiteDB) GetMeta(ctx context.Context, key string) (*string, error) {
	row := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key)
	var val string
	err := row.Scan(&val)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &val, nil
}

func (s *SQLiteDB) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = ?`,
		key, value, value)
	return err
}

func (s *SQLiteDB) Close() error {
	return s.db.Close()
}

func scanCacheKey(row *sql.Row) (*CacheKey, error) {
	var ck CacheKey
	err := row.Scan(&ck.ID, &ck.Key, &ck.Version, &ck.UpdatedAt, &ck.AccessedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ck, nil
}

func scanCacheKeys(rows *sql.Rows) ([]CacheKey, error) {
	var keys []CacheKey
	for rows.Next() {
		var ck CacheKey
		if err := rows.Scan(&ck.ID, &ck.Key, &ck.Version, &ck.UpdatedAt, &ck.AccessedAt); err != nil {
			return nil, err
		}
		keys = append(keys, ck)
	}
	return keys, rows.Err()
}
