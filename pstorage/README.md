# PStorage

A Pure Storage-inspired storage management system implementing enterprise-grade data services with Python and Go components.

## Features

- **Data Reduction** - Always-on inline deduplication, compression (LZ4/Zstd), and pattern removal
- **High Availability** - ActiveCluster with synchronous/async replication and automatic failover
- **Flash Management** - DirectFlash simulation with wear leveling and garbage collection
- **Predictive Analytics** - Capacity forecasting, data tiering, and health monitoring
- **NVMe/DPU Offload** - Go-based NVMe-oF target and DPU acceleration (experimental)
- **RESTful API** - FastAPI-based management interface

## Quick Start

### Installation

```bash
# Clone the repository
git clone <repository-url>
cd pstorage

# Create virtual environment
python3 -m venv venv
source venv/bin/activate

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

## API Endpoints

### Core Storage

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/volumes` | GET/POST | List/create volumes |
| `/api/v1/volumes/{id}` | GET/DELETE | Get/delete volume |
| `/api/v1/volumes/{id}/write` | POST | Write data to volume |
| `/api/v1/volumes/{id}/read` | POST | Read data from volume |
| `/api/v1/volumes/{id}/snapshots` | GET/POST | List/create snapshots |

### Metrics & Monitoring

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/metrics` | GET | System metrics |
| `/api/v1/metrics/reduction` | GET | Data reduction stats |
| `/api/v1/metrics/flash` | GET | Flash health metrics |
| `/api/v1/metrics/replication` | GET | Replication stats |

### Predictive Analytics

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/analytics` | GET | Analytics overview |
| `/api/v1/analytics/capacity/forecast` | GET | Capacity predictions (7/30/90 days) |
| `/api/v1/analytics/health` | GET | Drive health assessment |
| `/api/v1/analytics/tiering/recommendations` | GET | Data tiering suggestions |
| `/api/v1/analytics/temperature/{volume_id}` | GET | Volume temperature (hot/warm/cold/frozen) |
| `/api/v1/analytics/volume/{volume_id}` | GET | Volume analytics |

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

### Get Capacity Forecast

```bash
curl http://localhost:8000/api/v1/analytics/capacity/forecast
```

Response:
```json
{
  "current": {"used_bytes": 1073741824, "utilization_percent": 25.0},
  "predictions": {
    "7_days": {"used_bytes": 1610612736, "utilization_percent": 37.5},
    "30_days": {"used_bytes": 2684354560, "utilization_percent": 62.5}
  },
  "thresholds": {
    "days_until_80_percent": 45,
    "days_until_90_percent": 60
  },
  "confidence": 0.85
}
```

### Check Drive Health

```bash
curl http://localhost:8000/api/v1/analytics/health
```

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              REST API Layer                                  │
│                         (FastAPI + Uvicorn + Pydantic)                       │
├─────────────────────────────────────────────────────────────────────────────┤
│                           Storage Controller                                 │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐ │
│  │   Purity    │  │   Volume    │  │  Snapshot   │  │     Predictive      │ │
│  │   Engine    │  │   Manager   │  │   Manager   │  │     Analytics       │ │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────────────┘ │
├─────────────────────────────────────────────────────────────────────────────┤
│                          DirectFlash Layer                                   │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐ │
│  │    Block    │  │    Wear     │  │   Garbage   │  │    L2P Mapping      │ │
│  │   Manager   │  │  Leveling   │  │  Collection │  │                     │ │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────────────┘ │
├─────────────────────────────────────────────────────────────────────────────┤
│                         ActiveCluster (HA)                                   │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐ │
│  │ Replication │  │   Failover  │  │   Mediator  │  │    Quorum Mgmt      │ │
│  │  (sync/async)│ │  (auto)     │  │  (witness)  │  │                     │ │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────────────┘ │
├─────────────────────────────────────────────────────────────────────────────┤
│                        Go NVMe/DPU Layer (Experimental)                      │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐ │
│  │  NVMe-oF    │  │    DPU      │  │  PowerScale │  │    NVMe Driver      │ │
│  │   Target    │  │   Client    │  │   Striping  │  │    (SPDK/CGO)       │ │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────────────┘ │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Project Structure

```
pstorage/
├── src/pstorage/
│   ├── api/                  # REST API (FastAPI)
│   │   ├── routes/           # API endpoints
│   │   │   ├── volumes.py
│   │   │   ├── snapshots.py
│   │   │   ├── metrics.py
│   │   │   ├── analytics.py  # Predictive analytics endpoints
│   │   │   └── replication.py
│   │   └── schemas/          # Pydantic models
│   ├── core/                 # Core storage logic
│   │   ├── controller.py     # Main orchestrator
│   │   ├── volume_manager.py
│   │   ├── snapshot_manager.py
│   │   └── config.py
│   ├── analytics/            # Predictive analytics
│   │   └── predictor.py      # StoragePredictor (forecasting, tiering)
│   ├── directflash/          # Flash management
│   │   ├── simulator.py      # DirectFlash simulator
│   │   ├── block_manager.py  # L2P mapping
│   │   ├── wear_leveling.py
│   │   └── garbage_collection.py
│   ├── purity/               # Data reduction
│   │   ├── engine.py         # Reduction pipeline
│   │   ├── deduplication.py  # SHA-256 fingerprinting
│   │   ├── compression.py    # LZ4/Zstd compression
│   │   └── pattern_removal.py
│   ├── activecluster/        # High availability
│   │   ├── cluster.py
│   │   ├── replication.py
│   │   ├── mediator.py
│   │   └── failover.py
│   └── storage/              # Storage backends
│       └── file_backend.py
├── nvmedrv/                  # Go NVMe driver (CGO/SPDK)
├── nvmeof/                   # Go NVMe-oF target
├── dpu/                      # Go DPU client
├── powerscale/               # Go distributed striping
├── tests/                    # Test suite (35 tests)
│   ├── test_api/
│   ├── test_purity/
│   └── test_analytics/
├── docs/                     # Documentation
└── docker/                   # Docker configuration
```

## Running Tests

```bash
# Activate virtual environment
source venv/bin/activate

# Run all Python tests
pytest

# Run with coverage
pytest --cov=pstorage

# Run specific test module
pytest tests/test_analytics/

# Run integration tests
./test_integration.sh
```

## Key Concepts

### Data Reduction Pipeline

All writes pass through the Purity engine:

1. **Pattern Removal** - Detect zero blocks and repeated patterns
2. **Deduplication** - Hash-based block deduplication (SHA-256)
3. **Compression** - LZ4 (fast, inline) or Zstd (high ratio, post-process)

Typical reduction ratios: 3:1 to 5:1 for mixed workloads.

### Predictive Analytics

The `StoragePredictor` provides intelligent insights:

| Feature | Description |
|---------|-------------|
| **Capacity Forecasting** | Linear regression predicting usage at 7/30/90 days |
| **Data Temperature** | Classifies data as hot/warm/cold/frozen based on access patterns |
| **Drive Health** | Scores health (0-100) based on wear, bad blocks, variance |
| **Tiering Recommendations** | Suggests optimal tier (NVMe/SSD/HDD/Archive) per volume |

### DirectFlash

Simulates direct flash management:

- **L2P Mapping** - Logical to physical address translation
- **Wear Leveling** - Distribute writes evenly across flash (threshold: 1000 P/E cycles)
- **Garbage Collection** - Background reclamation (triggers at 75% utilization)

### ActiveCluster

High availability features:

- **Synchronous Replication** - Zero data loss (RPO=0)
- **Asynchronous Replication** - Journal-based for lower latency
- **Automatic Failover** - Quorum-based with ~5s RTO
- **Mediator/Witness** - Split-brain prevention

## Performance Optimizations

The codebase implements several optimizations:

| Optimization | Location | Benefit |
|--------------|----------|---------|
| LRU Cache | `config.py` | Singleton settings |
| Async I/O | All modules | Non-blocking operations |
| Parallel Replication | `replication.py` | `asyncio.gather()` for peers |
| Background GC | `garbage_collection.py` | Non-blocking reclamation |
| Generators | `pattern_removal.py` | Lazy evaluation |
| Copy-on-Write | `volume_manager.py` | Efficient cloning |
| Path Sharding | `file_backend.py` | Reduced FS overhead |

## Docker

```bash
# Build image
docker build -t pstorage .

# Run container
docker run -p 8000:8000 pstorage

# Or using docker-compose
docker-compose up
```

## Documentation

- [NVMe Architecture](docs/nvme-architecture.md) - End-to-end NVMe, DirectFlash, and cluster coordination
- [E2E Testing Guide](docs/e2e-testing.md) - Integration testing documentation

## License

MIT License - see LICENSE file for details.
