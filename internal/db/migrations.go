package db

// migrations contains the SQL statements to set up the database schema.
// These match the final state after all TypeScript Kysely migrations,
// including $3_remove_unused_columns (no driver_upload_id, no e_tag).
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS cache_keys (
		id VARCHAR(255) PRIMARY KEY NOT NULL,
		key TEXT NOT NULL,
		version TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		accessed_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS uploads (
		id TEXT PRIMARY KEY NOT NULL,
		created_at TEXT NOT NULL,
		key TEXT NOT NULL,
		version TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS upload_parts (
		upload_id TEXT,
		part_number INTEGER,
		PRIMARY KEY (upload_id, part_number),
		FOREIGN KEY (upload_id) REFERENCES uploads(id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS meta (
		key TEXT PRIMARY KEY,
		value TEXT
	)`,
}

// mysqlMigrations contains MySQL-specific schema.
// MySQL requires varchar(255) for primary keys instead of TEXT,
// and uses backticks for reserved keywords like "key".
var mysqlMigrations = []string{
	`CREATE TABLE IF NOT EXISTS cache_keys (
		id VARCHAR(255) PRIMARY KEY NOT NULL,
		` + "`key`" + ` TEXT NOT NULL,
		version TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		accessed_at TEXT NOT NULL
	) ENGINE=InnoDB CHARSET=latin1`,
	`CREATE TABLE IF NOT EXISTS uploads (
		id VARCHAR(255) PRIMARY KEY NOT NULL,
		created_at TEXT NOT NULL,
		` + "`key`" + ` TEXT NOT NULL,
		version TEXT NOT NULL
	) ENGINE=InnoDB CHARSET=latin1`,
	`CREATE TABLE IF NOT EXISTS upload_parts (
		upload_id VARCHAR(255),
		part_number INTEGER,
		PRIMARY KEY (upload_id, part_number),
		FOREIGN KEY (upload_id) REFERENCES uploads(id) ON DELETE CASCADE
	) ENGINE=InnoDB CHARSET=latin1`,
	`CREATE TABLE IF NOT EXISTS meta (
		` + "`key`" + ` VARCHAR(255) PRIMARY KEY,
		value TEXT
	) ENGINE=InnoDB CHARSET=latin1`,
}
