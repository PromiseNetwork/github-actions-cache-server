package storage

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestUploadPart(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFilesystem(root)
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}

	data := []byte("hello world")
	if err := fs.UploadPart("upload-1", 1, bytes.NewReader(data)); err != nil {
		t.Fatalf("UploadPart: %v", err)
	}

	partPath := filepath.Join(root, BaseFolderName, UploadFolderName, "upload-1", "part_1")
	got, err := os.ReadFile(partPath)
	if err != nil {
		t.Fatalf("read part file: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("part content = %q, want %q", got, data)
	}
}

func TestCompleteMultipartUpload(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFilesystem(root)
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}

	parts := [][]byte{
		[]byte("part-one-"),
		[]byte("part-two-"),
		[]byte("part-three"),
	}
	for i, data := range parts {
		if err := fs.UploadPart("upload-2", i+1, bytes.NewReader(data)); err != nil {
			t.Fatalf("UploadPart %d: %v", i+1, err)
		}
	}

	cacheFileName := "testcache.tar"
	if err := fs.CompleteMultipartUpload(cacheFileName, "upload-2", []int{1, 2, 3}); err != nil {
		t.Fatalf("CompleteMultipartUpload: %v", err)
	}

	finalPath := filepath.Join(root, BaseFolderName, cacheFileName)
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}

	want := []byte("part-one-part-two-part-three")
	if !bytes.Equal(got, want) {
		t.Errorf("final content = %q, want %q", got, want)
	}

	// Upload dir should be cleaned up
	uploadDir := filepath.Join(root, BaseFolderName, UploadFolderName, "upload-2")
	if _, err := os.Stat(uploadDir); !os.IsNotExist(err) {
		t.Errorf("upload dir still exists after complete")
	}
}

func TestCreateReadStream(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFilesystem(root)
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}

	// Write a file directly
	data := []byte("cached content")
	filePath := filepath.Join(root, BaseFolderName, "myfile.tar")
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Existing file
	rc, err := fs.CreateReadStream("myfile.tar")
	if err != nil {
		t.Fatalf("CreateReadStream: %v", err)
	}
	if rc == nil {
		t.Fatal("expected non-nil reader for existing file")
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("content = %q, want %q", got, data)
	}

	// Missing file
	rc, err = fs.CreateReadStream("nonexistent.tar")
	if err != nil {
		t.Fatalf("CreateReadStream missing: %v", err)
	}
	if rc != nil {
		rc.Close()
		t.Error("expected nil reader for missing file")
	}
}

func TestDelete(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFilesystem(root)
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}

	// Create files
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("file%d.tar", i)
		path := filepath.Join(root, BaseFolderName, name)
		if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	if err := fs.Delete([]string{"file0.tar", "file1.tar"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Deleted files should not exist
	for _, name := range []string{"file0.tar", "file1.tar"} {
		path := filepath.Join(root, BaseFolderName, name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("file %s still exists", name)
		}
	}

	// file2 should still exist
	path := filepath.Join(root, BaseFolderName, "file2.tar")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file2.tar should still exist: %v", err)
	}

	// Deleting missing files should not error
	if err := fs.Delete([]string{"nonexistent.tar"}); err != nil {
		t.Errorf("Delete missing file should not error: %v", err)
	}
}

func TestCleanupMultipartUpload(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFilesystem(root)
	if err != nil {
		t.Fatalf("NewFilesystem: %v", err)
	}

	// Upload some parts
	for i := 1; i <= 3; i++ {
		if err := fs.UploadPart("cleanup-test", i, bytes.NewReader([]byte("data"))); err != nil {
			t.Fatalf("UploadPart %d: %v", i, err)
		}
	}

	uploadDir := filepath.Join(root, BaseFolderName, UploadFolderName, "cleanup-test")
	if _, err := os.Stat(uploadDir); err != nil {
		t.Fatalf("upload dir should exist: %v", err)
	}

	if err := fs.CleanupMultipartUpload("cleanup-test"); err != nil {
		t.Fatalf("CleanupMultipartUpload: %v", err)
	}

	if _, err := os.Stat(uploadDir); !os.IsNotExist(err) {
		t.Error("upload dir should be removed after cleanup")
	}
}
