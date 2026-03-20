package storage

import "io"

const (
	BaseFolderName   = "gh-actions-cache"
	UploadFolderName = ".uploads"
)

// StorageDriver defines the interface for cache storage backends.
type StorageDriver interface {
	// Delete removes cache files by their file names.
	Delete(cacheFileNames []string) error

	// CreateReadStream returns a reader for the given cache file.
	// Returns nil, nil if the file does not exist.
	CreateReadStream(cacheFileName string) (io.ReadCloser, error)

	// CreateDownloadURL returns a signed/direct URL for downloading the cache file.
	// Returns empty string if not supported.
	CreateDownloadURL(cacheFileName string) (string, error)

	// UploadPart uploads a single part of a multipart upload.
	UploadPart(uploadID string, partNumber int, data io.Reader) error

	// CompleteMultipartUpload combines all parts into the final cache file.
	CompleteMultipartUpload(cacheFileName, uploadID string, partNumbers []int) error

	// CleanupMultipartUpload removes temporary part files for an upload.
	CleanupMultipartUpload(uploadID string) error
}
