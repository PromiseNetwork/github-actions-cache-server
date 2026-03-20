package api

import (
	"encoding/base64"
	"encoding/binary"
	"testing"
)

func TestGetChunkIndexFromBlockID_BuildxFormat(t *testing.T) {
	// 64-byte block with uint32 big-endian at offset 16
	block := make([]byte, 64)
	binary.BigEndian.PutUint32(block[16:20], 42)

	encoded := base64.StdEncoding.EncodeToString(block)
	idx, err := getChunkIndexFromBlockID(encoded)
	if err != nil {
		t.Fatalf("getChunkIndexFromBlockID: %v", err)
	}
	if idx != 42 {
		t.Errorf("index = %d, want 42", idx)
	}
}

func TestGetChunkIndexFromBlockID_StandardFormat(t *testing.T) {
	// 48-byte block: 36-byte UUID + "00000005" (padded to 12 bytes)
	uuid := "550e8400-e29b-41d4-a716-446655440000" // 36 chars
	indexPart := "00000005    "                      // 12 chars, padded with spaces
	block := []byte(uuid + indexPart)

	if len(block) != 48 {
		t.Fatalf("block length = %d, want 48", len(block))
	}

	encoded := base64.StdEncoding.EncodeToString(block)
	idx, err := getChunkIndexFromBlockID(encoded)
	if err != nil {
		t.Fatalf("getChunkIndexFromBlockID: %v", err)
	}
	if idx != 5 {
		t.Errorf("index = %d, want 5", idx)
	}
}

func TestGetChunkIndexFromBlockID_InvalidLength(t *testing.T) {
	// 32 bytes - neither 64 nor 48
	block := make([]byte, 32)
	encoded := base64.StdEncoding.EncodeToString(block)

	_, err := getChunkIndexFromBlockID(encoded)
	if err == nil {
		t.Fatal("expected error for invalid block length")
	}
}
