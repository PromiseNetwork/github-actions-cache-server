package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type MySQLDB struct {
	db *sql.DB
}

func NewMySQL(host, port, user, password, database string) (*MySQLDB, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true", user, password, host, port, database)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}

	db.SetMaxOpenConns(10)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}

	return &MySQLDB{db: db}, nil
}

func (m *MySQLDB) Migrate(ctx context.Context) error {
	for _, stmt := range mysqlMigrations {
		if _, err := m.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	return nil
}

func (m *MySQLDB) FindKeyMatch(ctx context.Context, key, version string, restoreKeys []string) (*CacheKey, error) {
	ck, err := m.findByID(ctx, CacheKeyID(key, version))
	if err != nil {
		return nil, err
	}
	if ck != nil {
		return ck, nil
	}

	ck, err = m.findPrefixed(ctx, key, version)
	if err != nil {
		return nil, err
	}
	if ck != nil {
		return ck, nil
	}

	for _, rk := range restoreKeys {
		ck, err = m.findByID(ctx, CacheKeyID(rk, version))
		if err != nil {
			return nil, err
		}
		if ck != nil {
			return ck, nil
		}

		ck, err = m.findPrefixed(ctx, rk, version)
		if err != nil {
			return nil, err
		}
		if ck != nil {
			return ck, nil
		}
	}

	return nil, nil
}

func (m *MySQLDB) findByID(ctx context.Context, id string) (*CacheKey, error) {
	row := m.db.QueryRowContext(ctx,
		`SELECT id, ` + "`key`" + `, version, updated_at, accessed_at FROM cache_keys WHERE id = ?`, id)
	return scanCacheKey(row)
}

func (m *MySQLDB) findPrefixed(ctx context.Context, keyPrefix, version string) (*CacheKey, error) {
	row := m.db.QueryRowContext(ctx,
		`SELECT id, ` + "`key`" + `, version, updated_at, accessed_at FROM cache_keys
		 WHERE ` + "`key`" + ` LIKE ? AND version = ?
		 ORDER BY updated_at DESC LIMIT 1`, keyPrefix+"%", version)
	return scanCacheKey(row)
}

func (m *MySQLDB) ListEntriesByKey(ctx context.Context, key string) ([]CacheKey, error) {
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, ` + "`key`" + `, version, updated_at, accessed_at FROM cache_keys WHERE ` + "`key`" + ` = ?`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCacheKeys(rows)
}

func (m *MySQLDB) UpdateOrCreateKey(ctx context.Context, key, version string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := CacheKeyID(key, version)

	res, err := m.db.ExecContext(ctx,
		`UPDATE cache_keys SET updated_at = ?, accessed_at = ? WHERE id = ?`, now, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return m.CreateKey(ctx, key, version)
	}
	return nil
}

func (m *MySQLDB) TouchKey(ctx context.Context, key, version string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := m.db.ExecContext(ctx,
		`UPDATE cache_keys SET accessed_at = ? WHERE id = ?`, now, CacheKeyID(key, version))
	return err
}

func (m *MySQLDB) FindStaleKeys(ctx context.Context, olderThanDays int) ([]CacheKey, error) {
	if olderThanDays == 0 {
		rows, err := m.db.QueryContext(ctx,
			`SELECT id, ` + "`key`" + `, version, updated_at, accessed_at FROM cache_keys`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanCacheKeys(rows)
	}

	threshold := time.Now().UTC().Add(-time.Duration(olderThanDays) * 24 * time.Hour).Format(time.RFC3339Nano)
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, ` + "`key`" + `, version, updated_at, accessed_at FROM cache_keys WHERE accessed_at < ?`, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCacheKeys(rows)
}

func (m *MySQLDB) CreateKey(ctx context.Context, key, version string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO cache_keys (id, ` + "`key`" + `, version, updated_at, accessed_at) VALUES (?, ?, ?, ?, ?)`,
		CacheKeyID(key, version), key, version, now, now)
	return err
}

func (m *MySQLDB) PruneKeys(ctx context.Context, keys []CacheKey) error {
	if keys == nil {
		_, err := m.db.ExecContext(ctx, `DELETE FROM cache_keys`)
		return err
	}

	tx, err := m.db.BeginTx(ctx, nil)
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

func (m *MySQLDB) GetUpload(ctx context.Context, key, version string) (*Upload, error) {
	row := m.db.QueryRowContext(ctx,
		`SELECT id, created_at, ` + "`key`" + `, version FROM uploads WHERE ` + "`key`" + ` = ? AND version = ?`, key, version)
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

func (m *MySQLDB) GetUploadByID(ctx context.Context, id string) (*Upload, error) {
	row := m.db.QueryRowContext(ctx,
		`SELECT id, created_at, ` + "`key`" + `, version FROM uploads WHERE id = ?`, id)
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

func (m *MySQLDB) CreateUpload(ctx context.Context, upload Upload) error {
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO uploads (id, created_at, ` + "`key`" + `, version) VALUES (?, ?, ?, ?)`,
		upload.ID, upload.CreatedAt, upload.Key, upload.Version)
	return err
}

func (m *MySQLDB) DeleteUpload(ctx context.Context, id string) error {
	_, err := m.db.ExecContext(ctx, `DELETE FROM uploads WHERE id = ?`, id)
	return err
}

func (m *MySQLDB) CreateUploadPart(ctx context.Context, part UploadPart) error {
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO upload_parts (upload_id, part_number) VALUES (?, ?)
		 ON DUPLICATE KEY UPDATE upload_id = upload_id`,
		part.UploadID, part.PartNumber)
	return err
}

func (m *MySQLDB) ListUploadParts(ctx context.Context, uploadID string) ([]UploadPart, error) {
	rows, err := m.db.QueryContext(ctx,
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

func (m *MySQLDB) ListStaleUploads(ctx context.Context, olderThan time.Duration) ([]Upload, error) {
	threshold := time.Now().UTC().Add(-olderThan).Format(time.RFC3339Nano)
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, created_at, ` + "`key`" + `, version FROM uploads WHERE created_at < ?`, threshold)
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

func (m *MySQLDB) GetMeta(ctx context.Context, key string) (*string, error) {
	row := m.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE ` + "`key`" + ` = ?`, key)
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

func (m *MySQLDB) SetMeta(ctx context.Context, key, value string) error {
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO meta (` + "`key`" + `, value) VALUES (?, ?) ON DUPLICATE KEY UPDATE value = ?`,
		key, value, value)
	return err
}

func (m *MySQLDB) Close() error {
	return m.db.Close()
}
