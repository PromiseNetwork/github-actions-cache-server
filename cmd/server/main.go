package main

import (
	"context"
	"errors"
	"fmt"
	"log"
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
		log.Fatalf("failed to load config: %v", err)
	}

	if cfg.Debug {
		log.Println("debug mode enabled")
	}

	ctx := context.Background()

	// Initialize DB
	database, err := newDB(cfg)
	if err != nil {
		log.Fatalf("failed to init db: %v", err)
	}
	defer database.Close()

	if err := database.Migrate(ctx); err != nil {
		log.Fatalf("failed to migrate db: %v", err)
	}
	log.Printf("using database driver: %s", cfg.DBDriver)

	// Initialize storage driver
	storageDriver, err := newStorage(ctx, cfg)
	if err != nil {
		log.Fatalf("failed to init storage: %v", err)
	}
	log.Printf("using storage driver: %s", cfg.StorageDriver)

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
			log.Printf("warning: failed to init redis, falling back to direct DB: %v", err)
		} else {
			cacheService = redisLayer
			defer redisLayer.Close()
			log.Println("redis cache layer enabled")
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
				log.Printf("cache cleanup error: %v", err)
			}
		})
		log.Printf("cache cleanup: entries older than %dd, schedule: %s", cfg.CacheCleanupOlderDays, cfg.CacheCleanupCron)
	}
	c.AddFunc(cfg.UploadCleanupCron, func() {
		if err := svc.PruneUploads(context.Background(), 24*time.Hour); err != nil {
			log.Printf("upload cleanup error: %v", err)
		}
	})
	log.Printf("upload cleanup schedule: %s", cfg.UploadCleanupCron)
	c.Start()

	// Graceful shutdown
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("starting github-actions-cache-server on :%d", cfg.Port)
		log.Printf("API base URL: %s", cfg.APIBaseURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-sigCtx.Done()
	log.Println("shutting down server...")

	c.Stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("server shutdown error: %v", err)
	}

	log.Println("server stopped")
}

func newDB(cfg *config.Config) (db.DB, error) {
	switch cfg.DBDriver {
	case "sqlite":
		path := os.Getenv("SQLITE_PATH")
		if path == "" {
			path = ".data/cache.db"
		}
		return db.NewSQLite(path)
	case "postgres":
		connStr := os.Getenv("DATABASE_URL")
		if connStr == "" {
			return nil, fmt.Errorf("DATABASE_URL is required for postgres driver")
		}
		return db.NewPostgres(connStr)
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
	default:
		return nil, fmt.Errorf("unsupported storage driver: %s", cfg.StorageDriver)
	}
}

func handleVersionCheck(ctx context.Context, database db.DB, svc *cache.CacheService) {
	version := "1.0.0" // TODO: inject via ldflags
	existing, err := database.GetMeta(ctx, "version")
	if err != nil {
		log.Printf("warning: failed to get version from meta: %v", err)
		return
	}

	if existing == nil || *existing != version {
		if existing != nil {
			log.Printf("version changed from %s to %s, pruning cache...", *existing, version)
		} else {
			log.Printf("first install (version %s), pruning cache...", version)
		}
		if err := svc.PruneCaches(ctx, 0); err != nil {
			log.Printf("warning: failed to prune caches on version change: %v", err)
		}
	}

	if err := database.SetMeta(ctx, "version", version); err != nil {
		log.Printf("warning: failed to set version in meta: %v", err)
	}
}
