#!/bin/bash
# SATFARM Evaluation: Performance Benchmark
# Measures solve time with varying worker counts
#
# Prerequisites:
#   - Server running (docker compose up)
#   - Worker image built: docker build -t satfarm-worker -f Dockerfile.worker .
#   - Database accessible via docker exec

set -e

HTTP_BASE="${HTTP_BASE:-http://localhost:8080}"
GRPC_ADDR="${GRPC_ADDR:-localhost:50051}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CNF_FILE="$SCRIPT_DIR/hard263.cnf"

# Configurable worker counts
WORKER_COUNTS="${WORKER_COUNTS:-1 2 4 8}"

echo "========================================"
echo " SATFARM Performance Benchmark"
echo "========================================"
echo ""
echo "CNF file: $CNF_FILE"
echo "Worker counts to test: $WORKER_COUNTS"
echo ""

# --- Helper functions ---
random_string() {
    cat /dev/urandom | tr -dc 'a-z0-9' | fold -w 8 | head -n 1
}

WORKER_PIDS=""

cleanup() {
    echo ""
    echo "[cleanup] Stopping all workers..."
    for i in $(seq 1 10); do
        docker stop satfarm-bench-worker-$i 2>/dev/null || true
    done
    for pid in $WORKER_PIDS; do
        wait $pid 2>/dev/null || true
    done
    WORKER_PIDS=""
    echo "[cleanup] Done"
}
trap cleanup EXIT

# Create results file
RESULTS_FILE="/tmp/satfarm_benchmark_$(date +%Y%m%d_%H%M%S).csv"
echo "workers,duration_seconds,job_id" > "$RESULTS_FILE"

# --- Create test user once ---
echo "[setup] Creating test user..."
USERNAME="benchmark_$(random_string)"
EMAIL="${USERNAME}@test.com"
PASSWORD="testpass123"

curl -s -X POST "$HTTP_BASE/signup" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "username=$USERNAME&email=$EMAIL&password=$PASSWORD&confirm_password=$PASSWORD" \
    -c /tmp/satfarm_cookies.txt -b /tmp/satfarm_cookies.txt > /dev/null

curl -s -X POST "$HTTP_BASE/login" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "username=$USERNAME&password=$PASSWORD" \
    -c /tmp/satfarm_cookies.txt -b /tmp/satfarm_cookies.txt > /dev/null

for i in $(seq 1 100); do
    curl -s -X POST "$HTTP_BASE/deposit" \
        -H "Content-Type: application/x-www-form-urlencoded" \
        -b /tmp/satfarm_cookies.txt > /dev/null
done

TOKEN_RESP=$(curl -s -X POST "$HTTP_BASE/tokens/new" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "label=benchmark-token" \
    -b /tmp/satfarm_cookies.txt)

# Extract secret from JSON response
TOKEN=$(echo "$TOKEN_RESP" | grep -oP '"secret"\s*:\s*"[a-fA-F0-9]+"' | cut -d'"' -f4)

if [ -z "$TOKEN" ]; then
    echo "ERROR: Could not extract token"
    echo "Response: $TOKEN_RESP"
    exit 1
fi


echo "    User: $USERNAME, Token: ${TOKEN:0:8}..."
echo ""

# --- Run benchmarks ---
for NUM_WORKERS in $WORKER_COUNTS; do
    echo "========================================"
    echo " Testing with $NUM_WORKERS worker(s)"
    echo "========================================"
    
    # Start workers
    echo "[1/4] Starting $NUM_WORKERS worker(s)..."
    WORKER_PIDS=""
    for i in $(seq 1 $NUM_WORKERS); do
        # Docker workers with host networking for localhost access
        docker run --rm --network host --name satfarm-bench-worker-$i \
            satfarm-worker \
            -addr "$GRPC_ADDR" \
            -http "$HTTP_BASE" \
            -token "$TOKEN" \
            -name "bench-worker-$i" \
            > /tmp/worker_bench_$i.log 2>&1 &
        WORKER_PIDS="$WORKER_PIDS $!"
    done
    
    # Wait for workers to connect
    sleep $((NUM_WORKERS * 2))
    
    # Submit job
    echo "[2/4] Submitting job..."
    JOB_RESP=$(curl -s -X POST "$HTTP_BASE/jobs/new" \
        -F "cnf=@$CNF_FILE" \
        -F "max_workers=$NUM_WORKERS" \
        -b /tmp/satfarm_cookies.txt -L)
    
    # Get latest job ID
    JOB_ID=$(docker exec satfarm-db psql -U app -d appdb -t -c \
        "SELECT id FROM jobs WHERE user_id = (SELECT id FROM users WHERE username='$USERNAME') ORDER BY id DESC LIMIT 1;" 2>/dev/null | tr -d ' ')
    
    echo "    Job ID: $JOB_ID"
    
    # Wait for completion
    echo "[3/4] Waiting for job completion..."
    MAX_WAIT=300  # 5 minutes max
    ELAPSED=0
    while [ $ELAPSED -lt $MAX_WAIT ]; do
        STATUS=$(docker exec satfarm-db psql -U app -d appdb -t -c \
            "SELECT status FROM jobs WHERE id = $JOB_ID;" 2>/dev/null | tr -d ' ')
        
        if [ "$STATUS" = "completed" ] || [ "$STATUS" = "failed" ]; then
            break
        fi
        
        sleep 5
        ELAPSED=$((ELAPSED + 5))
        echo "    ... waiting ($ELAPSED s, status=$STATUS)"
    done
    
    # Calculate duration from database
    echo "[4/4] Calculating duration..."
    DURATION=$(docker exec satfarm-db psql -U app -d appdb -t -c \
        "SELECT EXTRACT(EPOCH FROM (completed_at - started_at)) FROM jobs WHERE id = $JOB_ID;" 2>/dev/null | tr -d ' ')
    
    if [ -z "$DURATION" ] || [ "$DURATION" = "" ]; then
        DURATION="N/A"
        echo "    Duration: Could not calculate (job may not have completed)"
    else
        echo "    Duration: $DURATION seconds"
        echo "$NUM_WORKERS,$DURATION,$JOB_ID" >> "$RESULTS_FILE"
    fi
    
    # Stop workers
    echo "[cleanup] Stopping workers..."
    for i in $(seq 1 $NUM_WORKERS); do
        docker stop satfarm-bench-worker-$i 2>/dev/null || true
    done
    for pid in $WORKER_PIDS; do
        wait $pid 2>/dev/null || true
    done
    WORKER_PIDS=""
    
    # Brief pause between runs
    sleep 2
    echo ""
done

# Clear trap since we cleaned up manually
trap - EXIT

echo "========================================"
echo " Benchmark Complete!"
echo "========================================"
echo ""
echo "Results saved to: $RESULTS_FILE"
echo ""
cat "$RESULTS_FILE"
echo ""
echo "--- Summary ---"

# Generate simple ASCII bar chart
echo ""
echo "Speedup visualization (relative to 1 worker):"
BASELINE=$(grep "^1," "$RESULTS_FILE" | cut -d',' -f2)

if [ -n "$BASELINE" ]; then
    while IFS=',' read -r workers duration job_id; do
        if [ "$workers" = "workers" ]; then continue; fi
        if [ -z "$duration" ] || [ "$duration" = "N/A" ]; then continue; fi
        
        SPEEDUP=$(echo "scale=2; $BASELINE / $duration" | bc 2>/dev/null || echo "N/A")
        BAR_LEN=$(echo "$SPEEDUP * 10" | bc 2>/dev/null | cut -d'.' -f1 || echo "0")
        BAR=$(printf '%*s' "$BAR_LEN" '' | tr ' ' '#')
        
        printf "%2s workers: %6.1f sec  (%.2fx speedup) %s\n" "$workers" "$duration" "$SPEEDUP" "$BAR"
    done < "$RESULTS_FILE"
fi

echo ""
echo "To plot this data:"
echo "  - Import $RESULTS_FILE into Excel/Google Sheets"
echo "  - Create a line chart: X=workers, Y=duration_seconds"
