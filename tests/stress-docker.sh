#!/usr/bin/env bash
set -euo pipefail

# Long-running stress test: Go vs Node cache server side-by-side in Docker
# Runs multiple rounds of increasing intensity with resource monitoring

GO_URL="http://go-server:3000"
NODE_URL="http://node-server:3000"
GO_API="$GO_URL/_apis/artifactcache"
NODE_API="$NODE_URL/_apis/artifactcache"

BOLD='\033[1m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[0;33m'
RED='\033[0;31m'
NC='\033[0m'

# Install hey
echo "Installing hey..."
go install github.com/rakyll/hey@latest 2>&1
export PATH="$PATH:$(go env GOPATH)/bin"

# Generate test payloads
echo "Generating test payloads..."
head -c $((1 * 1024 * 1024)) /dev/urandom > /tmp/chunk-1mb.bin
head -c $((10 * 1024 * 1024)) /dev/urandom > /tmp/chunk-10mb.bin
head -c $((50 * 1024 * 1024)) /dev/urandom > /tmp/chunk-50mb.bin

wait_for_servers() {
  echo "Waiting for servers..."
  for url in "$GO_URL" "$NODE_URL"; do
    local retries=60
    while ! curl -sf "$url/healthz" > /dev/null 2>&1 && \
          ! curl -sf "$url/" > /dev/null 2>&1; do
      retries=$((retries - 1))
      if [[ $retries -le 0 ]]; then
        echo "FATAL: Server at $url failed to start"
        exit 1
      fi
      sleep 1
    done
  done
  echo "Both servers ready."
}

# Seed a server with cache entries
seed() {
  local api_url=$1
  local count=$2
  local size=${3:-1024}
  for i in $(seq 1 "$count"); do
    local key="seed-${size}-${i}-$$"
    local resp
    resp=$(curl -sf -X POST "$api_url/caches" \
      -H "Content-Type: application/json" \
      -d "{\"key\":\"$key\",\"version\":\"v${i}\"}")
    local cid
    cid=$(echo "$resp" | grep -o '"cacheId":[0-9]*' | grep -o '[0-9]*')
    if [[ -n "$cid" ]]; then
      curl -sf -X PATCH "$api_url/caches/$cid" \
        -H "Content-Type: application/octet-stream" \
        -H "Content-Range: bytes 0-$((size - 1))/*" \
        --data-binary @<(head -c "$size" /dev/urandom) > /dev/null
      curl -sf -X POST "$api_url/caches/$cid" \
        -H "Content-Type: application/json" \
        -d "{\"size\":$size}" > /dev/null
    fi
  done
}

# Get docker stats for a container
get_container_stats() {
  # Inside the stress container we can't run docker stats, so we rely on
  # /proc if available or skip. The compose resource limits + hey output
  # give us what we need. Stats are collected externally.
  :
}

# Run hey and extract key metrics into a compact format
run_hey() {
  local label=$1; shift
  hey "$@" 2>&1 | awk '
    /^Summary:/ { in_summary=1 }
    /Requests\/sec:/ { rps=$2 }
    /Total:/ && in_summary { total=$2 }
    /Slowest:/ { slowest=$2 }
    /Fastest:/ { fastest=$2 }
    /Average:/ { average=$2 }
    /50%/ { p50=$3 }
    /90%/ { p90=$3 }
    /99%/ { p99=$3 }
    /\[200\]/ { s200=$2 }
    /\[204\]/ { s204=$2 }
    /\[500\]/ { s500=$2 }
    END {
      ok = s200 + s204 + 0
      printf "  %-8s rps=%-8s p50=%-8s p90=%-8s p99=%-8s ok=%-5s err=%-5s\n", \
        "'"$label"':", rps, p50, p90, p99, ok, s500+0
    }
  '
}

# Full lifecycle test: reserve -> upload -> commit -> lookup, N concurrent
lifecycle_test() {
  local api_url=$1
  local label=$2
  local concurrency=$3
  local chunk_file=$4
  local chunk_size=$5

  local start_time end_time successes=0 failures=0
  start_time=$(perl -MTime::HiRes=time -e 'printf "%.6f\n", time')

  local pids=()
  for i in $(seq 1 "$concurrency"); do
    (
      local k="lifecycle-${label}-${i}-$$-$(head -c 8 /dev/urandom | od -An -tx1 | tr -d ' \n')"
      local v="v${i}"

      local r
      r=$(curl -sf -X POST "$api_url/caches" \
        -H "Content-Type: application/json" \
        -d "{\"key\":\"$k\",\"version\":\"$v\"}" 2>/dev/null) || exit 1
      local id
      id=$(echo "$r" | grep -o '"cacheId":[0-9]*' | grep -o '[0-9]*')
      [[ -z "$id" ]] && exit 1

      curl -sf -X PATCH "$api_url/caches/$id" \
        -H "Content-Type: application/octet-stream" \
        -H "Content-Range: bytes 0-$((chunk_size - 1))/*" \
        --data-binary @"$chunk_file" > /dev/null 2>&1 || exit 1

      curl -sf -X POST "$api_url/caches/$id" \
        -H "Content-Type: application/json" \
        -d "{\"size\":$chunk_size}" > /dev/null 2>&1 || exit 1

      curl -sf "$api_url/cache?keys=$k&version=$v" > /dev/null 2>&1 || exit 1
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
  local elapsed ops_per_sec
  elapsed=$(perl -e "printf '%.3f', $end_time - $start_time")
  ops_per_sec=$(perl -e "printf '%.1f', $successes / ($end_time - $start_time)")

  printf "  %-8s %d/%d ok | %s failures | %ss | %s ops/sec\n" \
    "$label:" "$successes" "$concurrency" "$failures" "$elapsed" "$ops_per_sec"
}

print_separator() {
  echo ""
  echo "========================================================================"
  echo ""
}

# ==========================================================================

wait_for_servers

echo -e "\n${BOLD}================================================================${NC}"
echo -e "${BOLD}  CACHE SERVER STRESS TEST: GO vs NODE                          ${NC}"
echo -e "${BOLD}  Both servers limited to 2 CPU / 2 GB RAM                      ${NC}"
echo -e "${BOLD}================================================================${NC}"

# Seed both servers
echo -e "\n${CYAN}Seeding both servers with 200 cache entries...${NC}"
seed "$GO_API" 200 1024 &
seed "$NODE_API" 200 1024 &
wait

# Get a download URL from each
GO_LOOKUP=$(curl -sf "$GO_API/cache?keys=seed-1024-1-$$&version=v1")
GO_DL=$(echo "$GO_LOOKUP" | grep -o '"archiveLocation":"[^"]*"' | sed 's/"archiveLocation":"//;s/"//' | sed "s|http://localhost:3001|$GO_URL|")
NODE_LOOKUP=$(curl -sf "$NODE_API/cache?keys=seed-1024-1-$$&version=v1")
NODE_DL=$(echo "$NODE_LOOKUP" | grep -o '"archiveLocation":"[^"]*"' | sed 's/"archiveLocation":"//;s/"//' | sed "s|http://localhost:3002|$NODE_URL|")

# =========================================================================
# ROUND 1: Warmup - light load
# =========================================================================
print_separator
echo -e "${BOLD}${YELLOW}ROUND 1: Warmup (c=10, n=200)${NC}"
echo -e "${CYAN}--- Reserve ---${NC}"
run_hey "Go"   -n 200 -c 10 -m POST -H "Content-Type: application/json" \
  -d '{"key":"warmup","version":"v1"}' "$GO_API/caches"
run_hey "Node" -n 200 -c 10 -m POST -H "Content-Type: application/json" \
  -d '{"key":"warmup","version":"v1"}' "$NODE_API/caches"

echo -e "${CYAN}--- Lookup ---${NC}"
run_hey "Go"   -n 200 -c 10 "$GO_API/cache?keys=seed-1024-1-$$&version=v1"
run_hey "Node" -n 200 -c 10 "$NODE_API/cache?keys=seed-1024-1-$$&version=v1"

echo -e "${CYAN}--- Download ---${NC}"
if [[ -n "$GO_DL" ]]; then run_hey "Go" -n 200 -c 10 "$GO_DL"; fi
if [[ -n "$NODE_DL" ]]; then run_hey "Node" -n 200 -c 10 "$NODE_DL"; fi

# =========================================================================
# ROUND 2: Medium load
# =========================================================================
print_separator
echo -e "${BOLD}${YELLOW}ROUND 2: Medium load (c=50, n=1000)${NC}"

echo -e "${CYAN}--- Reserve ---${NC}"
run_hey "Go"   -n 1000 -c 50 -m POST -H "Content-Type: application/json" \
  -d '{"key":"medium","version":"v1"}' "$GO_API/caches"
run_hey "Node" -n 1000 -c 50 -m POST -H "Content-Type: application/json" \
  -d '{"key":"medium","version":"v1"}' "$NODE_API/caches"

echo -e "${CYAN}--- Lookup ---${NC}"
run_hey "Go"   -n 1000 -c 50 "$GO_API/cache?keys=seed-1024-1-$$&version=v1"
run_hey "Node" -n 1000 -c 50 "$NODE_API/cache?keys=seed-1024-1-$$&version=v1"

echo -e "${CYAN}--- Download ---${NC}"
if [[ -n "$GO_DL" ]]; then run_hey "Go" -n 1000 -c 50 "$GO_DL"; fi
if [[ -n "$NODE_DL" ]]; then run_hey "Node" -n 1000 -c 50 "$NODE_DL"; fi

echo -e "${CYAN}--- Upload 1MB ---${NC}"
for srv in Go Node; do
  api_url="$GO_API"; [[ "$srv" == "Node" ]] && api_url="$NODE_API"
  r=$(curl -sf -X POST "$api_url/caches" \
    -H "Content-Type: application/json" \
    -d "{\"key\":\"upload-med-${srv}-$$\",\"version\":\"v1\"}")
  cid=$(echo "$r" | grep -o '"cacheId":[0-9]*' | grep -o '[0-9]*')
  if [[ -n "$cid" ]]; then
    run_hey "$srv" -n 200 -c 25 -m PATCH \
      -H "Content-Type: application/octet-stream" \
      -H "Content-Range: bytes 0-1048575/*" \
      -D /tmp/chunk-1mb.bin "$api_url/caches/$cid"
  fi
done

echo -e "${CYAN}--- Full lifecycle (1MB) ---${NC}"
lifecycle_test "$GO_API" "Go" 50 /tmp/chunk-1mb.bin 1048576
lifecycle_test "$NODE_API" "Node" 50 /tmp/chunk-1mb.bin 1048576

# =========================================================================
# ROUND 3: Heavy load
# =========================================================================
print_separator
echo -e "${BOLD}${YELLOW}ROUND 3: Heavy load (c=100, n=2000)${NC}"

echo -e "${CYAN}--- Reserve ---${NC}"
run_hey "Go"   -n 2000 -c 100 -m POST -H "Content-Type: application/json" \
  -d '{"key":"heavy","version":"v1"}' "$GO_API/caches"
run_hey "Node" -n 2000 -c 100 -m POST -H "Content-Type: application/json" \
  -d '{"key":"heavy","version":"v1"}' "$NODE_API/caches"

echo -e "${CYAN}--- Lookup ---${NC}"
run_hey "Go"   -n 2000 -c 100 "$GO_API/cache?keys=seed-1024-1-$$&version=v1"
run_hey "Node" -n 2000 -c 100 "$NODE_API/cache?keys=seed-1024-1-$$&version=v1"

echo -e "${CYAN}--- Download ---${NC}"
if [[ -n "$GO_DL" ]]; then run_hey "Go" -n 2000 -c 100 "$GO_DL"; fi
if [[ -n "$NODE_DL" ]]; then run_hey "Node" -n 2000 -c 100 "$NODE_DL"; fi

echo -e "${CYAN}--- Upload 1MB ---${NC}"
for srv in Go Node; do
  api_url="$GO_API"; [[ "$srv" == "Node" ]] && api_url="$NODE_API"
  r=$(curl -sf -X POST "$api_url/caches" \
    -H "Content-Type: application/json" \
    -d "{\"key\":\"upload-heavy-${srv}-$$\",\"version\":\"v1\"}")
  cid=$(echo "$r" | grep -o '"cacheId":[0-9]*' | grep -o '[0-9]*')
  if [[ -n "$cid" ]]; then
    run_hey "$srv" -n 500 -c 50 -m PATCH \
      -H "Content-Type: application/octet-stream" \
      -H "Content-Range: bytes 0-1048575/*" \
      -D /tmp/chunk-1mb.bin "$api_url/caches/$cid"
  fi
done

echo -e "${CYAN}--- Full lifecycle (1MB) ---${NC}"
lifecycle_test "$GO_API" "Go" 100 /tmp/chunk-1mb.bin 1048576
lifecycle_test "$NODE_API" "Node" 100 /tmp/chunk-1mb.bin 1048576

# =========================================================================
# ROUND 4: Extreme load
# =========================================================================
print_separator
echo -e "${BOLD}${YELLOW}ROUND 4: Extreme load (c=200, n=5000)${NC}"

echo -e "${CYAN}--- Reserve ---${NC}"
run_hey "Go"   -n 5000 -c 200 -m POST -H "Content-Type: application/json" \
  -d '{"key":"extreme","version":"v1"}' "$GO_API/caches"
run_hey "Node" -n 5000 -c 200 -m POST -H "Content-Type: application/json" \
  -d '{"key":"extreme","version":"v1"}' "$NODE_API/caches"

echo -e "${CYAN}--- Lookup ---${NC}"
run_hey "Go"   -n 5000 -c 200 "$GO_API/cache?keys=seed-1024-1-$$&version=v1"
run_hey "Node" -n 5000 -c 200 "$NODE_API/cache?keys=seed-1024-1-$$&version=v1"

echo -e "${CYAN}--- Download ---${NC}"
if [[ -n "$GO_DL" ]]; then run_hey "Go" -n 5000 -c 200 "$GO_DL"; fi
if [[ -n "$NODE_DL" ]]; then run_hey "Node" -n 5000 -c 200 "$NODE_DL"; fi

echo -e "${CYAN}--- Full lifecycle (1MB) ---${NC}"
lifecycle_test "$GO_API" "Go" 200 /tmp/chunk-1mb.bin 1048576
lifecycle_test "$NODE_API" "Node" 200 /tmp/chunk-1mb.bin 1048576

# =========================================================================
# ROUND 5: Large payload stress
# =========================================================================
print_separator
echo -e "${BOLD}${YELLOW}ROUND 5: Large payloads (10MB and 50MB uploads)${NC}"

echo -e "${CYAN}--- Full lifecycle (10MB, c=50) ---${NC}"
lifecycle_test "$GO_API" "Go" 50 /tmp/chunk-10mb.bin $((10 * 1024 * 1024))
lifecycle_test "$NODE_API" "Node" 50 /tmp/chunk-10mb.bin $((10 * 1024 * 1024))

echo -e "${CYAN}--- Full lifecycle (50MB, c=20) ---${NC}"
lifecycle_test "$GO_API" "Go" 20 /tmp/chunk-50mb.bin $((50 * 1024 * 1024))
lifecycle_test "$NODE_API" "Node" 20 /tmp/chunk-50mb.bin $((50 * 1024 * 1024))

# =========================================================================
# ROUND 6: Sustained load (endurance)
# =========================================================================
print_separator
echo -e "${BOLD}${YELLOW}ROUND 6: Sustained load - 60 second continuous blast${NC}"

echo -e "${CYAN}--- Reserve (c=100, 60s) ---${NC}"
run_hey "Go"   -z 60s -c 100 -m POST -H "Content-Type: application/json" \
  -d '{"key":"sustained","version":"v1"}' "$GO_API/caches"
run_hey "Node" -z 60s -c 100 -m POST -H "Content-Type: application/json" \
  -d '{"key":"sustained","version":"v1"}' "$NODE_API/caches"

echo -e "${CYAN}--- Lookup (c=100, 60s) ---${NC}"
run_hey "Go"   -z 60s -c 100 "$GO_API/cache?keys=seed-1024-1-$$&version=v1"
run_hey "Node" -z 60s -c 100 "$NODE_API/cache?keys=seed-1024-1-$$&version=v1"

echo -e "${CYAN}--- Download (c=100, 60s) ---${NC}"
if [[ -n "$GO_DL" ]]; then run_hey "Go" -z 60s -c 100 "$GO_DL"; fi
if [[ -n "$NODE_DL" ]]; then run_hey "Node" -z 60s -c 100 "$NODE_DL"; fi

# =========================================================================
# ROUND 7: Mixed workload sustained
# =========================================================================
print_separator
echo -e "${BOLD}${YELLOW}ROUND 7: Mixed workload - 120s concurrent reserve+lookup+download${NC}"

for srv in Go Node; do
  api_url="$GO_API"; dl_url="$GO_DL"
  [[ "$srv" == "Node" ]] && api_url="$NODE_API" && dl_url="$NODE_DL"

  echo -e "${CYAN}--- $srv (all endpoints, c=50 each, 120s) ---${NC}"

  # Fire all three in parallel
  hey -z 120s -c 50 -m POST -H "Content-Type: application/json" \
    -d '{"key":"mixed","version":"v1"}' "$api_url/caches" > "/tmp/hey-${srv}-reserve.txt" 2>&1 &
  pid_r=$!

  hey -z 120s -c 50 \
    "$api_url/cache?keys=seed-1024-1-$$&version=v1" > "/tmp/hey-${srv}-lookup.txt" 2>&1 &
  pid_l=$!

  if [[ -n "$dl_url" ]]; then
    hey -z 120s -c 50 "$dl_url" > "/tmp/hey-${srv}-download.txt" 2>&1 &
    pid_d=$!
  fi

  wait "$pid_r" "$pid_l" ${pid_d:+"$pid_d"} 2>/dev/null || true

  for op in reserve lookup download; do
    f="/tmp/hey-${srv}-${op}.txt"
    [[ -f "$f" ]] || continue
    awk -v label="$op" '
      /Requests\/sec:/ { rps=$2 }
      /50%/ { p50=$3 }
      /90%/ { p90=$3 }
      /99%/ { p99=$3 }
      /\[200\]/ { s200=$2 }
      /\[204\]/ { s204=$2 }
      /\[500\]/ { s500=$2 }
      END {
        ok = s200 + s204 + 0
        printf "  %-10s rps=%-8s p50=%-8s p90=%-8s p99=%-8s ok=%-6s err=%-5s\n", \
          label":", rps, p50, p90, p99, ok, s500+0
      }
    ' "$f"
  done
done

# =========================================================================
print_separator
echo -e "${BOLD}${GREEN}STRESS TEST COMPLETE${NC}"
echo ""
echo "Run 'docker compose -f docker-compose.stress.yml logs go-server' or"
echo "'docker compose -f docker-compose.stress.yml logs node-server' to see server logs."
echo ""
echo "For resource usage, run in another terminal during the test:"
echo "  docker stats --no-stream --format 'table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}'"
