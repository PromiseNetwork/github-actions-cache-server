package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type FilesystemDriver struct {
	rootPath string
}

func NewFilesystem(rootPath string) (*FilesystemDriver, error) {
	if rootPath == "" {
		rootPath = ".data/storage/filesystem"
	}

	cacheDir := filepath.Join(rootPath, BaseFolderName)
	uploadDir := filepath.Join(rootPath, BaseFolderName, UploadFolderName)

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		return nil, fmt.Errorf("create upload dir: %w", err)
	}

	return &FilesystemDriver{rootPath: rootPath}, nil
}

func (f *FilesystemDriver) Delete(cacheFileNames []string) error {
	for _, name := range cacheFileNames {
		path := filepath.Join(f.rootPath, BaseFolderName, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (f *FilesystemDriver) CreateReadStream(cacheFileName string) (io.ReadCloser, error) {
	path := filepath.Join(f.rootPath, BaseFolderName, cacheFileName)
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (f *FilesystemDriver) CreateDownloadURL(_ string) (string, error) {
	return "", nil
}

func (f *FilesystemDriver) UploadPart(uploadID string, partNumber int, data io.Reader) error {
	dir := filepath.Join(f.rootPath, BaseFolderName, UploadFolderName, uploadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	partPath := filepath.Join(dir, fmt.Sprintf("part_%d", partNumber))
	file, err := os.Create(partPath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(file, data)
	return err
}

func (f *FilesystemDriver) CompleteMultipartUpload(cacheFileName, uploadID string, partNumbers []int) error {
	destPath := filepath.Join(f.rootPath, BaseFolderName, cacheFileName)
	dest, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer dest.Close()

	for _, pn := range partNumbers {
		partPath := filepath.Join(f.rootPath, BaseFolderName, UploadFolderName, uploadID, fmt.Sprintf("part_%d", pn))
		part, err := os.Open(partPath)
		if err != nil {
			return fmt.Errorf("open part %d: %w", pn, err)
		}
		_, err = io.Copy(dest, part)
		part.Close()
		if err != nil {
			return fmt.Errorf("copy part %d: %w", pn, err)
		}
	}

	return f.CleanupMultipartUpload(uploadID)
}

func (f *FilesystemDriver) CleanupMultipartUpload(uploadID string) error {
	dir := filepath.Join(f.rootPath, BaseFolderName, UploadFolderName, uploadID)
	return os.RemoveAll(dir)
}
