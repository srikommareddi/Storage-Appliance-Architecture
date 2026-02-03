# DPU Integration Architecture (Future Enhancement)

## Overview

This document outlines how the Storage Appliance Architecture could be enhanced with DPU (Data Processing Unit) offload capabilities for improved performance and CPU efficiency.

## Target DPU Platforms

- **NVIDIA BlueField-2/3 DPU**
  - ARM cores for compute offload
  - NVMe-oF acceleration
  - RDMA networking

- **AMD Pensando DPU**
  - P4-programmable pipeline
  - Storage protocol offload
  - Hardware crypto acceleration

## Proposed Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    Host CPU (x86)                        │
│  ┌─────────────────────────────────────────────────┐    │
│  │  Storage Appliance Control Plane (Go)           │    │
│  │  - Management API (S3, Admin)                   │    │
│  │  - Configuration                                │    │
│  │  - Monitoring/Metrics                           │    │
│  └──────────────────┬──────────────────────────────┘    │
└────────────────────┼────────────────────────────────────┘
                     │ PCIe / RDMA
                     ▼
┌─────────────────────────────────────────────────────────┐
│                 DPU (BlueField/Pensando)                 │
│  ┌─────────────────────────────────────────────────┐    │
│  │  Data Plane (Offloaded to DPU ARM Cores)        │    │
│  │  ┌─────────────────────────────────────────┐    │    │
│  │  │  NVMe-oF Target (nvmeof/target.go)     │    │    │
│  │  │  - TCP/RDMA transport                   │    │    │
│  │  │  - Namespace management                 │    │    │
│  │  │  - I/O command processing               │    │    │
│  │  └─────────────────────────────────────────┘    │    │
│  │  ┌─────────────────────────────────────────┐    │    │
│  │  │  Erasure Coding Acceleration            │    │    │
│  │  │  - Reed-Solomon on DPU cores            │    │    │
│  │  │  - Hardware XOR engines                 │    │    │
│  │  └─────────────────────────────────────────┘    │    │
│  │  ┌─────────────────────────────────────────┐    │    │
│  │  │  Compression Offload                    │    │    │
│  │  │  - Hardware compression engines          │    │    │
│  │  │  - zstd/lz4 acceleration                │    │    │
│  │  └─────────────────────────────────────────┘    │    │
│  └─────────────────┬───────────────────────────────┘    │
│                    │                                     │
│                    ▼                                     │
│         ┌──────────────────────┐                        │
│         │  DPU Local NVMe      │                        │
│         │  High-speed storage  │                        │
│         └──────────────────────┘                        │
└─────────────────────────────────────────────────────────┘
```

## Benefits of DPU Offload

### Performance
- **Lower Latency**: Sub-10μs I/O latency with DPU-direct NVMe access
- **Higher IOPS**: 5M+ IOPS per DPU (vs 100K CPU-based)
- **Zero-Copy**: Direct DPU-to-NVMe transfers without host CPU involvement

### CPU Efficiency
- **CPU Offload**: Free host CPU for application workloads
- **Reduced PCIe Traffic**: Data stays on DPU for local NVMe access
- **Network Offload**: DPU handles NVMe-oF protocol processing

### Scalability
- **Multi-DPU**: Scale horizontally with multiple DPUs per server
- **Disaggregation**: Separate compute and storage resources
- **Resource Isolation**: Storage operations isolated from compute

## Implementation Plan

### Phase 1: DPU Communication Layer
```go
// dpu/client.go
type DPUClient struct {
    addr       string
    metrics    *DPUMetrics
}

func NewDPUClient(addr string) *DPUClient {
    // Local DPU client (placeholder for gRPC/DOCA)
}

func (c *DPUClient) OffloadNVMeTarget(ctx context.Context,
    config NVMeTargetConfig) (*DPUNVMeoFTarget, error) {
    // Register NVMe-oF target on DPU
}
```

### Phase 2: Data Plane Offload
```go
// nvmeof/target_dpu.go
type DPUNVMeoFTarget struct {
    dpuClient *dpu.DPUClient
    subsystems map[string]*Subsystem
}

func (t *DPUNVMeoFTarget) Start() error {
    // Start NVMe-oF target on DPU
    return t.dpuClient.OffloadNVMeTarget(t.config)
}
```

### Example Usage
```go
import (
    "context"
    "time"

    "github.com/srilakshmi/storage/dpu"
)

client := dpu.NewClient("dpu://local")
ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
defer cancel()

target, _ := client.OffloadNVMeTarget(ctx, dpu.NVMeTargetConfig{
    SubsystemNQN: "nqn.storage",
    ListenAddr:  "0.0.0.0:4420",
    HostNQN:     "nqn.host",
    NamespaceID: 1,
    SizeBytes:   1024 * 1024 * 1024,
    BlockSize:   4096,
})

_ = target.Running()
```

### Phase 3: Erasure Coding Offload
```go
// powerscale/erasure_dpu.go
func (ofs *OneFS) encodeStripeDPU(data []byte,
                                   protection ProtectionLevel) ([][]byte, error) {
    // Offload erasure coding to DPU
    return ofs.dpuClient.EncodeStripe(data,
                                       protection.DataStripes,
                                       protection.ParityStripes)
}
```

## Performance Targets with DPU

| Metric | CPU-Based | DPU-Based | Improvement |
|--------|-----------|-----------|-------------|
| Read Latency | 10.5 μs | 3 μs | 3.5x faster |
| Write Latency | 10.5 μs | 4 μs | 2.6x faster |
| IOPS | 100K | 5M+ | 50x higher |
| CPU Usage | 100% | 10% | 90% reduction |
| Network BW | Limited | 200 Gbps | DPU RDMA |

## Driver Performance Targets (GPU + DPU)

### GPU Offload Targets (Erasure + Compression)
- **Reed-Solomon Encode**: 10-20 GB/s per GPU (A100/H100 class)
- **Reed-Solomon Decode**: 8-16 GB/s per GPU
- **Compression (zstd/lz4)**: 4-8 GB/s with GPU pipelines
- **Use Cases**: Large object ingest, rebuild storms, bulk rebalancing

### DPU NVMe/NVMe-oF Driver Targets
- **NVMe-oF TCP**: 1-2M IOPS per DPU with SPDK poll-mode
- **NVMe-oF RDMA**: 3-5M IOPS per DPU (RoCEv2)
- **P99 Latency**: <5 μs on local NVMe, <10 μs over fabric
- **CPU Savings**: 70-90% host CPU offload for I/O path

## DPU Vendor Support

### NVIDIA DOCA SDK Integration
```bash
# Install DOCA SDK
apt-get install doca-sdk

# Build with DOCA support
CGO_ENABLED=1 go build -tags doca ./...
```

### AMD Pensando P4 Integration
```bash
# Install Pensando SDK
apt-get install pensando-sdk

# Build with Pensando support
CGO_ENABLED=1 go build -tags pensando ./...
```

## Code Changes Required

### Minimal Changes to Existing Code
The current architecture is **DPU-ready** with minimal modifications:

1. **Interface Abstraction**: ✅ Already have clean interfaces
2. **Modular Design**: ✅ NVMe-oF target is standalone
3. **Performance Focus**: ✅ Already optimized for low latency

### Estimated Implementation Effort
- **DPU Client Library**: implemented (local client placeholder)
- **NVMe-oF Offload**: planned (1-2 weeks)
- **Erasure Coding Offload**: implemented (local DPU emulator + worker pool)
- **Testing & Integration**: pending
- **Total**: ~2 months for full DPU integration

## Real-World Use Cases

### Use Case 1: Disaggregated Storage
- Host CPUs run applications
- DPUs handle all storage I/O
- Scale storage independently from compute

### Use Case 2: Multi-Tenant Storage
- Each tenant gets dedicated DPU resources
- Strong isolation between tenants
- Predictable performance per tenant

### Use Case 3: Edge Storage
- Low-power ARM cores on DPU
- Local NVMe caching
- Efficient remote replication

## References

- NVIDIA BlueField DPU: https://www.nvidia.com/en-us/networking/products/data-processing-unit/
- AMD Pensando: https://www.amd.com/en/products/accelerators/pensando.html
- SPDK on DPU: https://spdk.io/doc/dpu.html
- Dell PowerStore DPU Integration: Dell EMC architecture papers

## Status

**Current Status**: Architecture designed, DPU integration planned
**Next Steps**: Implement DPU client library and communication protocol
**Timeline**: Q2 2026 (planned)
