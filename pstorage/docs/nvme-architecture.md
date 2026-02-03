# NVMe Architecture

This document explains the end-to-end NVMe architecture implemented in pstorage, including how DirectFlash technology bypasses traditional storage bottlenecks to deliver consistent low-latency performance.

## Overview

NVMe (Non-Volatile Memory Express) is a storage protocol designed specifically for flash and next-generation solid-state drives. Unlike legacy protocols (SATA, SAS), NVMe was built from the ground up to exploit the parallelism and low latency of flash storage.

### Key NVMe Advantages

| Feature | Legacy (SATA/SAS) | NVMe |
|---------|-------------------|------|
| Queue Depth | 1-32 | 65,535 |
| Queues | 1 | 65,535 |
| Command Set | Mechanical disk-oriented | Flash-optimized |
| Latency | ~6ms | ~10-20μs |
| IOPS (4K random) | ~100K | 1M+ |

## End-to-End NVMe Architecture

pstorage implements an end-to-end NVMe architecture that eliminates protocol translations and bottlenecks at every layer:

```
┌─────────────────────────────────────────────────────────────┐
│                      Application Layer                       │
│              (AI/ML, Analytics, Databases)                   │
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
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                     Raw NAND Flash                           │
│         (Direct access, no SSD controller overhead)          │
└─────────────────────────────────────────────────────────────┘
```

### Protocol Efficiency

Traditional storage stacks suffer from multiple protocol translations:

```
Application → SCSI → SAS/SATA → SSD Controller → Flash
```

End-to-end NVMe eliminates this:

```
Application → NVMe → DirectFlash → Flash
```

## DirectFlash Technology

DirectFlash is the core innovation that enables direct access to raw NAND flash, bypassing the SSD controller entirely. This approach provides:

### 1. Predictable Low Latency

By eliminating the SSD controller's firmware overhead, DirectFlash delivers consistent sub-millisecond latency without the variance introduced by:
- SSD garbage collection pauses
- Wear leveling delays
- Internal buffer management

**Implementation:** See [simulator.py](../src/pstorage/directflash/simulator.py)

```python
class DirectFlashSimulator:
    """
    Simulates Pure Storage's DirectFlash architecture:
    - Direct access to raw NAND flash (bypassing SSD controller)
    - Global wear leveling across all flash
    - Efficient garbage collection
    - Predictable latency
    """
```

### 2. Global Flash Management

Instead of each SSD managing its own flash independently, DirectFlash enables:

**Global Wear Leveling**
- Wear is distributed across ALL flash in the system
- No hot spots or premature wear-out
- Extended flash lifespan

**Implementation:** See [wear_leveling.py](../src/pstorage/directflash/wear_leveling.py)

```python
class WearLeveling:
    """
    Ensures even distribution of program/erase cycles across all flash blocks
    to maximize flash lifespan.

    Strategies:
    - Dynamic: Move hot (frequently written) data to less-worn blocks
    - Static: Also periodically move cold (rarely written) data
    """
```

**Global Garbage Collection**
- System-wide view of invalid blocks
- Reclaim space without impacting performance
- Background operation with minimal I/O disruption

**Implementation:** See [garbage_collection.py](../src/pstorage/directflash/garbage_collection.py)

### 3. Block Management

The block manager handles logical-to-physical address translation:

**Implementation:** See [block_manager.py](../src/pstorage/directflash/block_manager.py)

```python
class BlockManager:
    """
    Manages logical-to-physical block mapping.

    Simulates flash memory characteristics:
    - Pages can only be written once before erase
    - Erase is done at block granularity
    - Blocks wear out after many P/E cycles
    """
```

Block states in the lifecycle:

```
FREE → VALID → INVALID → (erase) → FREE
                  │
                  └→ BAD (if worn out)
```

## Data Reduction Pipeline

The Purity engine provides always-on data reduction without performance impact:

```
Raw Data → Pattern Removal → Deduplication → Compression → Storage
```

### Stage 1: Pattern Removal
Detects and encodes:
- Zero blocks (common in databases, VMs)
- Repeated byte patterns

### Stage 2: Deduplication
- Hash-based fingerprinting (SHA-256)
- Global deduplication across all volumes
- Reference counting for safe deletion

### Stage 3: Compression
- LZ4 (fast) or Zstd (high ratio)
- Only applied to unique, non-pattern data
- Metadata preserved for decompression

## Workload Optimization

### Real-Time Analytics

End-to-end NVMe architecture is essential for real-time analytics workloads:

| Requirement | How NVMe Addresses It |
|-------------|----------------------|
| High read throughput | Parallel NVMe queues |
| Low scan latency | No protocol translation |
| Random access patterns | Native flash optimization |
| Mixed read/write | QoS with queue prioritization |

**Example Use Cases:**
- Streaming analytics (Kafka, Flink)
- Real-time dashboards
- Log aggregation and search (Elasticsearch)

### AI/ML Workloads

Machine learning workloads have unique storage demands:

| ML Phase | Storage Pattern | NVMe Benefit |
|----------|-----------------|--------------|
| Data ingestion | Sequential write | High bandwidth |
| Training | Random read (shuffling) | Low latency, high IOPS |
| Checkpointing | Large sequential write | Consistent latency |
| Inference | Random read | Sub-ms response |

**GPU Direct Storage:**
NVMe enables direct data paths from storage to GPU memory, bypassing CPU:

```
Storage (NVMe) → PCIe → GPU Memory
```

This eliminates the traditional bottleneck:
```
Storage → CPU Memory → GPU Memory (2x copy overhead)
```

### Transactional Databases

OLTP databases require:

| Requirement | NVMe Solution |
|-------------|---------------|
| ACID durability | Fast, reliable writes |
| Low commit latency | Sub-ms write acknowledgment |
| High concurrency | 65K queues, massive parallelism |
| Consistent performance | No GC stalls with DirectFlash |

**Supported Workloads:**
- PostgreSQL, MySQL
- Oracle, SQL Server
- Distributed databases (CockroachDB, TiDB)

## Performance Characteristics

### Latency Profiles

| Operation | Typical Latency | Notes |
|-----------|-----------------|-------|
| 4K Random Read | 70-100μs | Flash read time |
| 4K Random Write | 10-20μs | Write to buffer |
| Sequential Read | Line rate | NVMe parallelism |
| Sequential Write | Line rate | With data reduction |

### IOPS Scaling

DirectFlash enables linear IOPS scaling:

```
Single Module:     ~200K IOPS
4 Modules:         ~800K IOPS
16 Modules:       ~3.2M IOPS
Full System:      10M+ IOPS
```

## Configuration

Key settings in pstorage that affect NVMe behavior:

```python
# Flash configuration
total_flash_blocks: int = 1024      # Total flash blocks
flash_block_size: int = 262144      # 256KB blocks
flash_page_size: int = 4096         # 4KB pages

# Wear leveling
wear_leveling_threshold: int = 1000 # P/E count diff trigger

# Garbage collection
gc_threshold: float = 0.85          # Start GC at 85% full
gc_target: float = 0.75             # Target 75% after GC
```

## Monitoring and Observability

### Key Metrics

The storage controller exposes comprehensive metrics:

```python
stats = await controller.get_stats()

# Flash health and wear
stats["flash"]["health_percentage"]      # % flash life remaining
stats["flash"]["utilization"]            # Current capacity usage

# Data reduction effectiveness
stats["data_reduction"]["total_reduction_ratio"]   # e.g., 5:1
stats["data_reduction"]["dedupe_bytes_saved"]      # Bytes saved

# Wear leveling status
stats["flash"]["wear_leveling"]["blocks_moved"]
stats["flash"]["wear_leveling"]["wear_variance"]

# Garbage collection activity
stats["flash"]["garbage_collection"]["total_bytes_reclaimed"]
```

### Health Monitoring

Flash health is calculated based on wear:

```python
health_percentage = ((max_endurance - avg_erase_count) / max_endurance) * 100
```

Monitor these indicators:
- **Health < 20%**: Plan flash replacement
- **Wear variance high**: Check wear leveling
- **GC frequency high**: Consider adding capacity

## Storage OS Principles

pstorage implements core storage operating system principles that govern how data flows through the system.

### Unified Storage Stack

Unlike general-purpose operating systems that treat storage as an afterthought, a storage OS is purpose-built for I/O:

```
┌────────────────────────────────────────────────────────────────┐
│                    Storage OS Architecture                      │
├────────────────────────────────────────────────────────────────┤
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐         │
│  │   REST API   │  │   iSCSI/FC   │  │   NVMe-oF    │  ← Host │
│  └──────────────┘  └──────────────┘  └──────────────┘  Access │
├────────────────────────────────────────────────────────────────┤
│                    Volume Abstraction Layer                     │
│        (Thin provisioning, snapshots, clones)                  │
├────────────────────────────────────────────────────────────────┤
│                    Data Services Layer                          │
│        (Deduplication, compression, encryption)                │
├────────────────────────────────────────────────────────────────┤
│                    Flash Translation Layer                      │
│        (L2P mapping, wear leveling, garbage collection)        │
├────────────────────────────────────────────────────────────────┤
│                    Hardware Abstraction                         │
│        (DirectFlash modules, NVMe controllers)                 │
└────────────────────────────────────────────────────────────────┘
```

### Key Design Principles

**1. Always-On Data Services**

Data reduction runs inline with every I/O—not as a background job:

```python
# From purity/engine.py
async def reduce_and_store(self, data: bytes) -> ReductionResult:
    """
    Apply full data reduction pipeline and store.

    Pipeline:
    1. Pattern removal (zero/pattern detection)
    2. Deduplication (if not pattern-reduced)
    3. Compression (if not deduplicated)
    """
```

**2. Metadata-Driven Architecture**

All data operations are metadata operations first:

| Layer | Metadata | Purpose |
|-------|----------|---------|
| Volume | Block map | Logical → Content ID |
| Dedupe | Fingerprint index | Content → Storage location |
| Flash | L2P table | Logical → Physical address |

**3. Copy-on-Write Everything**

Snapshots and clones are instantaneous because they share data:

```
Volume A:  [block1] → content_abc
                        ↑
Snapshot:  [block1] ────┘  (same pointer, no copy)
```

**4. Global Resource Management**

Resources are managed globally, not per-device:

- **Global deduplication** across all volumes
- **Global wear leveling** across all flash
- **Global garbage collection** coordinated system-wide

### I/O Path Optimization

The storage OS optimizes the I/O path at every level:

```
Write Path (optimized):
┌─────────┐    ┌─────────┐    ┌─────────┐    ┌─────────┐
│ Receive │ →  │ Reduce  │ →  │ Dedupe  │ →  │  Store  │
│   I/O   │    │ (inline)│    │ (check) │    │ (if new)│
└─────────┘    └─────────┘    └─────────┘    └─────────┘
     │                              │
     │                              ▼
     │                        Already exists?
     │                        Just update metadata
     │
     └── Acknowledge to host (after metadata committed)

Read Path (optimized):
┌─────────┐    ┌─────────┐    ┌─────────┐    ┌─────────┐
│ Receive │ →  │ Lookup  │ →  │  Fetch  │ →  │ Decode  │
│   I/O   │    │ metadata│    │  data   │    │ & send  │
└─────────┘    └─────────┘    └─────────┘    └─────────┘
```

## Cluster Coordination

pstorage implements ActiveCluster for high availability with zero data loss.

### Architecture Overview

```
                    ┌─────────────────┐
                    │    Mediator     │
                    │  (Quorum/Tie-   │
                    │   breaker)      │
                    └────────┬────────┘
                             │
              ┌──────────────┼──────────────┐
              │              │              │
              ▼              ▼              ▼
        ┌──────────┐   ┌──────────┐   ┌──────────┐
        │  Node A  │◄─►│  Node B  │◄─►│  Node C  │
        │ PRIMARY  │   │SECONDARY │   │SECONDARY │
        └──────────┘   └──────────┘   └──────────┘
              │              │              │
              ▼              ▼              ▼
        ┌──────────┐   ┌──────────┐   ┌──────────┐
        │  Flash   │   │  Flash   │   │  Flash   │
        │ Modules  │   │ Modules  │   │ Modules  │
        └──────────┘   └──────────┘   └──────────┘
```

**Implementation:** See [cluster.py](../src/pstorage/activecluster/cluster.py)

### Cluster States

```python
class ClusterState(Enum):
    INITIALIZING = "initializing"  # Cluster starting up
    HEALTHY = "healthy"            # All nodes operational
    DEGRADED = "degraded"          # Some nodes down, still operational
    FAILED = "failed"              # Lost quorum, read-only
    MAINTENANCE = "maintenance"    # Planned maintenance mode
```

### Quorum and Split-Brain Prevention

The mediator ensures cluster consistency:

**Implementation:** See [mediator.py](../src/pstorage/activecluster/mediator.py)

```python
class Mediator:
    """
    Cluster mediator for quorum and split-brain prevention.

    The mediator:
    - Maintains cluster membership
    - Monitors node health via heartbeats
    - Manages quorum (majority vote for decisions)
    - Prevents split-brain by arbitrating primary selection
    - Coordinates failover decisions
    """
```

**Quorum Rules:**
- Minimum nodes for quorum: `(total_nodes // 2) + 1`
- No writes without quorum (prevents data divergence)
- Mediator acts as tie-breaker in 2-node clusters

### Replication Modes

**Implementation:** See [replication.py](../src/pstorage/activecluster/replication.py)

| Mode | Behavior | RPO | Use Case |
|------|----------|-----|----------|
| **Synchronous** | Write acknowledged after ALL replicas confirm | 0 | Mission-critical data |
| **Asynchronous** | Write acknowledged after primary, replicate in background | >0 | Performance-sensitive |

```python
class ReplicationMode(Enum):
    SYNC = "sync"   # Zero data loss, higher latency
    ASYNC = "async" # Lower latency, potential data loss on failure
```

**Synchronous Write Flow:**

```
Host Write
    │
    ▼
┌─────────┐     parallel      ┌─────────┐
│ Primary │ ────────────────► │Secondary│
│  write  │                   │  write  │
└────┬────┘                   └────┬────┘
     │                             │
     │◄────────── ack ─────────────┤
     │
     ▼
 Ack to Host (only after both complete)
```

### Automatic Failover

**Implementation:** See [failover.py](../src/pstorage/activecluster/failover.py)

```python
class FailoverController:
    """
    Automatic failover controller.

    Handles:
    - Detection of primary failure
    - Coordinated failover to secondary
    - Automatic failback when original primary recovers
    - Manual failover for maintenance

    Ensures zero data loss (RPO=0) and minimal downtime (RTO~0).
    """
```

**Failover State Machine:**

```
     ┌──────────────────────────────────────┐
     │                                      │
     ▼                                      │
┌─────────┐   primary fails   ┌────────────┴──┐
│ NORMAL  │ ────────────────► │ FAILING_OVER  │
└─────────┘                   └───────┬───────┘
     ▲                                │
     │                                │ election complete
     │                                ▼
     │   original recovers    ┌───────────────┐
     └─────────────────────── │  FAILED_OVER  │
        (auto failback)       └───────────────┘
```

**Failover Timeline (typical):**

| Phase | Duration | Action |
|-------|----------|--------|
| Detection | 5 sec | Heartbeat timeout |
| Election | <100 ms | Quorum-based selection |
| Promotion | <100 ms | New primary activated |
| I/O Resume | <100 ms | Host reconnects |
| **Total RTO** | **~5 seconds** | |

### Configuration

```python
# ActiveCluster settings in config.py
cluster_enabled: bool = False
node_id: str = "node-1"
peer_nodes: list[str] = []           # URLs of peer nodes
replication_mode: Literal["sync", "async"] = "sync"
heartbeat_interval_ms: int = 1000    # Health check frequency
failover_timeout_ms: int = 5000      # Time before declaring failure
```

## Low-Level Hardware Interactions

This section covers how pstorage interacts with flash hardware at the lowest level.

### NAND Flash Characteristics

Flash memory has fundamental physical characteristics that drive the architecture:

**1. Asymmetric Operations**

| Operation | Granularity | Time | Notes |
|-----------|-------------|------|-------|
| Read | Page (4KB) | ~50μs | Fast, random access |
| Write | Page (4KB) | ~200μs | Sequential within block |
| Erase | Block (256KB) | ~2ms | Must erase before rewrite |

**2. Write Amplification**

Flash cannot overwrite—it must erase first. This causes write amplification:

```
Logical Write: 4KB
                │
                ▼
    ┌─────────────────────────┐
    │ If block has valid data:│
    │ 1. Read entire block    │  ← 256KB read
    │ 2. Modify 4KB           │
    │ 3. Write to new block   │  ← 256KB write
    │ 4. Erase old block      │  ← Full block erase
    └─────────────────────────┘
                │
                ▼
Actual I/O: 512KB+ (WAF > 100x worst case)
```

pstorage minimizes WAF through:
- Log-structured writes (append-only)
- Intelligent garbage collection
- Global wear leveling

**3. Block States**

```python
class BlockState(Enum):
    FREE = "free"       # Erased, ready for write
    VALID = "valid"     # Contains current data
    INVALID = "invalid" # Contains stale data, can be erased
    BAD = "bad"         # Failed block, unusable
```

### Logical-to-Physical Mapping

The L2P table is the core data structure for flash management:

```
Logical Address Space          Physical Flash
─────────────────────         ──────────────────
    LBA 0  ────────────────►  Block 42, Page 3
    LBA 1  ────────────────►  Block 17, Page 0
    LBA 2  ────────────────►  Block 89, Page 7
    ...
```

**Implementation:** See [block_manager.py](../src/pstorage/directflash/block_manager.py)

```python
class BlockManager:
    def __init__(self):
        # Logical to physical mapping (L2P table)
        self._l2p_table: dict[int, int] = {}

        # Free block pool
        self._free_blocks: set[int] = set(range(total_blocks))
```

### Garbage Collection Details

GC reclaims space from invalid blocks:

```
Before GC:
┌────────┬────────┬────────┬────────┐
│ VALID  │INVALID │ VALID  │INVALID │  Block A (50% valid)
└────────┴────────┴────────┴────────┘
┌────────┬────────┬────────┬────────┐
│INVALID │INVALID │INVALID │ VALID  │  Block B (25% valid)
└────────┴────────┴────────┴────────┘

GC Process:
1. Select victim block (Block B - lowest validity)
2. Copy valid pages to new block
3. Erase victim block

After GC:
┌────────┬────────┬────────┬────────┐
│ VALID  │INVALID │ VALID  │INVALID │  Block A (unchanged)
└────────┴────────┴────────┴────────┘
┌────────┬────────┬────────┬────────┐
│  FREE  │  FREE  │  FREE  │  FREE  │  Block B (erased, reusable)
└────────┴────────┴────────┴────────┘
┌────────┬────────┬────────┬────────┐
│ VALID  │  FREE  │  FREE  │  FREE  │  Block C (compacted data)
└────────┴────────┴────────┴────────┘
```

**GC Thresholds:**

```python
gc_threshold: float = 0.75  # Start GC when 75% full
gc_target: float = 0.50     # GC until 50% free
```

### Wear Leveling Mechanics

Flash blocks wear out after ~100,000 program/erase cycles:

```
Block Wear Over Time:

  Erase Count
      ▲
100K ─┼─────────────────────────────────────── BAD (worn out)
      │                                    ╱
 75K ─┼───────────────────────────────────╱
      │                              ╱
 50K ─┼─────────────────────────────╱    ← Hot block (without leveling)
      │                        ╱
 25K ─┼───────────────────────╱
      │              ╱╱╱╱╱╱╱╱
   0 ─┼─────────────╱───────────────────── Cold blocks (rarely written)
      └────────────────────────────────────►
                                      Time
```

**Wear Leveling Strategies:**

```python
class WearLevelingStrategy(Enum):
    DYNAMIC = "dynamic"  # Move hot data to less-worn blocks
    STATIC = "static"    # Also move cold data periodically
```

**Dynamic Leveling Algorithm:**

```
1. Find most-worn VALID block (source)
2. Find least-worn FREE block (destination)
3. If (source.erase_count - dest.erase_count) > threshold:
   a. Copy data from source to destination
   b. Update L2P mapping
   c. Mark source as INVALID
   d. Erase source (becomes FREE)
```

### Hardware Abstraction

The storage backend abstracts physical storage:

```python
# From storage/backend.py
class StorageBackend(ABC):
    """Abstract storage backend interface."""

    @abstractmethod
    async def write_block(self, block_id: str, data: bytes) -> bool:
        """Write a block to storage."""

    @abstractmethod
    async def read_block(self, block_id: str) -> bytes:
        """Read a block from storage."""

    @abstractmethod
    async def delete_block(self, block_id: str) -> bool:
        """Delete a block from storage."""
```

This abstraction allows:
- File-based backend for development/testing
- DirectFlash backend for production
- Cloud backend for hybrid deployments

### NVMe Command Interface

At the lowest level, NVMe commands interact with flash:

| Command | Purpose | Queue Type |
|---------|---------|------------|
| Read | Fetch data | I/O Queue |
| Write | Store data | I/O Queue |
| Flush | Persist buffers | I/O Queue |
| TRIM/Deallocate | Mark blocks unused | I/O Queue |
| Identify | Query device info | Admin Queue |
| Get Log Page | Read telemetry | Admin Queue |

**TRIM Command Flow:**

```
Application deletes file
        │
        ▼
Filesystem sends TRIM
        │
        ▼
NVMe TRIM command
        │
        ▼
Mark blocks as INVALID in L2P
        │
        ▼
GC can now reclaim these blocks
```

## Further Reading

- [DirectFlash Simulator](../src/pstorage/directflash/simulator.py) - Main flash interface
- [Block Manager](../src/pstorage/directflash/block_manager.py) - L2P mapping
- [Wear Leveling](../src/pstorage/directflash/wear_leveling.py) - Flash longevity
- [Garbage Collection](../src/pstorage/directflash/garbage_collection.py) - Space reclamation
- [Purity Engine](../src/pstorage/purity/engine.py) - Data reduction
- [Storage Controller](../src/pstorage/core/controller.py) - Orchestration
- [ActiveCluster](../src/pstorage/activecluster/cluster.py) - High availability
- [Mediator](../src/pstorage/activecluster/mediator.py) - Quorum management
- [Replication](../src/pstorage/activecluster/replication.py) - Data synchronization
- [Failover Controller](../src/pstorage/activecluster/failover.py) - Automatic failover
