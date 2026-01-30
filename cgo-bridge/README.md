# CGO Bridge for C++ Hardware Acceleration

This package provides optional C++ integration for hardware-accelerated storage operations.

## Build Modes

### Pure Go Build (Default)
```bash
go build ./...
```
Uses software implementations from nvmeof, unity, and powerscale packages.

### CGO Build (Hardware Acceleration)
```bash
CGO_ENABLED=1 go build -tags cgo ./...
```
Links with C++ code in `cpp-legacy/` for direct hardware access.

## Requirements for CGO Build

1. **SPDK Libraries**
   ```bash
   # Install SPDK
   git clone https://github.com/spdk/spdk
   cd spdk
   ./scripts/pkgdep.sh
   ./configure
   make
   ```

2. **NVMe Libraries**
   ```bash
   # Kernel NVMe headers
   sudo apt-get install linux-headers-$(uname -r)
   ```

3. **Build Tools**
   ```bash
   sudo apt-get install build-essential g++
   ```

## Architecture

```
┌─────────────────────────────────────┐
│     Go Application Layer            │
│  (nvmeof, unity, powerscale)        │
└──────────────┬──────────────────────┘
               │
         ┌─────┴─────┐
         │           │
    Pure Go      CGO Bridge
    (default)    (optional)
         │           │
         │           ▼
         │     C++ Hardware Layer
         │     (cpp-legacy/)
         │           │
         └───────────┴──────────────────▶ Storage Hardware
```

## Usage

### Option 1: Pure Go (Software)
```go
import "github.com/srilakshmi/storage/nvmeof"

initiator := nvmeof.NewNVMeoFInitiator("192.168.1.100:4420", "nqn.host")
```

### Option 2: CGO (Hardware Accelerated)
```go
import "github.com/srilakshmi/storage/cgo-bridge"

// Direct hardware access
ctrl, err := cgobridge.NewNVMeHardwareController("0000:04:00.0")
if err != nil {
    // Fallback to software
}

// Use hardware controller
ctrl.ReadBlocks(1, 0, 8, buffer)
stats, _ := ctrl.GetStats()
```

## Performance Comparison

| Operation | Pure Go | CGO (C++) |
|-----------|---------|-----------|
| Latency | ~100μs | ~10μs |
| IOPS | 100K | 1M+ |
| CPU | Higher | Lower |
| Memory | Safe | Unsafe |

## Trade-offs

**Pure Go:**
- ✅ Cross-platform
- ✅ Memory safe
- ✅ Easy deployment
- ❌ Higher latency
- ❌ Software overhead

**CGO + C++:**
- ✅ Direct hardware access
- ✅ Ultra-low latency
- ✅ Maximum IOPS
- ❌ Platform-specific
- ❌ Complex build
- ❌ Unsafe operations

## Recommendation

- **Development/Testing:** Pure Go
- **Production (General):** Pure Go
- **Production (Ultra-High Performance):** CGO + C++
