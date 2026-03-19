package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/promisenetwork/github-actions-cache-server/internal/cache"
	"github.com/promisenetwork/github-actions-cache-server/internal/config"
	"github.com/promisenetwork/github-actions-cache-server/internal/db"
)

// --- Mock DB (only methods used by handlers) ---

type mockDB struct {
	listEntriesByKeyFn func(ctx context.Context, key string) ([]db.CacheKey, error)
	getUploadFn        func(ctx context.Context, key, version string) (*db.Upload, error)
}

func (m *mockDB) ListEntriesByKey(ctx context.Context, key string) ([]db.CacheKey, error) {
	if m.listEntriesByKeyFn != nil {
		return m.listEntriesByKeyFn(ctx, key)
	}
	return nil, nil
}

func (m *mockDB) GetUpload(ctx context.Context, key, version string) (*db.Upload, error) {
	if m.getUploadFn != nil {
		return m.getUploadFn(ctx, key, version)
	}
	return nil, nil
}

// Unused DB methods — satisfy the interface.
func (m *mockDB) FindKeyMatch(context.Context, string, string, []string) (*db.CacheKey, error) {
	return nil, nil
}
func (m *mockDB) UpdateOrCreateKey(context.Context, string, string) error { return nil }
func (m *mockDB) TouchKey(context.Context, string, string) error         { return nil }
func (m *mockDB) FindStaleKeys(context.Context, int) ([]db.CacheKey, error) {
	return nil, nil
}
func (m *mockDB) CreateKey(context.Context, string, string) error        { return nil }
func (m *mockDB) PruneKeys(context.Context, []db.CacheKey) error        { return nil }
func (m *mockDB) GetUploadByID(context.Context, string) (*db.Upload, error) {
	return nil, nil
}
func (m *mockDB) CreateUpload(context.Context, db.Upload) error              { return nil }
func (m *mockDB) DeleteUpload(context.Context, string) error                 { return nil }
func (m *mockDB) CreateUploadPart(context.Context, db.UploadPart) error      { return nil }
func (m *mockDB) ListUploadParts(context.Context, string) ([]db.UploadPart, error) {
	return nil, nil
}
func (m *mockDB) ListStaleUploads(context.Context, time.Duration) ([]db.Upload, error) {
	return nil, nil
}
func (m *mockDB) Migrate(context.Context) error                { return nil }
func (m *mockDB) GetMeta(context.Context, string) (*string, error) { return nil, nil }
func (m *mockDB) SetMeta(context.Context, string, string) error    { return nil }
func (m *mockDB) Close() error                                     { return nil }

// --- Mock cache.Service ---

type mockService struct {
	reserveCacheFn  func(ctx context.Context, key, version string) (int64, error)
	uploadChunkFn   func(ctx context.Context, uploadID string, chunkStream io.Reader, chunkIndex int) error
	commitCacheFn   func(ctx context.Context, uploadID string) error
	getCacheEntryFn func(ctx context.Context, keys []string, version string) (*cache.CacheEntry, error)
	downloadFn      func(cacheFileName string) (io.ReadCloser, error)
	db              *mockDB
}

func (m *mockService) ReserveCache(ctx context.Context, key, version string) (int64, error) {
	if m.reserveCacheFn != nil {
		return m.reserveCacheFn(ctx, key, version)
	}
	return 0, nil
}

func (m *mockService) UploadChunk(ctx context.Context, uploadID string, chunkStream io.Reader, chunkIndex int) error {
	if m.uploadChunkFn != nil {
		return m.uploadChunkFn(ctx, uploadID, chunkStream, chunkIndex)
	}
	return nil
}

func (m *mockService) CommitCache(ctx context.Context, uploadID string) error {
	if m.commitCacheFn != nil {
		return m.commitCacheFn(ctx, uploadID)
	}
	return nil
}

func (m *mockService) GetCacheEntry(ctx context.Context, keys []string, version string) (*cache.CacheEntry, error) {
	if m.getCacheEntryFn != nil {
		return m.getCacheEntryFn(ctx, keys, version)
	}
	return nil, nil
}

func (m *mockService) Download(cacheFileName string) (io.ReadCloser, error) {
	if m.downloadFn != nil {
		return m.downloadFn(cacheFileName)
	}
	return nil, nil
}

func (m *mockService) PruneCaches(context.Context, int) error              { return nil }
func (m *mockService) PruneUploads(context.Context, time.Duration) error   { return nil }
func (m *mockService) GetDB() db.DB                                        { return m.db }

// --- Helpers ---

func newTestServer(svc cache.Service) *httptest.Server {
	cfg := &config.Config{
		APIBaseURL:     "http://localhost:3000",
		MetricsEnabled: false,
	}
	return httptest.NewServer(NewRouter(svc, cfg))
}

func jsonBody(v any) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

func decodeJSON(t *testing.T, r io.Reader) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	return m
}

// --- V1 Tests ---

func TestGetCacheEntry_Hit(t *testing.T) {
	svc := &mockService{
		db: &mockDB{},
		getCacheEntryFn: func(_ context.Context, keys []string, version string) (*cache.CacheEntry, error) {
			return &cache.CacheEntry{
				ArchiveLocation: "http://localhost:3000/download/abc/somefile",
				CacheKey:        keys[0],
			}, nil
		},
	}
	ts := newTestServer(svc)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/_apis/artifactcache/cache?keys=k1,k2&version=v1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	m := decodeJSON(t, resp.Body)
	if m["archiveLocation"] != "http://localhost:3000/download/abc/somefile" {
		t.Errorf("unexpected archiveLocation: %v", m["archiveLocation"])
	}
	if m["cacheKey"] != "k1" {
		t.Errorf("unexpected cacheKey: %v", m["cacheKey"])
	}
}

func TestGetCacheEntry_Miss(t *testing.T) {
	svc := &mockService{db: &mockDB{}}
	ts := newTestServer(svc)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/_apis/artifactcache/cache?keys=k1&version=v1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 204 {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

func TestGetCacheEntry_MissingParams(t *testing.T) {
	svc := &mockService{db: &mockDB{}}
	ts := newTestServer(svc)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/_apis/artifactcache/cache")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestListEntries(t *testing.T) {
	svc := &mockService{
		db: &mockDB{
			listEntriesByKeyFn: func(_ context.Context, key string) ([]db.CacheKey, error) {
				return []db.CacheKey{
					{Key: "k1", Version: "v1"},
					{Key: "k1", Version: "v2"},
				}, nil
			},
		},
	}
	ts := newTestServer(svc)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/_apis/artifactcache/caches?key=k1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	m := decodeJSON(t, resp.Body)
	if m["totalCount"] != float64(2) {
		t.Errorf("expected totalCount=2, got %v", m["totalCount"])
	}
	caches, ok := m["artifactCaches"].([]any)
	if !ok || len(caches) != 2 {
		t.Fatalf("expected 2 artifactCaches, got %v", m["artifactCaches"])
	}
	first := caches[0].(map[string]any)
	if first["cacheKey"] != "k1" || first["cacheVersion"] != "v1" {
		t.Errorf("unexpected first entry: %v", first)
	}
}

func TestReserveCache(t *testing.T) {
	svc := &mockService{
		db: &mockDB{},
		reserveCacheFn: func(_ context.Context, key, version string) (int64, error) {
			return 1234567890, nil
		},
	}
	ts := newTestServer(svc)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/_apis/artifactcache/caches", "application/json",
		jsonBody(map[string]string{"key": "mykey", "version": "v1"}))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	m := decodeJSON(t, resp.Body)
	if m["cacheId"] != float64(1234567890) {
		t.Errorf("expected cacheId=1234567890, got %v", m["cacheId"])
	}
}

func TestUploadChunk(t *testing.T) {
	var gotCacheID string
	svc := &mockService{
		db: &mockDB{},
		uploadChunkFn: func(_ context.Context, uploadID string, _ io.Reader, _ int) error {
			gotCacheID = uploadID
			return nil
		},
	}
	ts := newTestServer(svc)
	defer ts.Close()

	req, _ := http.NewRequest("PATCH", ts.URL+"/_apis/artifactcache/caches/123", strings.NewReader("chunk data"))
	req.Header.Set("Content-Range", "bytes 0-1023/*")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 204 {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if gotCacheID != "123" {
		t.Errorf("expected cacheId=123, got %s", gotCacheID)
	}
}

func TestCommitCache(t *testing.T) {
	var gotCacheID string
	svc := &mockService{
		db: &mockDB{},
		commitCacheFn: func(_ context.Context, uploadID string) error {
			gotCacheID = uploadID
			return nil
		},
	}
	ts := newTestServer(svc)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/_apis/artifactcache/caches/123", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 204 {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if gotCacheID != "123" {
		t.Errorf("expected cacheId=123, got %s", gotCacheID)
	}
}

// --- V2 Tests ---

func TestV2CreateCacheEntry_OK(t *testing.T) {
	svc := &mockService{
		db: &mockDB{},
		reserveCacheFn: func(_ context.Context, key, version string) (int64, error) {
			return 9999, nil
		},
	}
	ts := newTestServer(svc)
	defer ts.Close()

	resp, err := http.Post(
		ts.URL+"/twirp/github.actions.results.api.v1.CacheService/CreateCacheEntry",
		"application/json",
		jsonBody(map[string]string{"key": "k1", "version": "v1"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	m := decodeJSON(t, resp.Body)
	if m["ok"] != true {
		t.Errorf("expected ok=true, got %v", m["ok"])
	}
	if m["signed_upload_url"] != "http://localhost:3000/upload/9999" {
		t.Errorf("unexpected signed_upload_url: %v", m["signed_upload_url"])
	}
	if _, exists := m["message"]; !exists {
		t.Error("expected message field")
	}
}

func TestV2CreateCacheEntry_AlreadyReserved(t *testing.T) {
	svc := &mockService{
		db: &mockDB{},
		reserveCacheFn: func(_ context.Context, key, version string) (int64, error) {
			return 0, nil // already reserved
		},
	}
	ts := newTestServer(svc)
	defer ts.Close()

	resp, err := http.Post(
		ts.URL+"/twirp/github.actions.results.api.v1.CacheService/CreateCacheEntry",
		"application/json",
		jsonBody(map[string]string{"key": "k1", "version": "v1"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	m := decodeJSON(t, resp.Body)
	if m["ok"] != false {
		t.Errorf("expected ok=false, got %v", m["ok"])
	}
	if m["message"] != "cache already reserved" {
		t.Errorf("unexpected message: %v", m["message"])
	}
}

func TestV2FinalizeCacheEntry(t *testing.T) {
	var committedID string
	svc := &mockService{
		db: &mockDB{
			getUploadFn: func(_ context.Context, key, version string) (*db.Upload, error) {
				return &db.Upload{ID: "upload-42", Key: key, Version: version}, nil
			},
		},
		commitCacheFn: func(_ context.Context, uploadID string) error {
			committedID = uploadID
			return nil
		},
	}
	ts := newTestServer(svc)
	defer ts.Close()

	resp, err := http.Post(
		ts.URL+"/twirp/github.actions.results.api.v1.CacheService/FinalizeCacheEntryUpload",
		"application/json",
		jsonBody(map[string]string{"key": "k1", "version": "v1"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	m := decodeJSON(t, resp.Body)
	if m["ok"] != true {
		t.Errorf("expected ok=true, got %v", m["ok"])
	}
	if m["entry_id"] != "upload-42" {
		t.Errorf("unexpected entry_id: %v", m["entry_id"])
	}
	if committedID != "upload-42" {
		t.Errorf("expected committedID=upload-42, got %s", committedID)
	}
}

func TestV2GetCacheEntryDownloadURL_Hit(t *testing.T) {
	svc := &mockService{
		db: &mockDB{},
		getCacheEntryFn: func(_ context.Context, keys []string, version string) (*cache.CacheEntry, error) {
			return &cache.CacheEntry{
				ArchiveLocation: "http://localhost:3000/download/rand/file",
				CacheKey:        keys[0],
			}, nil
		},
	}
	ts := newTestServer(svc)
	defer ts.Close()

	resp, err := http.Post(
		ts.URL+"/twirp/github.actions.results.api.v1.CacheService/GetCacheEntryDownloadURL",
		"application/json",
		jsonBody(map[string]any{"key": "k1", "restore_keys": []string{"k2"}, "version": "v1"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	m := decodeJSON(t, resp.Body)
	if m["ok"] != true {
		t.Errorf("expected ok=true, got %v", m["ok"])
	}
	if m["signed_download_url"] != "http://localhost:3000/download/rand/file" {
		t.Errorf("unexpected signed_download_url: %v", m["signed_download_url"])
	}
	if m["matched_key"] != "k1" {
		t.Errorf("unexpected matched_key: %v", m["matched_key"])
	}
}

func TestV2GetCacheEntryDownloadURL_Miss(t *testing.T) {
	svc := &mockService{db: &mockDB{}}
	ts := newTestServer(svc)
	defer ts.Close()

	resp, err := http.Post(
		ts.URL+"/twirp/github.actions.results.api.v1.CacheService/GetCacheEntryDownloadURL",
		"application/json",
		jsonBody(map[string]string{"key": "k1", "version": "v1"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	m := decodeJSON(t, resp.Body)
	if m["ok"] != false {
		t.Errorf("expected ok=false, got %v", m["ok"])
	}
}

// --- Upload Tests ---

func TestBlobUpload_Block(t *testing.T) {
	var gotChunkIndex int
	svc := &mockService{
		db: &mockDB{},
		uploadChunkFn: func(_ context.Context, uploadID string, _ io.Reader, chunkIndex int) error {
			gotChunkIndex = chunkIndex
			return nil
		},
	}
	ts := newTestServer(svc)
	defer ts.Close()

	// Build a standard 48-byte block ID: 36-byte UUID + 12-char index
	blockIDRaw := fmt.Sprintf("01234567-0123-0123-0123-0123456789ab%-12d", 5)
	blockID := base64.StdEncoding.EncodeToString([]byte(blockIDRaw))

	req, _ := http.NewRequest("PUT", ts.URL+"/upload/123?blockid="+blockID, strings.NewReader("data"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	if resp.Header.Get("x-ms-request-id") == "" {
		t.Error("expected x-ms-request-id header")
	}
	if gotChunkIndex != 5 {
		t.Errorf("expected chunkIndex=5, got %d", gotChunkIndex)
	}
}

func TestBlobUpload_Blocklist(t *testing.T) {
	svc := &mockService{db: &mockDB{}}
	ts := newTestServer(svc)
	defer ts.Close()

	req, _ := http.NewRequest("PUT", ts.URL+"/upload/123?comp=blocklist", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	if resp.Header.Get("x-ms-request-id") == "" {
		t.Error("expected x-ms-request-id header")
	}
}

func TestBlobUpload_BuildxBlockID(t *testing.T) {
	var gotChunkIndex int
	svc := &mockService{
		db: &mockDB{},
		uploadChunkFn: func(_ context.Context, _ string, _ io.Reader, chunkIndex int) error {
			gotChunkIndex = chunkIndex
			return nil
		},
	}
	ts := newTestServer(svc)
	defer ts.Close()

	// Build a 64-byte buildx block ID with uint32BE=7 at offset 16
	raw := make([]byte, 64)
	binary.BigEndian.PutUint32(raw[16:20], 7)
	blockID := base64.StdEncoding.EncodeToString(raw)

	req, _ := http.NewRequest("PUT", ts.URL+"/upload/123?blockid="+blockID, strings.NewReader("data"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	if gotChunkIndex != 7 {
		t.Errorf("expected chunkIndex=7, got %d", gotChunkIndex)
	}
}

// --- Download Tests ---

func TestDownload_OK(t *testing.T) {
	svc := &mockService{
		db: &mockDB{},
		downloadFn: func(cacheFileName string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("file contents")), nil
		},
	}
	ts := newTestServer(svc)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/download/abc/somefile")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("expected content-type application/octet-stream, got %s", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "file contents" {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestDownload_NotFound(t *testing.T) {
	svc := &mockService{
		db: &mockDB{},
		downloadFn: func(cacheFileName string) (io.ReadCloser, error) {
			return nil, nil // not found
		},
	}
	ts := newTestServer(svc)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/download/abc/missing")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

// --- Health Tests ---

func TestRootHealth(t *testing.T) {
	svc := &mockService{db: &mockDB{}}
	ts := newTestServer(svc)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("expected body 'ok', got %s", body)
	}
}

func TestHealthz(t *testing.T) {
	svc := &mockService{db: &mockDB{}}
	ts := newTestServer(svc)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("expected body 'ok', got %s", body)
	}
}

func TestReadyz(t *testing.T) {
	svc := &mockService{db: &mockDB{}}
	ts := newTestServer(svc)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("expected body 'ok', got %s", body)
	}
}
