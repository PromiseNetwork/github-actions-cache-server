#!/usr/bin/env bash
set -euo pipefail

# Stress test: Node (Nitro) vs Go cache server
# Hammers the server with concurrent requests using hey

PORT=3987
BASE_URL="http://localhost:$PORT"
API_URL="$BASE_URL/_apis/artifactcache"
CONCURRENCY=${1:-50}
TOTAL_REQUESTS=${2:-500}

BOLD='\033[1m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[0;33m'
NC='\033[0m'

MONITOR_PID=""
RESOURCE_LOG="/tmp/stress-resources.log"

cleanup() {
  if [[ -n "${MONITOR_PID:-}" ]]; then
    kill "$MONITOR_PID" 2>/dev/null || true
    wait "$MONITOR_PID" 2>/dev/null || true
    MONITOR_PID=""
  fi
  if [[ -n "${SERVER_PID:-}" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf .data/bench-storage .data/bench-cache.db .data/bench-cache.db-wal .data/bench-cache.db-shm
  rm -f /tmp/stress-download.bin
}
trap cleanup EXIT

# Monitor CPU% and RSS memory of a process tree, sampling every 0.5s
start_monitor() {
  local pid=$1
  > "$RESOURCE_LOG"
  (
    while kill -0 "$pid" 2>/dev/null; do
      # Sum CPU and RSS across the process and all children (for Node cluster)
      local all_pids
      all_pids=$(pgrep -P "$pid" 2>/dev/null | tr '\n' ',' || true)
      all_pids="${pid},${all_pids%,}"
      # Sum all CPU and RSS for the process group
      ps -p "$all_pids" -o %cpu=,rss= 2>/dev/null | awk '
        { cpu += $1; rss += $2 }
        END { if(NR>0) printf "%.1f %d\n", cpu, rss }
      ' >> "$RESOURCE_LOG"
      sleep 0.5
    done
  ) &
  MONITOR_PID=$!
}

stop_monitor_and_report() {
  local label=$1
  if [[ -n "${MONITOR_PID:-}" ]]; then
    kill "$MONITOR_PID" 2>/dev/null || true
    wait "$MONITOR_PID" 2>/dev/null || true
    MONITOR_PID=""
  fi

  if [[ ! -s "$RESOURCE_LOG" ]]; then
    echo -e "  ${YELLOW}No resource data collected${NC}"
    return
  fi

  # For Node cluster mode, also sum child processes
  # But we monitor the main PID which ps reports for
  local avg_cpu max_cpu avg_rss max_rss samples
  samples=$(wc -l < "$RESOURCE_LOG" | tr -d ' ')
  avg_cpu=$(awk '{ sum += $1; n++ } END { if(n>0) printf "%.1f", sum/n; else print "0" }' "$RESOURCE_LOG")
  max_cpu=$(awk '{ if($1 > max) max=$1 } END { printf "%.1f", max }' "$RESOURCE_LOG")
  avg_rss=$(awk '{ sum += $2; n++ } END { if(n>0) printf "%.1f", sum/n/1024; else print "0" }' "$RESOURCE_LOG")
  max_rss=$(awk '{ if($2 > max) max=$2 } END { printf "%.1f", max/1024 }' "$RESOURCE_LOG")

  echo -e "\n${GREEN}Resource usage ($label) [$samples samples]:${NC}"
  printf "  %-16s %8s%%  (peak: %s%%)\n" "CPU avg:" "$avg_cpu" "$max_cpu"
  printf "  %-16s %8s MB (peak: %s MB)\n" "Memory avg:" "$avg_rss" "$max_rss"
}

cd "$(dirname "$0")/.."

# Check for hey
if ! command -v hey &>/dev/null; then
  echo "Installing hey..."
  go install github.com/rakyll/hey@latest
fi

# Generate payloads
head -c $((1 * 1024 * 1024)) /dev/urandom > /tmp/stress-chunk.bin

wait_for_server() {
  local retries=50
  while ! curl -sf "$BASE_URL/healthz" > /dev/null 2>&1 && \
        ! curl -sf "$BASE_URL/" > /dev/null 2>&1; do
    retries=$((retries - 1))
    if [[ $retries -le 0 ]]; then echo "Server failed to start"; exit 1; fi
    sleep 0.2
  done
  sleep 0.5
}

# Seed the server with cache entries for lookup/download tests
seed_caches() {
  local count=$1
  echo "  Seeding $count cache entries..."
  for i in $(seq 1 "$count"); do
    local key="stress-seed-${i}"
    local version="v${i}"
    local resp
    resp=$(curl -sf -X POST "$API_URL/caches" \
      -H "Content-Type: application/json" \
      -d "{\"key\":\"$key\",\"version\":\"$version\"}")
    local cid
    cid=$(echo "$resp" | grep -o '"cacheId":[0-9]*' | grep -o '[0-9]*')
    if [[ -n "$cid" ]]; then
      curl -sf -X PATCH "$API_URL/caches/$cid" \
        -H "Content-Type: application/octet-stream" \
        -H "Content-Range: bytes 0-1023/*" \
        --data-binary @<(head -c 1024 /dev/urandom) > /dev/null
      curl -sf -X POST "$API_URL/caches/$cid" \
        -H "Content-Type: application/json" \
        -d '{"size":1024}' > /dev/null
    fi
  done
}

run_stress() {
  local label="$1"
  echo -e "\n${BOLD}${CYAN}=== Stress Test: $label ===${NC}"
  echo -e "Concurrency: $CONCURRENCY | Total requests: $TOTAL_REQUESTS\n"

  wait_for_server
  seed_caches 100

  # --- Test 1: Reserve (POST) ---
  echo -e "${YELLOW}[1/5] Reserve (POST /_apis/artifactcache/caches)${NC}"
  # hey doesn't support unique bodies per request, so we test raw throughput
  # of the endpoint. Each request creates a cache with a hash-based key.
  hey -n "$TOTAL_REQUESTS" -c "$CONCURRENCY" -m POST \
    -H "Content-Type: application/json" \
    -d '{"key":"stress-reserve-UNIQUE","version":"v1"}' \
    "$API_URL/caches" 2>&1 | tail -20

  # --- Test 2: Lookup (GET) ---
  echo -e "\n${YELLOW}[2/5] Lookup (GET /_apis/artifactcache/cache)${NC}"
  hey -n "$TOTAL_REQUESTS" -c "$CONCURRENCY" \
    "$API_URL/cache?keys=stress-seed-1&version=v1" 2>&1 | tail -20

  # --- Test 3: Download ---
  # Get the download URL first
  local lookup_resp archive_url
  lookup_resp=$(curl -sf "$API_URL/cache?keys=stress-seed-1&version=v1")
  archive_url=$(echo "$lookup_resp" | grep -o '"archiveLocation":"[^"]*"' | sed 's/"archiveLocation":"//;s/"//')

  if [[ -n "$archive_url" ]]; then
    echo -e "\n${YELLOW}[3/5] Download (GET /download/...)${NC}"
    hey -n "$TOTAL_REQUESTS" -c "$CONCURRENCY" \
      "$archive_url" 2>&1 | tail -20
  else
    echo -e "\n${YELLOW}[3/5] Download - SKIPPED (no archive URL)${NC}"
  fi

  # --- Test 4: Upload (1MB chunks) ---
  echo -e "\n${YELLOW}[4/5] Upload 1MB chunks (PATCH /_apis/artifactcache/caches/{id})${NC}"
  # Reserve a single cache to upload to
  local reserve_resp cache_id
  reserve_resp=$(curl -sf -X POST "$API_URL/caches" \
    -H "Content-Type: application/json" \
    -d "{\"key\":\"stress-upload-target-$(date +%s)\",\"version\":\"v1\"}")
  cache_id=$(echo "$reserve_resp" | grep -o '"cacheId":[0-9]*' | grep -o '[0-9]*')

  if [[ -n "$cache_id" ]]; then
    # Upload same chunk many times (server handles idempotent chunk index)
    hey -n "$((TOTAL_REQUESTS / 5))" -c "$((CONCURRENCY / 2))" -m PATCH \
      -H "Content-Type: application/octet-stream" \
      -H "Content-Range: bytes 0-1048575/*" \
      -D /tmp/stress-chunk.bin \
      "$API_URL/caches/$cache_id" 2>&1 | tail -20
  else
    echo "  SKIPPED (could not reserve cache)"
  fi

  # --- Test 5: Full lifecycle under concurrency ---
  echo -e "\n${YELLOW}[5/5] Full lifecycle (reserve+upload+commit+lookup) x${CONCURRENCY} concurrent${NC}"
  local start_time end_time successes=0 failures=0

  start_time=$(perl -MTime::HiRes=time -e 'printf "%.6f\n", time')

  # Run CONCURRENCY parallel full-lifecycle operations
  local pids=()
  for i in $(seq 1 "$CONCURRENCY"); do
    (
      local k="stress-lifecycle-${i}-$$-$(date +%N)"
      local v="v${i}"

      # Reserve
      local r
      r=$(curl -sf -X POST "$API_URL/caches" \
        -H "Content-Type: application/json" \
        -d "{\"key\":\"$k\",\"version\":\"$v\"}" 2>/dev/null) || exit 1
      local id
      id=$(echo "$r" | grep -o '"cacheId":[0-9]*' | grep -o '[0-9]*')
      [[ -z "$id" ]] && exit 1

      # Upload 1MB
      curl -sf -X PATCH "$API_URL/caches/$id" \
        -H "Content-Type: application/octet-stream" \
        -H "Content-Range: bytes 0-1048575/*" \
        --data-binary @/tmp/stress-chunk.bin > /dev/null 2>&1 || exit 1

      # Commit
      curl -sf -X POST "$API_URL/caches/$id" \
        -H "Content-Type: application/json" \
        -d '{"size":1048576}' > /dev/null 2>&1 || exit 1

      # Lookup
      curl -sf "$API_URL/cache?keys=$k&version=$v" > /dev/null 2>&1 || exit 1
    ) &
    pids+=($!)
  done

  for pid in "${pids[@]}"; do
    if wait "$pid" 2>/dev/null; then
      successes=$((successes + 1))
    else
      failures=$((failures + 1))
    fi
  done

  end_time=$(perl -MTime::HiRes=time -e 'printf "%.6f\n", time')
  local elapsed
  elapsed=$(perl -e "printf '%.3f', $end_time - $start_time")

  echo "  $CONCURRENCY concurrent full lifecycles (1MB each):"
  echo "  Completed: ${successes}/${CONCURRENCY} | Failed: ${failures} | Time: ${elapsed}s"
  local ops_per_sec
  ops_per_sec=$(perl -e "printf '%.1f', $successes / ($end_time - $start_time)")
  echo "  Throughput: ${ops_per_sec} full lifecycles/sec"
}

echo -e "${BOLD}Cache Server Stress Test: Node vs Go${NC}"
echo "======================================="
echo -e "Concurrency: $CONCURRENCY | Requests per test: $TOTAL_REQUESTS\n"

# --- Go ---
echo -e "${CYAN}Building Go server...${NC}"
go build -o .output/cache-server ./cmd/server/

cleanup
API_BASE_URL="$BASE_URL" \
STORAGE_DRIVER=filesystem \
STORAGE_FILESYSTEM_PATH=.data/bench-storage \
DB_DRIVER=sqlite \
DB_SQLITE_PATH=.data/bench-cache.db \
PORT="$PORT" \
.output/cache-server &>/dev/null &
SERVER_PID=$!

start_monitor "$SERVER_PID"
run_stress "Go"
stop_monitor_and_report "Go"
kill "$SERVER_PID" 2>/dev/null; wait "$SERVER_PID" 2>/dev/null || true
unset SERVER_PID

# --- Node ---
echo -e "\n${CYAN}Building Node server...${NC}"
pnpm run build 2>&1 | tail -1

cleanup
API_BASE_URL="$BASE_URL" \
STORAGE_DRIVER=filesystem \
STORAGE_FILESYSTEM_PATH=.data/bench-storage \
DB_DRIVER=sqlite \
DB_SQLITE_PATH=.data/bench-cache.db \
NITRO_PORT="$PORT" \
node .output/server/index.mjs &>/dev/null &
SERVER_PID=$!

start_monitor "$SERVER_PID"
run_stress "Node"
stop_monitor_and_report "Node"
kill "$SERVER_PID" 2>/dev/null; wait "$SERVER_PID" 2>/dev/null || true
unset SERVER_PID

rm -f /tmp/stress-chunk.bin
echo -e "\n${BOLD}${GREEN}Stress test complete.${NC}"
