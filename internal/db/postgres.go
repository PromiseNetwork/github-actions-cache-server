package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type PostgresDB struct {
	db *sql.DB
}

func NewPostgres(connStr string) (*PostgresDB, error) {
	db, err := sql.Open("pgx", connStr)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	db.SetMaxOpenConns(10)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return &PostgresDB{db: db}, nil
}

func (p *PostgresDB) Migrate(ctx context.Context) error {
	for _, stmt := range migrations {
		if _, err := p.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	return nil
}

func (p *PostgresDB) FindKeyMatch(ctx context.Context, key, version string, restoreKeys []string) (*CacheKey, error) {
	ck, err := p.findByID(ctx, CacheKeyID(key, version))
	if err != nil {
		return nil, err
	}
	if ck != nil {
		return ck, nil
	}

	ck, err = p.findPrefixed(ctx, key, version)
	if err != nil {
		return nil, err
	}
	if ck != nil {
		return ck, nil
	}

	for _, rk := range restoreKeys {
		ck, err = p.findByID(ctx, CacheKeyID(rk, version))
		if err != nil {
			return nil, err
		}
		if ck != nil {
			return ck, nil
		}

		ck, err = p.findPrefixed(ctx, rk, version)
		if err != nil {
			return nil, err
		}
		if ck != nil {
			return ck, nil
		}
	}

	return nil, nil
}

func (p *PostgresDB) findByID(ctx context.Context, id string) (*CacheKey, error) {
	row := p.db.QueryRowContext(ctx,
		`SELECT id, key, version, updated_at, accessed_at FROM cache_keys WHERE id = $1`, id)
	return scanCacheKeyPg(row)
}

func (p *PostgresDB) findPrefixed(ctx context.Context, keyPrefix, version string) (*CacheKey, error) {
	row := p.db.QueryRowContext(ctx,
		`SELECT id, key, version, updated_at, accessed_at FROM cache_keys
		 WHERE key LIKE $1 AND version = $2
		 ORDER BY updated_at DESC LIMIT 1`, keyPrefix+"%", version)
	return scanCacheKeyPg(row)
}

func (p *PostgresDB) ListEntriesByKey(ctx context.Context, key string) ([]CacheKey, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, key, version, updated_at, accessed_at FROM cache_keys WHERE key = $1`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCacheKeys(rows)
}

func (p *PostgresDB) UpdateOrCreateKey(ctx context.Context, key, version string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := CacheKeyID(key, version)

	res, err := p.db.ExecContext(ctx,
		`UPDATE cache_keys SET updated_at = $1, accessed_at = $2 WHERE id = $3`, now, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return p.CreateKey(ctx, key, version)
	}
	return nil
}

func (p *PostgresDB) TouchKey(ctx context.Context, key, version string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := p.db.ExecContext(ctx,
		`UPDATE cache_keys SET accessed_at = $1 WHERE id = $2`, now, CacheKeyID(key, version))
	return err
}

func (p *PostgresDB) FindStaleKeys(ctx context.Context, olderThanDays int) ([]CacheKey, error) {
	if olderThanDays == 0 {
		rows, err := p.db.QueryContext(ctx,
			`SELECT id, key, version, updated_at, accessed_at FROM cache_keys`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanCacheKeys(rows)
	}

	threshold := time.Now().UTC().Add(-time.Duration(olderThanDays) * 24 * time.Hour).Format(time.RFC3339Nano)
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, key, version, updated_at, accessed_at FROM cache_keys WHERE accessed_at < $1`, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCacheKeys(rows)
}

func (p *PostgresDB) CreateKey(ctx context.Context, key, version string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO cache_keys (id, key, version, updated_at, accessed_at) VALUES ($1, $2, $3, $4, $5)`,
		CacheKeyID(key, version), key, version, now, now)
	return err
}

func (p *PostgresDB) PruneKeys(ctx context.Context, keys []CacheKey) error {
	if keys == nil {
		_, err := p.db.ExecContext(ctx, `DELETE FROM cache_keys`)
		return err
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `DELETE FROM cache_keys WHERE id = $1`)
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

func (p *PostgresDB) GetUpload(ctx context.Context, key, version string) (*Upload, error) {
	row := p.db.QueryRowContext(ctx,
		`SELECT id, created_at, key, version FROM uploads WHERE key = $1 AND version = $2`, key, version)
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

func (p *PostgresDB) GetUploadByID(ctx context.Context, id string) (*Upload, error) {
	row := p.db.QueryRowContext(ctx,
		`SELECT id, created_at, key, version FROM uploads WHERE id = $1`, id)
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

func (p *PostgresDB) CreateUpload(ctx context.Context, upload Upload) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO uploads (id, created_at, key, version) VALUES ($1, $2, $3, $4)`,
		upload.ID, upload.CreatedAt, upload.Key, upload.Version)
	return err
}

func (p *PostgresDB) DeleteUpload(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM uploads WHERE id = $1`, id)
	return err
}

func (p *PostgresDB) CreateUploadPart(ctx context.Context, part UploadPart) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO upload_parts (upload_id, part_number) VALUES ($1, $2)
		 ON CONFLICT (upload_id, part_number) DO NOTHING`,
		part.UploadID, part.PartNumber)
	return err
}

func (p *PostgresDB) ListUploadParts(ctx context.Context, uploadID string) ([]UploadPart, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT upload_id, part_number FROM upload_parts WHERE upload_id = $1 ORDER BY part_number`, uploadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var parts []UploadPart
	for rows.Next() {
		var pt UploadPart
		if err := rows.Scan(&pt.UploadID, &pt.PartNumber); err != nil {
			return nil, err
		}
		parts = append(parts, pt)
	}
	return parts, rows.Err()
}

func (p *PostgresDB) ListStaleUploads(ctx context.Context, olderThan time.Duration) ([]Upload, error) {
	threshold := time.Now().UTC().Add(-olderThan).Format(time.RFC3339Nano)
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, created_at, key, version FROM uploads WHERE created_at < $1`, threshold)
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

func (p *PostgresDB) GetMeta(ctx context.Context, key string) (*string, error) {
	row := p.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = $1`, key)
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

func (p *PostgresDB) SetMeta(ctx context.Context, key, value string) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES ($1, $2) ON CONFLICT(key) DO UPDATE SET value = $2`,
		key, value)
	return err
}

func (p *PostgresDB) Close() error {
	return p.db.Close()
}

func scanCacheKeyPg(row *sql.Row) (*CacheKey, error) {
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
