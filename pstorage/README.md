# PStorage

A Pure Storage-inspired storage management system implementing enterprise-grade data services in Python.

## Features

- **Data Reduction** - Always-on inline deduplication, compression, and pattern removal
- **High Availability** - ActiveCluster with synchronous replication and automatic failover
- **Flash Management** - DirectFlash simulation with wear leveling and garbage collection
- **RESTful API** - FastAPI-based management interface

## Quick Start

### Installation

```bash
# Clone the repository
git clone <repository-url>
cd pstorage

# Install dependencies
pip install -e ".[dev]"
```

### Running the API Server

```bash
# Start the server
pstorage

# Or with uvicorn directly
uvicorn pstorage.api.main:app --reload
```

The API will be available at `http://localhost:8000`.

### API Documentation

Once running, visit:
- Swagger UI: `http://localhost:8000/docs`
- ReDoc: `http://localhost:8000/redoc`

## Configuration

Configuration via environment variables (prefix `PSTORAGE_`):

| Variable | Default | Description |
|----------|---------|-------------|
| `PSTORAGE_DEBUG` | `false` | Enable debug mode |
| `PSTORAGE_DATA_DIR` | `/tmp/pstorage/data` | Data storage directory |
| `PSTORAGE_API_PORT` | `8000` | API server port |
| `PSTORAGE_DEDUPE_ENABLED` | `true` | Enable deduplication |
| `PSTORAGE_COMPRESSION_ENABLED` | `true` | Enable compression |
| `PSTORAGE_COMPRESSION_ALGORITHM` | `lz4` | Compression algorithm (lz4/zstd) |
| `PSTORAGE_CLUSTER_ENABLED` | `false` | Enable clustering |
| `PSTORAGE_REPLICATION_MODE` | `sync` | Replication mode (sync/async) |

See [config.py](src/pstorage/core/config.py) for all options.

## Usage Examples

### Create a Volume

```bash
curl -X POST http://localhost:8000/api/v1/volumes \
  -H "Content-Type: application/json" \
  -d '{"name": "my-volume", "size_gb": 10}'
```

### Write Data

```bash
# Data must be base64 encoded
curl -X POST http://localhost:8000/api/v1/volumes/{volume_id}/write \
  -H "Content-Type: application/json" \
  -d '{"offset": 0, "data": "SGVsbG8gV29ybGQh"}'
```

### Create a Snapshot

```bash
curl -X POST http://localhost:8000/api/v1/volumes/{volume_id}/snapshots \
  -H "Content-Type: application/json" \
  -d '{"name": "snap-1"}'
```

### Check Data Reduction

```bash
curl http://localhost:8000/api/v1/metrics/reduction
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                         REST API                             │
│                    (FastAPI + Uvicorn)                       │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                    Storage Controller                        │
│    ┌─────────────┐  ┌─────────────┐  ┌─────────────┐       │
│    │   Purity    │  │   Volume    │  │  Snapshot   │       │
│    │   Engine    │  │   Manager   │  │   Manager   │       │
│    └─────────────┘  └─────────────┘  └─────────────┘       │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                   DirectFlash Layer                          │
│    ┌─────────────┐  ┌─────────────┐  ┌─────────────┐       │
│    │    Block    │  │    Wear     │  │   Garbage   │       │
│    │   Manager   │  │  Leveling   │  │  Collection │       │
│    └─────────────┘  └─────────────┘  └─────────────┘       │
└─────────────────────────────────────────────────────────────┘
```

## Project Structure

```
pstorage/
├── src/pstorage/
│   ├── api/              # REST API (FastAPI)
│   │   ├── routes/       # API endpoints
│   │   └── schemas/      # Pydantic models
│   ├── core/             # Core storage logic
│   │   ├── controller.py # Main orchestrator
│   │   ├── volume_manager.py
│   │   └── snapshot_manager.py
│   ├── directflash/      # Flash management
│   │   ├── simulator.py  # DirectFlash simulator
│   │   ├── block_manager.py
│   │   ├── wear_leveling.py
│   │   └── garbage_collection.py
│   ├── purity/           # Data reduction
│   │   ├── engine.py     # Reduction pipeline
│   │   ├── deduplication.py
│   │   ├── compression.py
│   │   └── pattern_removal.py
│   ├── activecluster/    # High availability
│   │   ├── cluster.py    # Cluster manager
│   │   ├── replication.py
│   │   ├── mediator.py
│   │   └── failover.py
│   └── storage/          # Storage backends
├── tests/                # Test suite
├── docs/                 # Documentation
└── docker/               # Docker configuration
```

## Documentation

- [NVMe Architecture](docs/nvme-architecture.md) - End-to-end NVMe, DirectFlash, and cluster coordination
- [API Reference](docs/api-reference.md) - Complete API documentation

## Running Tests

```bash
# Run all tests
pytest

# Run with coverage
pytest --cov=pstorage

# Run specific test module
pytest tests/test_purity/
```

## Docker

```bash
# Build image
docker build -t pstorage .

# Run container
docker run -p 8000:8000 pstorage
```

Or using docker-compose:

```bash
docker-compose up
```

## Key Concepts

### Data Reduction Pipeline

All writes pass through the Purity engine:

1. **Pattern Removal** - Detect zero blocks and repeated patterns
2. **Deduplication** - Hash-based block deduplication (SHA-256)
3. **Compression** - LZ4 (fast) or Zstd (high ratio)

### DirectFlash

Simulates direct flash management:

- **L2P Mapping** - Logical to physical address translation
- **Wear Leveling** - Distribute writes evenly across flash
- **Garbage Collection** - Reclaim space from invalid blocks

### ActiveCluster

High availability features:

- **Synchronous Replication** - Zero data loss (RPO=0)
- **Automatic Failover** - Minimal downtime (RTO ~5s)
- **Quorum-based** - Split-brain prevention

## License

MIT License - see LICENSE file for details.
