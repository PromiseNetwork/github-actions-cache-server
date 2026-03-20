package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/promisenetwork/github-actions-cache-server/internal/api"
	"github.com/promisenetwork/github-actions-cache-server/internal/cache"
	"github.com/promisenetwork/github-actions-cache-server/internal/config"
	"github.com/promisenetwork/github-actions-cache-server/internal/db"
	"github.com/promisenetwork/github-actions-cache-server/internal/storage"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Set up structured JSON logging for GKE
	logLevel := slog.LevelInfo
	if cfg.Debug {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// GKE expects "severity" instead of "level"
			if a.Key == slog.LevelKey {
				a.Key = "severity"
			}
			return a
		},
	})))

	slog.Debug("debug mode enabled")

	ctx := context.Background()

	// Initialize DB
	database, err := newDB(cfg)
	if err != nil {
		slog.Error("failed to init db", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	if err := database.Migrate(ctx); err != nil {
		slog.Error("failed to migrate db", "error", err)
		os.Exit(1)
	}
	slog.Info("using database driver", "driver", cfg.DBDriver)

	// Initialize storage driver
	storageDriver, err := newStorage(ctx, cfg)
	if err != nil {
		slog.Error("failed to init storage", "error", err)
		os.Exit(1)
	}
	slog.Info("using storage driver", "driver", cfg.StorageDriver)

	// Create cache service
	svc := &cache.CacheService{
		DB:                    database,
		Storage:               storageDriver,
		APIBaseURL:            cfg.APIBaseURL,
		EnableDirectDownloads: cfg.EnableDirectDownloads,
	}

	// Wrap with Redis if configured
	var cacheService cache.Service = svc
	if cfg.RedisURL != "" {
		redisLayer, err := cache.NewRedisCacheLayer(svc, cfg.RedisURL, cfg.RedisTTL)
		if err != nil {
			slog.Warn("failed to init redis, falling back to direct DB", "error", err)
		} else {
			cacheService = redisLayer
			defer redisLayer.Close()
			slog.Info("redis cache layer enabled")
		}
	}

	// Version check + prune on upgrade
	handleVersionCheck(ctx, database, svc)

	// Create HTTP router
	handler := api.NewRouter(cacheService, cfg)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Start cleanup cron jobs
	c := cron.New()
	if cfg.CacheCleanupOlderDays > 0 {
		c.AddFunc(cfg.CacheCleanupCron, func() {
			if err := svc.PruneCaches(context.Background(), cfg.CacheCleanupOlderDays); err != nil {
				slog.Error("cache cleanup error", "error", err)
			}
		})
		slog.Info("cache cleanup configured", "older_than_days", cfg.CacheCleanupOlderDays, "schedule", cfg.CacheCleanupCron)
	}
	c.AddFunc(cfg.UploadCleanupCron, func() {
		if err := svc.PruneUploads(context.Background(), 24*time.Hour); err != nil {
			slog.Error("upload cleanup error", "error", err)
		}
	})
	slog.Info("upload cleanup configured", "schedule", cfg.UploadCleanupCron)
	c.Start()

	// Graceful shutdown
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("starting github-actions-cache-server", "port", cfg.Port, "api_base_url", cfg.APIBaseURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-sigCtx.Done()
	slog.Info("shutting down server...")

	c.Stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown error", "error", err)
		os.Exit(1)
	}

	slog.Info("server stopped")
}

func newDB(cfg *config.Config) (db.DB, error) {
	switch cfg.DBDriver {
	case "sqlite":
		path := os.Getenv("DB_SQLITE_PATH")
		if path == "" {
			path = ".data/sqlite.db"
		}
		return db.NewSQLite(path)
	case "postgres":
		connStr := os.Getenv("DB_POSTGRES_URL")
		if connStr == "" {
			return nil, fmt.Errorf("DB_POSTGRES_URL is required for postgres driver")
		}
		return db.NewPostgres(connStr)
	case "mysql":
		return db.NewMySQL(cfg.MySQLHost, cfg.MySQLPort, cfg.MySQLUser, cfg.MySQLPassword, cfg.MySQLDatabase)
	default:
		return nil, fmt.Errorf("unsupported db driver: %s", cfg.DBDriver)
	}
}

func newStorage(ctx context.Context, cfg *config.Config) (storage.StorageDriver, error) {
	switch cfg.StorageDriver {
	case "filesystem":
		return storage.NewFilesystem(os.Getenv("STORAGE_FILESYSTEM_PATH"))
	case "gcs":
		return storage.NewGCS(ctx, storage.GCSOptions{
			Bucket:            os.Getenv("STORAGE_GCS_BUCKET"),
			ServiceAccountKey: os.Getenv("STORAGE_GCS_SERVICE_ACCOUNT_KEY"),
			Endpoint:          os.Getenv("STORAGE_GCS_ENDPOINT"),
		})
	case "s3":
		return storage.NewS3(ctx, cfg.S3Bucket)
	default:
		return nil, fmt.Errorf("unsupported storage driver: %s", cfg.StorageDriver)
	}
}

var version = "8.1.4"

func handleVersionCheck(ctx context.Context, database db.DB, svc *cache.CacheService) {
	existing, err := database.GetMeta(ctx, "version")
	if err != nil {
		slog.Warn("failed to get version from meta", "error", err)
		return
	}

	if existing == nil || *existing != version {
		if existing != nil {
			slog.Info("version changed, pruning cache...", "from", *existing, "to", version)
		} else {
			slog.Info("first install, pruning cache...", "version", version)
		}
		if err := svc.PruneCaches(ctx, 0); err != nil {
			slog.Warn("failed to prune caches on version change", "error", err)
		}
	}

	if err := database.SetMeta(ctx, "version", version); err != nil {
		slog.Warn("failed to set version in meta", "error", err)
	}
}
