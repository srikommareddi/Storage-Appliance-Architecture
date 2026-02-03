# API Reference

PStorage provides a RESTful API for storage management. The API follows REST conventions and returns JSON responses.

**Base URL:** `http://localhost:8000/api/v1`

## Authentication

Currently, the API does not require authentication. In production deployments, configure authentication via reverse proxy or extend the API middleware.

## Common Response Codes

| Code | Description |
|------|-------------|
| 200 | Success |
| 201 | Created |
| 204 | No Content (successful delete) |
| 400 | Bad Request |
| 404 | Not Found |
| 500 | Internal Server Error |

---

## Health & Status

### Health Check

```http
GET /health
```

**Response:**

```json
{
  "status": "healthy",
  "reduction_ratio": 2.5,
  "cluster_enabled": false
}
```

### List Arrays

```http
GET /api/v1/arrays
```

Returns information about the storage array.

**Response:**

```json
{
  "arrays": [
    {
      "id": "node-1",
      "name": "PStorage",
      "model": "PStorage Virtual Array",
      "version": "0.1.0",
      "capacity": {
        "total_bytes": 268435456,
        "used_bytes": 52428800,
        "reduction_ratio": 3.2
      }
    }
  ]
}
```

---

## Volumes

### Create Volume

```http
POST /api/v1/volumes
```

**Request Body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Volume name (1-255 chars) |
| `size_gb` | float | Yes | Size in GB (max 1024) |
| `description` | string | No | Optional description |
| `tags` | object | No | Key-value metadata |

**Example:**

```bash
curl -X POST http://localhost:8000/api/v1/volumes \
  -H "Content-Type: application/json" \
  -d '{
    "name": "production-db",
    "size_gb": 100,
    "description": "PostgreSQL data volume",
    "tags": {"env": "production", "app": "postgres"}
  }'
```

**Response (201):**

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "name": "production-db",
  "size_bytes": 107374182400,
  "used_bytes": 0,
  "state": "available",
  "description": "PostgreSQL data volume",
  "created_at": "2024-01-15T10:30:00Z",
  "updated_at": "2024-01-15T10:30:00Z",
  "tags": {"env": "production", "app": "postgres"},
  "reduction_ratio": 1.0
}
```

### List Volumes

```http
GET /api/v1/volumes
```

**Response:**

```json
{
  "volumes": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "name": "production-db",
      "size_bytes": 107374182400,
      "used_bytes": 52428800,
      "state": "available",
      "description": "PostgreSQL data volume",
      "created_at": "2024-01-15T10:30:00Z",
      "updated_at": "2024-01-15T10:35:00Z",
      "tags": {"env": "production"},
      "reduction_ratio": 2.5
    }
  ],
  "total": 1
}
```

### Get Volume

```http
GET /api/v1/volumes/{volume_id}
```

**Response:** Same as single volume object above.

### Update Volume

```http
PUT /api/v1/volumes/{volume_id}
```

**Request Body:**

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | New name |
| `description` | string | New description |
| `tags` | object | New tags (replaces existing) |

All fields are optional. Only provided fields are updated.

### Delete Volume

```http
DELETE /api/v1/volumes/{volume_id}
```

**Response:** 204 No Content

### Resize Volume

```http
POST /api/v1/volumes/{volume_id}/resize
```

**Request Body:**

```json
{
  "size_gb": 200
}
```

Note: Only expansion is supported. The new size must be larger than current size.

### Clone Volume

```http
POST /api/v1/volumes/{volume_id}/clone
```

Creates a copy-on-write clone of the volume.

**Request Body:**

```json
{
  "name": "production-db-clone",
  "description": "Clone for testing"
}
```

**Response:** New volume object.

---

## Data Operations

### Write Data

```http
POST /api/v1/volumes/{volume_id}/write
```

**Request Body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `offset` | integer | Yes | Byte offset (must be block-aligned to 4096) |
| `data` | string | Yes | Base64-encoded data |

**Example:**

```bash
# Write "Hello World!" at offset 0
curl -X POST http://localhost:8000/api/v1/volumes/{volume_id}/write \
  -H "Content-Type: application/json" \
  -d '{
    "offset": 0,
    "data": "SGVsbG8gV29ybGQh"
  }'
```

**Response:**

```json
{
  "volume_id": "550e8400-e29b-41d4-a716-446655440000",
  "offset": 0,
  "size": 12,
  "reduction_ratio": 1.0,
  "was_deduplicated": false,
  "was_compressed": true
}
```

### Read Data

```http
GET /api/v1/volumes/{volume_id}/read?offset=0
```

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `offset` | integer | 0 | Byte offset to read from |

**Response:**

```json
{
  "volume_id": "550e8400-e29b-41d4-a716-446655440000",
  "offset": 0,
  "size": 12,
  "data": "SGVsbG8gV29ybGQh"
}
```

The `data` field is base64-encoded.

---

## Snapshots

### Create Snapshot

```http
POST /api/v1/volumes/{volume_id}/snapshots
```

**Request Body:**

```json
{
  "name": "daily-backup-2024-01-15",
  "description": "Automated daily backup"
}
```

**Response (201):**

```json
{
  "id": "660e8400-e29b-41d4-a716-446655440001",
  "name": "daily-backup-2024-01-15",
  "volume_id": "550e8400-e29b-41d4-a716-446655440000",
  "volume_name": "production-db",
  "size_bytes": 107374182400,
  "used_bytes": 52428800,
  "state": "available",
  "description": "Automated daily backup",
  "created_at": "2024-01-15T00:00:00Z",
  "tags": {}
}
```

### List Volume Snapshots

```http
GET /api/v1/volumes/{volume_id}/snapshots
```

**Response:**

```json
{
  "snapshots": [...],
  "total": 5
}
```

### List All Snapshots

```http
GET /api/v1/snapshots
```

Lists snapshots across all volumes.

### Get Snapshot

```http
GET /api/v1/volumes/{volume_id}/snapshots/{snapshot_id}
```

### Delete Snapshot

```http
DELETE /api/v1/volumes/{volume_id}/snapshots/{snapshot_id}
```

### Restore Snapshot

```http
POST /api/v1/volumes/{volume_id}/snapshots/{snapshot_id}/restore
```

**Request Body:**

```json
{
  "target_volume_id": null
}
```

If `target_volume_id` is null, creates a new volume from the snapshot.
If specified, restores to the existing volume (overwrites current data).

---

## Metrics

### All Metrics

```http
GET /api/v1/metrics
```

Returns comprehensive system metrics including data reduction, flash health, volumes, and cluster status.

### Data Reduction Metrics

```http
GET /api/v1/metrics/reduction
```

**Response:**

```json
{
  "overall_reduction_ratio": 3.5,
  "deduplication": {
    "ratio": 2.1,
    "unique_blocks": 1000,
    "duplicate_blocks": 1100
  },
  "compression": {
    "ratio": 1.7,
    "algorithm": "lz4"
  },
  "pattern_removal": {
    "zero_blocks_found": 500,
    "pattern_blocks_found": 50
  },
  "bytes_saved": {
    "pattern": 2097152,
    "dedupe": 4608000,
    "compression": 1572864
  }
}
```

### Flash Metrics

```http
GET /api/v1/metrics/flash
```

**Response:**

```json
{
  "capacity": {
    "total_bytes": 268435456,
    "used_bytes": 134217728,
    "free_bytes": 134217728,
    "utilization": 0.5
  },
  "health": {
    "health_percentage": 99.5,
    "avg_erase_count": 50,
    "max_erase_count": 120
  },
  "wear_leveling": {
    "blocks_moved": 15,
    "wear_variance": 25.3
  },
  "garbage_collection": {
    "state": "idle",
    "blocks_reclaimed": 100,
    "gc_runs": 5
  }
}
```

### Volume Metrics

```http
GET /api/v1/metrics/volumes
```

**Response:**

```json
{
  "volumes": {
    "total": 5,
    "provisioned_bytes": 536870912000,
    "used_bytes": 107374182400
  },
  "snapshots": {
    "total": 25,
    "by_volume": {
      "vol-1": 5,
      "vol-2": 10,
      "vol-3": 10
    }
  }
}
```

### Replication Metrics

```http
GET /api/v1/metrics/replication
```

**Response (clustering disabled):**

```json
{
  "enabled": false,
  "message": "Clustering is not enabled"
}
```

**Response (clustering enabled):**

```json
{
  "enabled": true,
  "mode": "sync",
  "peers": 2,
  "operations": {
    "replicated": 15000,
    "pending": 0,
    "errors": 2
  },
  "latency_ms": 1.5,
  "failover": {
    "state": "normal",
    "total_failovers": 1,
    "avg_time_ms": 4500
  }
}
```

---

## Cluster Management

### Cluster Status

```http
GET /api/v1/cluster/status
```

**Response:**

```json
{
  "node_id": "node-1",
  "state": "healthy",
  "current_primary": "node-1",
  "has_quorum": true,
  "node_count": 3,
  "healthy_nodes": 3,
  "replication_lag_ms": 0.5
}
```

### Replication Status

```http
GET /api/v1/cluster/replication
```

**Response:**

```json
{
  "mode": "sync",
  "node_id": "node-1",
  "is_primary": true,
  "peers": [
    {
      "node_id": "node-2",
      "url": "http://node-2:8000",
      "state": "healthy",
      "last_heartbeat": 1705312200.5,
      "lag_bytes": 0
    }
  ],
  "operations_replicated": 15000,
  "operations_pending": 0,
  "avg_latency_ms": 1.2
}
```

### Add Peer

```http
POST /api/v1/cluster/peers
```

**Request Body:**

```json
{
  "node_id": "node-2",
  "url": "http://node-2:8000"
}
```

### Remove Peer

```http
DELETE /api/v1/cluster/peers/{node_id}
```

### Manual Failover

```http
POST /api/v1/cluster/failover
```

**Request Body:**

```json
{
  "target_node": "node-2"
}
```

If `target_node` is null, the system elects a new primary automatically.

**Response:**

```json
{
  "old_primary": "node-1",
  "new_primary": "node-2",
  "reason": "manual",
  "state": "failed_over",
  "duration_ms": 150
}
```

### Manual Failback

```http
POST /api/v1/cluster/failback
```

Restores the original primary after a failover.

### Heartbeat (Internal)

```http
POST /api/v1/cluster/heartbeat
```

Used internally for cluster health monitoring between nodes.

---

## Error Responses

All errors follow this format:

```json
{
  "detail": "Error message describing what went wrong"
}
```

### Common Errors

**400 Bad Request:**

```json
{
  "detail": "Offset must be aligned to block size (4096)"
}
```

**404 Not Found:**

```json
{
  "detail": "Volume 550e8400-e29b-41d4-a716-446655440000 not found"
}
```

---

## Rate Limiting

The API does not implement rate limiting by default. For production deployments, configure rate limiting at the reverse proxy level.

## Pagination

Currently, list endpoints return all items. For large deployments, implement pagination by extending the routes.

## OpenAPI Specification

The full OpenAPI specification is available at:

- **Swagger UI:** `http://localhost:8000/docs`
- **ReDoc:** `http://localhost:8000/redoc`
- **OpenAPI JSON:** `http://localhost:8000/openapi.json`
