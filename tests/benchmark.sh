#!/usr/bin/env bash
set -euo pipefail

# Benchmark: Node (Nitro) vs Go cache server
# Tests full cache lifecycle: reserve → upload → commit → lookup → download

PORT=3987
BASE_URL="http://localhost:$PORT"
API_URL="$BASE_URL/_apis/artifactcache"
ITERATIONS=${1:-50}
CHUNK_SIZE=${2:-$((1 * 1024 * 1024))} # default 1MB

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

cleanup() {
  if [[ -n "${SERVER_PID:-}" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf .data/bench-storage .data/bench-cache.db .data/bench-cache.db-wal .data/bench-cache.db-shm
  rm -f /tmp/bench-download.bin
}
trap cleanup EXIT

# Generate test payload once
head -c "$CHUNK_SIZE" /dev/urandom > /tmp/bench-chunk.bin

wait_for_server() {
  local retries=30
  while ! curl -sf "$BASE_URL/healthz" > /dev/null 2>&1 && \
        ! curl -sf "$BASE_URL/" > /dev/null 2>&1; do
    retries=$((retries - 1))
    if [[ $retries -le 0 ]]; then
      echo "Server failed to start"
      exit 1
    fi
    sleep 0.2
  done
}

run_benchmark() {
  local label="$1"
  echo -e "\n${BOLD}${CYAN}=== Benchmarking: $label ===${NC}"
  echo -e "Iterations: $ITERATIONS | Chunk size: $((CHUNK_SIZE / 1024))KB\n"

  wait_for_server

  local total_reserve=0
  local total_upload=0
  local total_commit=0
  local total_lookup=0
  local total_download=0
  local start_all end_all

  start_all=$(perl -MTime::HiRes=time -e 'printf "%.6f\n", time')

  for i in $(seq 1 "$ITERATIONS"); do
    local key="bench-key-${label}-${i}-$$"
    local version="bench-version-${i}"

    # Reserve
    local t0 t1
    t0=$(perl -MTime::HiRes=time -e 'printf "%.6f\n", time')
    local reserve_resp
    reserve_resp=$(curl -sf -X POST "$API_URL/caches" \
      -H "Content-Type: application/json" \
      -d "{\"key\":\"$key\",\"version\":\"$version\"}")
    t1=$(perl -MTime::HiRes=time -e 'printf "%.6f\n", time')
    total_reserve=$(perl -e "printf '%.6f', $total_reserve + $t1 - $t0")

    local cache_id
    cache_id=$(echo "$reserve_resp" | grep -o '"cacheId":[0-9]*' | grep -o '[0-9]*')
    if [[ -z "$cache_id" ]]; then
      echo "Failed to reserve cache at iteration $i: $reserve_resp"
      return 1
    fi

    # Upload
    t0=$(perl -MTime::HiRes=time -e 'printf "%.6f\n", time')
    curl -sf -X PATCH "$API_URL/caches/$cache_id" \
      -H "Content-Type: application/octet-stream" \
      -H "Content-Range: bytes 0-$((CHUNK_SIZE - 1))/*" \
      --data-binary @/tmp/bench-chunk.bin > /dev/null
    t1=$(perl -MTime::HiRes=time -e 'printf "%.6f\n", time')
    total_upload=$(perl -e "printf '%.6f', $total_upload + $t1 - $t0")

    # Commit
    t0=$(perl -MTime::HiRes=time -e 'printf "%.6f\n", time')
    curl -sf -X POST "$API_URL/caches/$cache_id" \
      -H "Content-Type: application/json" \
      -d "{\"size\":$CHUNK_SIZE}" > /dev/null
    t1=$(perl -MTime::HiRes=time -e 'printf "%.6f\n", time')
    total_commit=$(perl -e "printf '%.6f', $total_commit + $t1 - $t0")

    # Lookup
    t0=$(perl -MTime::HiRes=time -e 'printf "%.6f\n", time')
    local lookup_resp
    lookup_resp=$(curl -sf "$API_URL/cache?keys=$key&version=$version")
    t1=$(perl -MTime::HiRes=time -e 'printf "%.6f\n", time')
    total_lookup=$(perl -e "printf '%.6f', $total_lookup + $t1 - $t0")

    local archive_url
    archive_url=$(echo "$lookup_resp" | grep -o '"archiveLocation":"[^"]*"' | sed 's/"archiveLocation":"//;s/"//')

    # Download
    t0=$(perl -MTime::HiRes=time -e 'printf "%.6f\n", time')
    curl -sf "$archive_url" -o /tmp/bench-download.bin
    t1=$(perl -MTime::HiRes=time -e 'printf "%.6f\n", time')
    total_download=$(perl -e "printf '%.6f', $total_download + $t1 - $t0")
  done

  end_all=$(perl -MTime::HiRes=time -e 'printf "%.6f\n", time')
  local wall_time
  wall_time=$(perl -e "printf '%.3f', $end_all - $start_all")

  # Print results
  local avg_reserve avg_upload avg_commit avg_lookup avg_download
  avg_reserve=$(perl -e "printf '%.2f', ($total_reserve / $ITERATIONS) * 1000")
  avg_upload=$(perl -e "printf '%.2f', ($total_upload / $ITERATIONS) * 1000")
  avg_commit=$(perl -e "printf '%.2f', ($total_commit / $ITERATIONS) * 1000")
  avg_lookup=$(perl -e "printf '%.2f', ($total_lookup / $ITERATIONS) * 1000")
  avg_download=$(perl -e "printf '%.2f', ($total_download / $ITERATIONS) * 1000")
  local total_avg
  total_avg=$(perl -e "printf '%.2f', (($total_reserve + $total_upload + $total_commit + $total_lookup + $total_download) / $ITERATIONS) * 1000")

  echo -e "${GREEN}Results (avg ms per operation):${NC}"
  printf "  %-12s %8s ms\n" "Reserve:" "$avg_reserve"
  printf "  %-12s %8s ms\n" "Upload:" "$avg_upload"
  printf "  %-12s %8s ms\n" "Commit:" "$avg_commit"
  printf "  %-12s %8s ms\n" "Lookup:" "$avg_lookup"
  printf "  %-12s %8s ms\n" "Download:" "$avg_download"
  echo -e "  ${BOLD}%-12s %8s ms${NC}" "Total:" "$total_avg"
  echo -e "  Wall time:   ${wall_time}s"
}

cd "$(dirname "$0")/.."

echo -e "${BOLD}Cache Server Benchmark: Node vs Go${NC}"
echo "====================================="

# --- Go Server ---
echo -e "\n${CYAN}Building Go server...${NC}"
go build -o .output/cache-server ./cmd/server/

cleanup
API_BASE_URL="$BASE_URL" \
STORAGE_DRIVER=filesystem \
STORAGE_FILESYSTEM_PATH=.data/bench-storage \
DB_DRIVER=sqlite \
DB_SQLITE_PATH=.data/bench-cache.db \
PORT="$PORT" \
.output/cache-server &
SERVER_PID=$!

run_benchmark "Go"
kill "$SERVER_PID" 2>/dev/null; wait "$SERVER_PID" 2>/dev/null || true
unset SERVER_PID

# --- Node Server ---
echo -e "\n${CYAN}Building Node server...${NC}"
pnpm run build 2>&1 | tail -1

cleanup
API_BASE_URL="$BASE_URL" \
STORAGE_DRIVER=filesystem \
STORAGE_FILESYSTEM_PATH=.data/bench-storage \
DB_DRIVER=sqlite \
DB_SQLITE_PATH=.data/bench-cache.db \
NITRO_PORT="$PORT" \
node .output/server/index.mjs &
SERVER_PID=$!

run_benchmark "Node"
kill "$SERVER_PID" 2>/dev/null; wait "$SERVER_PID" 2>/dev/null || true
unset SERVER_PID

rm -f /tmp/bench-chunk.bin
echo -e "\n${BOLD}${GREEN}Benchmark complete.${NC}"
