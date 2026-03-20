package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	APIBaseURL             string
	StorageDriver          string
	DBDriver               string
	Debug                  bool
	Port                   int
	CacheCleanupOlderDays  int
	CacheCleanupCron       string
	UploadCleanupCron      string
	EnableDirectDownloads  bool
	TempDir                string
	MetricsEnabled         bool
	RedisURL               string
	RedisTTL               time.Duration
	S3Bucket               string // STORAGE_S3_BUCKET
	MySQLDatabase          string // DB_MYSQL_DATABASE
	MySQLHost              string // DB_MYSQL_HOST
	MySQLUser              string // DB_MYSQL_USER
	MySQLPassword          string // DB_MYSQL_PASSWORD
	MySQLPort              string // DB_MYSQL_PORT
}

func Load() (*Config, error) {
	apiBaseURL := os.Getenv("API_BASE_URL")
	if apiBaseURL == "" {
		return nil, fmt.Errorf("API_BASE_URL is required")
	}

	port := envInt("PORT", 0)
	if port == 0 {
		port = envInt("NITRO_PORT", 3000)
	}

	return &Config{
		APIBaseURL:            apiBaseURL,
		StorageDriver:         strings.ToLower(envStr("STORAGE_DRIVER", "filesystem")),
		DBDriver:              strings.ToLower(envStr("DB_DRIVER", "sqlite")),
		Debug:                 envBool("DEBUG", false),
		Port:                  port,
		CacheCleanupOlderDays: envInt("CACHE_CLEANUP_OLDER_THAN_DAYS", 90),
		CacheCleanupCron:      envStr("CACHE_CLEANUP_CRON", "0 0 * * *"),
		UploadCleanupCron:     envStr("UPLOAD_CLEANUP_CRON", "*/10 * * * *"),
		EnableDirectDownloads: envBool("ENABLE_DIRECT_DOWNLOADS", false),
		TempDir:               envStr("TEMP_DIR", os.TempDir()),
		MetricsEnabled:        envBool("METRICS_ENABLED", false),
		RedisURL:              os.Getenv("REDIS_URL"),
		RedisTTL:              envDuration("REDIS_TTL", 5*time.Minute),
		S3Bucket:              os.Getenv("STORAGE_S3_BUCKET"),
		MySQLDatabase:         os.Getenv("DB_MYSQL_DATABASE"),
		MySQLHost:             os.Getenv("DB_MYSQL_HOST"),
		MySQLUser:             os.Getenv("DB_MYSQL_USER"),
		MySQLPassword:         os.Getenv("DB_MYSQL_PASSWORD"),
		MySQLPort:             os.Getenv("DB_MYSQL_PORT"),
	}, nil
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
