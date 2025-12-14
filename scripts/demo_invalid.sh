#!/bin/bash
# SATFARM Evaluation: Invalid Result Rejection Test
# Demonstrates that invalid SAT results are rejected and workers penalized
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
echo " SATFARM Invalid Result Rejection Test"
echo "========================================"
echo ""
echo "This test demonstrates result verification:"
echo "1. Start 2 workers (1 normal + 1 malicious)"
echo "2. Submit a SAT job"
echo "3. Malicious worker sends garbage assignment"
echo "4. Server rejects, penalizes, re-enqueues"
echo "5. Normal worker solves correctly"
echo ""

# --- Helper functions ---
random_string() {
    cat /dev/urandom | tr -dc 'a-z0-9' | fold -w 8 | head -n 1
}

cleanup() {
    echo ""
    echo "[cleanup] Stopping workers..."
    docker stop satfarm-normal-worker 2>/dev/null || true
    docker stop satfarm-malicious-worker 2>/dev/null || true
    wait $WORKER1_PID 2>/dev/null || true
    wait $WORKER2_PID 2>/dev/null || true
    echo "[cleanup] Done"
}
trap cleanup EXIT

# --- Step 1: Create test user ---
echo "[1/7] Creating test user..."
USERNAME="invalidtest_$(random_string)"
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

echo "    Created user: $USERNAME"

# --- Step 2: Add balance and record initial ---
echo "[2/7] Adding SATC balance (100 deposits of 1000 each)..."
for i in $(seq 1 100); do
    curl -s -X POST "$HTTP_BASE/deposit" \
        -H "Content-Type: application/x-www-form-urlencoded" \
        -b /tmp/satfarm_cookies.txt > /dev/null
done

INITIAL_BALANCE=$(docker exec satfarm-db psql -U app -d appdb -t -c \
    "SELECT balance_satc FROM users WHERE username='$USERNAME';" 2>/dev/null | tr -d ' ')
echo "    Initial balance: $INITIAL_BALANCE SATC"

# --- Step 3: Create API token ---
echo "[3/7] Creating API token..."
TOKEN_RESP=$(curl -s -X POST "$HTTP_BASE/tokens/new" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "label=invalid-test-token" \
    -b /tmp/satfarm_cookies.txt)

# Extract secret from JSON response
TOKEN=$(echo "$TOKEN_RESP" | grep -oP '"secret"\s*:\s*"[a-fA-F0-9]+"' | cut -d'"' -f4)

if [ -z "$TOKEN" ]; then
    echo "ERROR: Could not extract token"
    echo "Response: $TOKEN_RESP"
    exit 1
fi


echo "    Token: ${TOKEN:0:8}..."

# --- Step 4: Start malicious worker FIRST ---
echo "[4/7] Starting malicious worker (will send wrong SAT)..."

# Malicious worker (Docker with host networking for localhost access)
docker run --rm --network host --name satfarm-malicious-worker \
    satfarm-testworker \
    -addr "$GRPC_ADDR" \
    -http "$HTTP_BASE" \
    -token "$TOKEN" \
    -name "malicious-worker" \
    -fail wrong_sat \
    > /tmp/worker_malicious.log 2>&1 &
WORKER2_PID=$!

echo "    Malicious worker PID: $WORKER2_PID"
sleep 2

# --- Step 5: Start normal worker ---
echo "[5/7] Starting normal worker..."

# Normal worker (Docker with host networking for localhost access)
docker run --rm --network host --name satfarm-normal-worker \
    satfarm-worker \
    -addr "$GRPC_ADDR" \
    -http "$HTTP_BASE" \
    -token "$TOKEN" \
    -name "normal-worker" \
    > /tmp/worker_normal.log 2>&1 &
WORKER1_PID=$!

echo "    Normal worker PID: $WORKER1_PID"
sleep 2

# --- Step 6: Submit job ---
echo "[6/7] Submitting SAT job..."
curl -s -X POST "$HTTP_BASE/jobs/new" \
    -F "cnf=@$CNF_FILE" \
    -F "max_workers=1" \
    -b /tmp/satfarm_cookies.txt -L > /dev/null

echo "    Job submitted"

# --- Step 7: Wait and check results ---
echo "[7/7] Waiting for processing..."
sleep 10

echo ""
echo "========================================"
echo " Results"
echo "========================================"
echo ""

# Show malicious worker log
echo "--- Malicious Worker Log ---"
cat /tmp/worker_malicious.log 2>/dev/null | tail -10 || echo "(no log)"
echo ""

# Show normal worker log
echo "--- Normal Worker Log (last 10 lines) ---"
tail -10 /tmp/worker_normal.log 2>/dev/null || echo "(no log)"
echo ""

# Check balance change
FINAL_BALANCE=$(docker exec satfarm-db psql -U app -d appdb -t -c \
    "SELECT balance_satc FROM users WHERE username='$USERNAME';" 2>/dev/null | tr -d ' ')
echo "--- Balance Check ---"
echo "Initial balance: $INITIAL_BALANCE SATC"
echo "Final balance:   $FINAL_BALANCE SATC"

DIFF=$((INITIAL_BALANCE - FINAL_BALANCE))
if [ "$DIFF" -gt 0 ]; then
    echo "Penalty applied: $DIFF SATC deducted"
else
    echo "No penalty visible (reward may have offset it)"
fi
echo ""

# Query transactions
echo "--- Recent Transactions ---"
docker exec satfarm-db psql -U app -d appdb -c \
    "SELECT reason, amount_satc, created_at 
     FROM transactions 
     WHERE user_id = (SELECT id FROM users WHERE username='$USERNAME')
     ORDER BY created_at DESC
     LIMIT 15;" 2>/dev/null || echo "(could not query database)"

echo ""

# Check shard attempts
echo "--- Shard Attempts ---"
docker exec satfarm-db psql -U app -d appdb -c \
    "SELECT s.id, s.status, s.attempt_count
     FROM job_shards s 
     JOIN jobs j ON s.job_id = j.id 
     WHERE j.user_id = (SELECT id FROM users WHERE username='$USERNAME');" 2>/dev/null || echo "(could not query database)"

echo ""
echo "========================================"
echo " Test Complete!"
echo "========================================"
echo ""
echo "What to look for:"
echo "  ✓ Malicious worker log: 'WRONG_SAT: Sending invalid SAT assignment'"
echo "  ✓ Transactions table shows 'worker_penalty'"
echo "  ✓ Shard attempt_count >= 2 (was re-assigned after rejection)"
echo "  ✓ Job still completed successfully"
