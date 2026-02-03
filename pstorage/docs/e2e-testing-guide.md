# End-to-End Testing Guide

This guide covers testing both the Go NVMe/DPU drivers and Python PStorage management layer.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                      Test Client                             │
│              (curl, Go tests, Python tests)                  │
└─────────────────────────────────────────────────────────────┘
                              │
          ┌───────────────────┼───────────────────┐
          ▼                                       ▼
┌─────────────────────┐               ┌─────────────────────┐
│   PStorage (Python) │               │   Go NVMe/DPU       │
│   REST API :8000    │               │   Drivers           │
│   ─────────────────  │               │   ─────────────────  │
│   • Volumes         │               │   • nvmedrv         │
│   • Snapshots       │               │   • nvmeof          │
│   • Data reduction  │◄─────────────►│   • dpu client      │
│   • Metrics         │  Integration  │   • powerscale      │
└─────────────────────┘               └─────────────────────┘
          │                                       │
          ▼                                       ▼
┌─────────────────────────────────────────────────────────────┐
│                   Storage Backend                            │
│         (File system / NVMe devices / DPU)                  │
└─────────────────────────────────────────────────────────────┘
```

## Prerequisites

### Go Components
```bash
# Install Go 1.21+
brew install go

# Verify
go version
```

### Python Components
```bash
# Install Python 3.10+
brew install python@3.12

# Create virtual environment
cd pstorage
python3.12 -m venv venv
source venv/bin/activate
pip install -e ".[dev]"
```

## Part 1: Test Go Components

### 1.1 Run Go Unit Tests

```bash
cd /Users/srilakshmi

# Test all Go packages
go test ./dpu/... ./nvmedrv/... ./nvmeof/... ./powerscale/... -v

# Test with coverage
go test ./... -cover

# Test specific package
go test ./nvmedrv/... -v
```

### 1.2 Test NVMe Driver

```go
// Example test: nvmedrv/driver_test.go
package nvmedrv

import (
    "testing"
)

func TestNVMeDriver(t *testing.T) {
    // Create emulated device (1GB, 4KB blocks)
    dev, err := NewDevice(1<<30, 4096)
    if err != nil {
        t.Fatalf("Failed to create device: %v", err)
    }
    defer dev.Close()

    // Write data
    data := make([]byte, 4096)
    copy(data, []byte("Hello NVMe!"))

    if err := dev.Write(0, 1, data); err != nil {
        t.Fatalf("Write failed: %v", err)
    }

    // Read back
    buf := make([]byte, 4096)
    if err := dev.Read(0, 1, buf); err != nil {
        t.Fatalf("Read failed: %v", err)
    }

    if string(buf[:11]) != "Hello NVMe!" {
        t.Errorf("Data mismatch")
    }
}
```

### 1.3 Test DPU Client

```bash
# Run DPU tests
go test ./dpu/... -v

# Example output:
# === RUN   TestDPUConnect
# --- PASS: TestDPUConnect (0.00s)
# === RUN   TestNVMeoFTargetCreate
# --- PASS: TestNVMeoFTargetCreate (0.01s)
```

## Part 2: Test Python PStorage

### 2.1 Run Unit Tests

```bash
cd /Users/srilakshmi/pstorage
source venv/bin/activate

# Run all tests
pytest -v

# Run with coverage
pytest --cov=pstorage -v

# Run specific module
pytest tests/test_purity/ -v
pytest tests/test_api/ -v
```

### 2.2 Start API Server

```bash
# Terminal 1: Start server
source venv/bin/activate
uvicorn pstorage.api.main:app --port 8000

# Or use the CLI
pstorage
```

### 2.3 API E2E Tests

```bash
# Terminal 2: Run tests

# Health check
curl http://localhost:8000/health

# Create volume
curl -X POST http://localhost:8000/api/v1/volumes \
  -H "Content-Type: application/json" \
  -d '{"name": "test-vol", "size_gb": 1}'

# Write data
VOLUME_ID="<id-from-above>"
curl -X POST "http://localhost:8000/api/v1/volumes/$VOLUME_ID/write" \
  -H "Content-Type: application/json" \
  -d '{"offset": 0, "data": "SGVsbG8gV29ybGQh"}'

# Read data
curl "http://localhost:8000/api/v1/volumes/$VOLUME_ID/read?offset=0"

# Check deduplication
curl http://localhost:8000/api/v1/metrics/reduction
```

## Part 3: Integration Testing

### 3.1 Integration Architecture

```
┌──────────────────┐     HTTP      ┌──────────────────┐
│  Test Client     │──────────────►│  PStorage API    │
│  (Python/curl)   │               │  (Port 8000)     │
└──────────────────┘               └────────┬─────────┘
                                            │
                                            ▼
                                   ┌──────────────────┐
                                   │  Storage Backend │
                                   │  (configurable)  │
                                   └────────┬─────────┘
                                            │
                    ┌───────────────────────┼───────────────────────┐
                    ▼                       ▼                       ▼
           ┌──────────────┐        ┌──────────────┐        ┌──────────────┐
           │ File Backend │        │ NVMe Backend │        │ DPU Backend  │
           │ (default)    │        │ (Go driver)  │        │ (Go client)  │
           └──────────────┘        └──────────────┘        └──────────────┘
```

### 3.2 Create Integration Test Script

```bash
#!/bin/bash
# test_integration.sh

set -e
echo "=== Storage Appliance Integration Tests ==="

# 1. Start PStorage API
echo "Starting PStorage API..."
cd /Users/srilakshmi/pstorage
source venv/bin/activate
uvicorn pstorage.api.main:app --port 8000 &
PSTORAGE_PID=$!
sleep 3

# 2. Run Go tests
echo "Running Go NVMe driver tests..."
cd /Users/srilakshmi
go test ./nvmedrv/... -v

echo "Running Go DPU tests..."
go test ./dpu/... -v

# 3. Test PStorage API
echo "Testing PStorage API..."

# Health check
curl -s http://localhost:8000/health | grep -q "healthy"
echo "✓ Health check passed"

# Create volume
VOLUME=$(curl -s -X POST http://localhost:8000/api/v1/volumes \
  -H "Content-Type: application/json" \
  -d '{"name": "integration-test", "size_gb": 1}')
VOLUME_ID=$(echo $VOLUME | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])")
echo "✓ Volume created: $VOLUME_ID"

# Write data
curl -s -X POST "http://localhost:8000/api/v1/volumes/$VOLUME_ID/write" \
  -H "Content-Type: application/json" \
  -d '{"offset": 0, "data": "SGVsbG8gSW50ZWdyYXRpb24h"}' > /dev/null
echo "✓ Data written"

# Write duplicate (test dedup)
WRITE2=$(curl -s -X POST "http://localhost:8000/api/v1/volumes/$VOLUME_ID/write" \
  -H "Content-Type: application/json" \
  -d '{"offset": 4096, "data": "SGVsbG8gSW50ZWdyYXRpb24h"}')
DEDUP=$(echo $WRITE2 | python3 -c "import sys,json; print(json.load(sys.stdin)['was_deduplicated'])")
echo "✓ Deduplication: $DEDUP"

# Read back
READ=$(curl -s "http://localhost:8000/api/v1/volumes/$VOLUME_ID/read?offset=0")
echo "✓ Data read back"

# Create snapshot
curl -s -X POST "http://localhost:8000/api/v1/volumes/$VOLUME_ID/snapshots" \
  -H "Content-Type: application/json" \
  -d '{"name": "snap-1"}' > /dev/null
echo "✓ Snapshot created"

# Check metrics
METRICS=$(curl -s http://localhost:8000/api/v1/metrics/reduction)
RATIO=$(echo $METRICS | python3 -c "import sys,json; print(json.load(sys.stdin)['overall_reduction_ratio'])")
echo "✓ Reduction ratio: $RATIO"

# Cleanup
curl -s -X DELETE "http://localhost:8000/api/v1/volumes/$VOLUME_ID" > /dev/null
echo "✓ Cleanup complete"

# Stop PStorage
kill $PSTORAGE_PID 2>/dev/null

echo ""
echo "=== All Integration Tests Passed! ==="
```

### 3.3 Run Integration Tests

```bash
chmod +x test_integration.sh
./test_integration.sh
```

## Part 4: Full Stack Test

### 4.1 Docker Compose Setup

```yaml
# docker-compose.test.yml
version: '3.8'

services:
  pstorage:
    build: ./pstorage
    ports:
      - "8000:8000"
    environment:
      - PSTORAGE_DEBUG=true
      - PSTORAGE_DATA_DIR=/data
    volumes:
      - pstorage-data:/data
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8000/health"]
      interval: 5s
      timeout: 3s
      retries: 3

  go-tests:
    build:
      context: .
      dockerfile: Dockerfile.go-test
    depends_on:
      pstorage:
        condition: service_healthy
    command: go test ./... -v

volumes:
  pstorage-data:
```

### 4.2 Run Full Stack

```bash
# Build and run all tests
docker-compose -f docker-compose.test.yml up --build --abort-on-container-exit

# View logs
docker-compose -f docker-compose.test.yml logs -f
```

## Test Matrix

| Component | Unit Test | Integration | E2E |
|-----------|-----------|-------------|-----|
| nvmedrv (Go) | `go test ./nvmedrv/...` | With PStorage | Docker |
| dpu (Go) | `go test ./dpu/...` | With PStorage | Docker |
| nvmeof (Go) | `go test ./nvmeof/...` | With PStorage | Docker |
| PStorage API | `pytest tests/` | With Go drivers | Docker |
| Deduplication | `pytest tests/test_purity/` | API test | Docker |
| Compression | `pytest tests/test_purity/` | API test | Docker |
| Volumes | `pytest tests/test_api/` | Full flow | Docker |

## Quick Test Commands

```bash
# Test everything
make test-all

# Or manually:

# 1. Go tests
go test ./... -v

# 2. Python tests
cd pstorage && pytest -v

# 3. Integration
./test_integration.sh

# 4. Full stack
docker-compose -f docker-compose.test.yml up --build
```

## Troubleshooting

### Go Tests Fail
```bash
# Check Go version
go version  # Need 1.21+

# Update dependencies
go mod tidy
```

### Python Tests Fail
```bash
# Check Python version
python --version  # Need 3.10+

# Reinstall dependencies
pip install -e ".[dev]" --force-reinstall
```

### Integration Fails
```bash
# Check if port 8000 is free
lsof -i :8000

# Kill existing processes
pkill -f uvicorn

# Check logs
tail -f /tmp/pstorage.log
```
