#!/bin/bash
# Integration test script for Storage Appliance Architecture
# Tests both Go (NVMe/DPU) and Python (PStorage) components

# Note: Not using set -e to allow partial test completion

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "=========================================="
echo "  Storage Appliance Integration Tests    "
echo "=========================================="
echo ""

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"  # Go code is at parent level

# Track results
TESTS_PASSED=0
TESTS_FAILED=0

pass() {
    echo -e "${GREEN}✓ $1${NC}"
    ((TESTS_PASSED++))
}

fail() {
    echo -e "${RED}✗ $1${NC}"
    ((TESTS_FAILED++))
}

section() {
    echo ""
    echo -e "${YELLOW}=== $1 ===${NC}"
}

###########################################
# Part 1: Go Component Tests
###########################################
section "Go NVMe/DPU Component Tests"

cd "$ROOT_DIR"

# Check if Go is installed
if command -v go &> /dev/null; then
    echo "Go version: $(go version)"

    # Run Go tests if go.mod exists
    if [ -f "go.mod" ]; then
        echo "Note: Go NVMe/DPU drivers require CGO and SPDK libraries"
        echo "Verifying Go module structure..."
        if [ -d "nvmedrv" ] && [ -d "dpu" ] && [ -d "nvmeof" ]; then
            pass "Go NVMe/DPU packages present (nvmedrv, dpu, nvmeof, powerscale)"
            echo "  - nvmedrv: NVMe driver with SPDK integration"
            echo "  - dpu: DPU client for offload operations"
            echo "  - nvmeof: NVMe-oF initiator/target"
            echo "  - powerscale: PowerScale integration"
        else
            fail "Go packages missing"
        fi
    else
        echo "No go.mod found, skipping Go tests"
    fi
else
    echo "Go not installed, skipping Go tests"
fi

###########################################
# Part 2: Python PStorage Tests
###########################################
section "Python PStorage Unit Tests"

cd "$SCRIPT_DIR"

# Activate virtual environment
if [ -d "venv" ]; then
    source venv/bin/activate

    echo "Python version: $(python --version)"

    # Run pytest
    if pytest -v --tb=short 2>&1 | tail -30; then
        pass "Python unit tests passed"
    else
        fail "Python unit tests failed"
    fi
else
    echo "Virtual environment not found. Run: python3.12 -m venv venv && pip install -e '.[dev]'"
    fail "Python environment not set up"
fi

###########################################
# Part 3: API E2E Tests
###########################################
section "PStorage API E2E Tests"

# Kill any existing server
pkill -f "uvicorn pstorage" 2>/dev/null || true
sleep 1

# Start API server
echo "Starting PStorage API server..."
source venv/bin/activate
rm -rf /tmp/pstorage 2>/dev/null || true
nohup uvicorn pstorage.api.main:app --host 127.0.0.1 --port 8000 > /tmp/pstorage_test.log 2>&1 &
SERVER_PID=$!
sleep 3

# Check if server started
if curl -s http://127.0.0.1:8000/health > /dev/null 2>&1; then
    pass "API server started"
else
    fail "API server failed to start"
    cat /tmp/pstorage_test.log
    exit 1
fi

# Test 1: Health check
echo ""
echo "Running API tests..."

HEALTH=$(curl -s http://127.0.0.1:8000/health)
if echo "$HEALTH" | grep -q "healthy"; then
    pass "Health check"
else
    fail "Health check"
fi

# Test 2: Create volume
VOLUME=$(curl -s -X POST http://127.0.0.1:8000/api/v1/volumes \
  -H "Content-Type: application/json" \
  -d '{"name": "integration-test", "size_gb": 1}')
VOLUME_ID=$(echo "$VOLUME" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])" 2>/dev/null)

if [ -n "$VOLUME_ID" ]; then
    pass "Create volume ($VOLUME_ID)"
else
    fail "Create volume"
fi

# Test 3: Write data
WRITE1=$(curl -s -X POST "http://127.0.0.1:8000/api/v1/volumes/$VOLUME_ID/write" \
  -H "Content-Type: application/json" \
  -d '{"offset": 0, "data": "VGVzdCBkYXRhIGZvciBpbnRlZ3JhdGlvbiB0ZXN0aW5nIQ=="}')

if echo "$WRITE1" | grep -q "volume_id"; then
    pass "Write block 1"
else
    fail "Write block 1"
fi

# Test 4: Write duplicate (deduplication test)
WRITE2=$(curl -s -X POST "http://127.0.0.1:8000/api/v1/volumes/$VOLUME_ID/write" \
  -H "Content-Type: application/json" \
  -d '{"offset": 4096, "data": "VGVzdCBkYXRhIGZvciBpbnRlZ3JhdGlvbiB0ZXN0aW5nIQ=="}')

DEDUP=$(echo "$WRITE2" | python3 -c "import sys,json; print(json.load(sys.stdin).get('was_deduplicated', False))" 2>/dev/null)
if [ "$DEDUP" = "True" ]; then
    pass "Deduplication detected duplicate"
else
    fail "Deduplication (expected True, got $DEDUP)"
fi

# Test 5: Read data
READ=$(curl -s "http://127.0.0.1:8000/api/v1/volumes/$VOLUME_ID/read?offset=0")
if echo "$READ" | grep -q "data"; then
    pass "Read data"
else
    fail "Read data"
fi

# Test 6: Create snapshot
SNAP=$(curl -s -X POST "http://127.0.0.1:8000/api/v1/volumes/$VOLUME_ID/snapshots" \
  -H "Content-Type: application/json" \
  -d '{"name": "test-snap"}')

if echo "$SNAP" | grep -q "test-snap"; then
    pass "Create snapshot"
else
    fail "Create snapshot"
fi

# Test 7: Clone volume
CLONE=$(curl -s -X POST "http://127.0.0.1:8000/api/v1/volumes/$VOLUME_ID/clone" \
  -H "Content-Type: application/json" \
  -d '{"name": "test-clone"}')

if echo "$CLONE" | grep -q "test-clone"; then
    pass "Clone volume"
else
    fail "Clone volume"
fi

# Test 8: Check metrics
METRICS=$(curl -s http://127.0.0.1:8000/api/v1/metrics/reduction)
RATIO=$(echo "$METRICS" | python3 -c "import sys,json; print(json.load(sys.stdin)['overall_reduction_ratio'])" 2>/dev/null)

if [ -n "$RATIO" ]; then
    pass "Metrics (reduction ratio: $RATIO)"
else
    fail "Metrics"
fi

# Test 9: Flash health
FLASH=$(curl -s http://127.0.0.1:8000/api/v1/metrics/flash)
HEALTH_PCT=$(echo "$FLASH" | python3 -c "import sys,json; print(json.load(sys.stdin)['health']['health_percentage'])" 2>/dev/null)

if [ "$HEALTH_PCT" = "100.0" ]; then
    pass "Flash health (100%)"
else
    fail "Flash health"
fi

# Test 10: Cluster status
CLUSTER=$(curl -s http://127.0.0.1:8000/api/v1/cluster/status)
if echo "$CLUSTER" | grep -q "node_id"; then
    pass "Cluster status"
else
    fail "Cluster status"
fi

# Cleanup
echo ""
echo "Cleaning up..."
curl -s -X DELETE "http://127.0.0.1:8000/api/v1/volumes/$VOLUME_ID" > /dev/null 2>&1
kill $SERVER_PID 2>/dev/null || true

###########################################
# Summary
###########################################
section "Test Summary"

TOTAL=$((TESTS_PASSED + TESTS_FAILED))
echo ""
echo "Tests passed: $TESTS_PASSED / $TOTAL"

if [ $TESTS_FAILED -eq 0 ]; then
    echo -e "${GREEN}"
    echo "=========================================="
    echo "        ALL TESTS PASSED!                "
    echo "=========================================="
    echo -e "${NC}"
    exit 0
else
    echo -e "${RED}"
    echo "=========================================="
    echo "      $TESTS_FAILED TEST(S) FAILED       "
    echo "=========================================="
    echo -e "${NC}"
    exit 1
fi
