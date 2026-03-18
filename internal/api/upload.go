package api

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/promisenetwork/github-actions-cache-server/internal/cache"
)

const (
	mbSize         = 1024 * 1024
	defaultChunkMB = 64
	buildxChunkMB  = 1
)

func handleBlobUpload(svc cache.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cacheID := r.PathValue("cacheId")

		// Handle blocklist commit (comp=blocklist)
		if r.URL.Query().Get("comp") == "blocklist" {
			w.Header().Set("x-ms-request-id", randomHex(16))
			w.WriteHeader(http.StatusCreated)
			return
		}

		blockID := r.URL.Query().Get("blockid")
		chunkIndex := 0

		if blockID != "" {
			idx, err := getChunkIndexFromBlockID(blockID)
			if err != nil {
				http.Error(w, "invalid block id", http.StatusBadRequest)
				return
			}
			chunkIndex = idx
		}

		if err := svc.UploadChunk(r.Context(), cacheID, r.Body, chunkIndex); err != nil {
			log.Printf("error uploading chunk: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("x-ms-request-id", randomHex(16))
		w.WriteHeader(http.StatusCreated)
	}
}

// getChunkIndexFromBlockID decodes a base64-encoded block ID.
// Docker buildx uses 64-byte blocks with uint32BE at offset 16.
// Standard clients use 48-byte blocks with UUID prefix + index.
func getChunkIndexFromBlockID(blockIDBase64 string) (int, error) {
	decoded, err := base64.StdEncoding.DecodeString(blockIDBase64)
	if err != nil {
		return 0, err
	}

	switch len(decoded) {
	case 64:
		// Docker buildx format: uint32 big-endian at offset 16
		return int(binary.BigEndian.Uint32(decoded[16:20])), nil
	case 48:
		// Standard format: 36-byte UUID prefix + numeric index
		s := string(decoded)
		indexStr := strings.TrimSpace(s[36:])
		return strconv.Atoi(indexStr)
	default:
		return 0, fmt.Errorf("unexpected block id length: %d", len(decoded))
	}
}
