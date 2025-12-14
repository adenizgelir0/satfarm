#!/bin/bash
# SATFARM Evaluation: Worker Crash Test
# Demonstrates shard re-enqueueing when a worker crashes
#
# Prerequisites:
#   - Server running (docker compose up)
#   - Worker images built: docker build -t satfarm-worker -f Dockerfile.worker .
#   - Testworker image built: docker build -t satfarm-testworker -f Dockerfile.testworker .

set -e

HTTP_BASE="${HTTP_BASE:-http://localhost:8080}"
GRPC_ADDR="${GRPC_ADDR:-localhost:50051}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CNF_FILE="$SCRIPT_DIR/uuf250-01.cnf"

echo "========================================"
echo " SATFARM Worker Crash Test"
echo "========================================"
echo ""
echo "This test demonstrates fault tolerance:"
echo "1. Start 2 workers (1 normal + 1 that crashes)"
echo "2. Submit a job with 2 shards"
echo "3. The crash worker dies mid-solve"
echo "4. Server re-enqueues shard → normal worker picks it up"
echo ""

# --- Helper functions ---
random_string() {
    cat /dev/urandom | tr -dc 'a-z0-9' | fold -w 8 | head -n 1
}

cleanup() {
    echo ""
    echo "[cleanup] Stopping workers..."
    docker stop satfarm-normal-worker 2>/dev/null || true
    docker stop satfarm-crash-worker 2>/dev/null || true
    wait $WORKER1_PID 2>/dev/null || true
    wait $WORKER2_PID 2>/dev/null || true
    echo "[cleanup] Done"
}
trap cleanup EXIT

# --- Step 1: Create test user ---
echo "[1/6] Creating test user..."
USERNAME="crashtest_$(random_string)"
EMAIL="${USERNAME}@test.com"
PASSWORD="testpass123"

REGISTER_RESP=$(curl -s -X POST "$HTTP_BASE/signup" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "username=$USERNAME&email=$EMAIL&password=$PASSWORD&confirm_password=$PASSWORD" \
    -c /tmp/satfarm_cookies.txt -b /tmp/satfarm_cookies.txt \
    -w "\n%{http_code}" -L)

HTTP_CODE=$(echo "$REGISTER_RESP" | tail -n1)
if [ "$HTTP_CODE" != "200" ]; then
    echo "Registration may have redirected (code=$HTTP_CODE), trying login..."
fi

# Login to get session
curl -s -X POST "$HTTP_BASE/login" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "username=$USERNAME&password=$PASSWORD" \
    -c /tmp/satfarm_cookies.txt -b /tmp/satfarm_cookies.txt > /dev/null

echo "    Created user: $USERNAME"

# --- Step 2: Add balance ---
echo "[2/6] Adding SATC balance (100 deposits of 1000 each)..."
for i in $(seq 1 100); do
    curl -s -X POST "$HTTP_BASE/deposit" \
        -H "Content-Type: application/x-www-form-urlencoded" \
        -b /tmp/satfarm_cookies.txt > /dev/null
done
echo "    Added 100,000 SATC"

# --- Step 3: Create API token ---
echo "[3/6] Creating API token..."
TOKEN_RESP=$(curl -s -X POST "$HTTP_BASE/tokens/new" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "label=crash-test-token" \
    -b /tmp/satfarm_cookies.txt)

# Extract secret from JSON response {"ok":true,"secret":"...","token":{...}}
TOKEN=$(echo "$TOKEN_RESP" | grep -oP '"secret"\s*:\s*"[a-fA-F0-9]+"' | cut -d'"' -f4)

if [ -z "$TOKEN" ]; then
    echo "ERROR: Could not extract token from response"
    echo "Response: $TOKEN_RESP"
    exit 1
fi


echo "    Token: ${TOKEN:0:8}..."

# --- Step 4: Start workers ---
echo "[4/6] Starting workers..."

# Normal worker (Docker with host networking for localhost access)
docker run --rm --network host --name satfarm-normal-worker \
    satfarm-worker \
    -addr "$GRPC_ADDR" \
    -http "$HTTP_BASE" \
    -token "$TOKEN" \
    -name "normal-worker" \
    > /tmp/worker_normal.log 2>&1 &
WORKER1_PID=$!

# Crash worker (will crash on first shard)
docker run --rm --network host --name satfarm-crash-worker \
    satfarm-testworker \
    -addr "$GRPC_ADDR" \
    -http "$HTTP_BASE" \
    -token "$TOKEN" \
    -name "crash-worker" \
    -fail crash_mid \
    > /tmp/worker_crash.log 2>&1 &
WORKER2_PID=$!

echo "    Normal worker PID: $WORKER1_PID"
echo "    Crash worker PID: $WORKER2_PID"

# Give workers time to connect
sleep 3

# --- Step 5: Submit job ---
echo "[5/6] Submitting job (2 shards)..."
JOB_RESP=$(curl -s -X POST "$HTTP_BASE/jobs/new" \
    -F "cnf=@$CNF_FILE" \
    -F "max_workers=2" \
    -b /tmp/satfarm_cookies.txt -L)

echo "    Job submitted"

# --- Step 6: Wait and check results ---
echo "[6/6] Waiting for job completion..."
echo ""

# Wait for job to complete (crash worker will die, normal worker will finish)
sleep 60

echo "========================================"
echo " Results"
echo "========================================"
echo ""

# Show crash worker log
echo "--- Crash Worker Log (last 10 lines) ---"
tail -10 /tmp/worker_crash.log 2>/dev/null || echo "(no log)"
echo ""

# Show normal worker log
echo "--- Normal Worker Log (last 15 lines) ---"
tail -15 /tmp/worker_normal.log 2>/dev/null || echo "(no log)"
echo ""

# Query database for attempt_count
echo "--- Database: Shard Attempts ---"
echo "If attempt_count > 1, the shard was re-assigned after crash"
docker exec satfarm-db psql -U app -d appdb -c \
    "SELECT s.id as shard_id, s.status, s.attempt_count, s.worker_name 
     FROM job_shards s 
     JOIN jobs j ON s.job_id = j.id 
     WHERE j.user_id = (SELECT id FROM users WHERE username='$USERNAME')
     ORDER BY s.id;" 2>/dev/null || echo "(could not query database)"

echo ""
echo "========================================"
echo " Test Complete!"
echo "========================================"
echo ""
echo "What to look for:"
echo "  ✓ Crash worker log shows 'CRASH: Disconnecting mid-solve'"
echo "  ✓ Normal worker log shows it solved 2 shards (including the re-assigned one)"
echo "  ✓ Database shows attempt_count >= 2 for one shard"
