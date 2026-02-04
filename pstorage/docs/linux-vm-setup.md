# Linux VM Setup for Go/SPDK Development

This guide covers setting up a Linux VM on macOS to run the Go NVMe/SPDK components.

## Why a VM?

The Go components (`nvmedrv`, `nvmeof`, `dpu`) require:
- **SPDK** - Storage Performance Development Kit (Linux only)
- **CGO** - Go's C interop (works on macOS, but SPDK doesn't)
- **Linux kernel interfaces** - For direct NVMe access

## Option 1: UTM (Recommended for Apple Silicon)

### Step 1: Install UTM

```bash
# Via Homebrew
brew install --cask utm

# Or download from: https://mac.getutm.app/
```

### Step 2: Download Ubuntu

- Go to: https://ubuntu.com/download/server
- Download **Ubuntu Server 22.04 LTS**
- For Apple Silicon: Choose ARM64 (aarch64)
- For Intel Mac: Choose AMD64

### Step 3: Create VM

1. Open UTM → Create New VM
2. Select "Virtualize" (faster) or "Emulate" (more compatible)
3. Choose "Linux"
4. Select the Ubuntu ISO
5. Configure:
   - **RAM**: 4096 MB (4 GB minimum)
   - **CPU**: 2-4 cores
   - **Storage**: 30 GB
6. In **Devices**, add:
   - "New Drive" → NVMe (for testing NVMe features)

### Step 4: Install Ubuntu

1. Start the VM
2. Follow Ubuntu installer
3. Choose minimal installation
4. Set username: `developer`
5. Enable OpenSSH server

### Step 5: Setup Development Environment

After Ubuntu boots, run:

```bash
# Download and run setup script
curl -fsSL https://raw.githubusercontent.com/srikommareddi/Storage-Appliance-Architecture/main/pstorage/scripts/setup-linux-vm.sh | bash

# Or manually:
git clone https://github.com/srikommareddi/Storage-Appliance-Architecture.git
cd Storage-Appliance-Architecture/pstorage
chmod +x scripts/setup-linux-vm.sh
./scripts/setup-linux-vm.sh
```

## Option 2: Docker (Limited - No Real NVMe)

Good for compilation testing, but can't access actual NVMe devices.

```bash
# Build image
docker build -t pstorage-linux-dev -f docker/Dockerfile.linux-dev .

# Run interactive shell
docker run -it --rm -v $(pwd):/app pstorage-linux-dev bash

# Inside container
go build ./powerscale/...
pytest tests/
```

## Option 3: VMware Fusion / Parallels

Same Ubuntu setup, but with commercial VM software.

| Feature | UTM | VMware Fusion | Parallels |
|---------|-----|---------------|-----------|
| Price | Free | $149+ | $99/year |
| Apple Silicon | ✅ Native | ✅ Native | ✅ Native |
| NVMe Passthrough | ⚠️ Emulated | ✅ Better | ✅ Better |
| Performance | Good | Better | Best |

## Testing NVMe in VM

### Check NVMe Device (inside VM)

```bash
# List NVMe devices
sudo nvme list

# Check NVMe controller
sudo nvme id-ctrl /dev/nvme0

# Test read performance
sudo fio --name=test --filename=/dev/nvme0n1 --direct=1 \
    --rw=randread --bs=4k --numjobs=1 --runtime=10
```

### Build Go NVMe Components

```bash
cd ~/pstorage/pstorage

# With SPDK installed:
export CGO_ENABLED=1
export CGO_CFLAGS="-I$HOME/spdk/include"
export CGO_LDFLAGS="-L$HOME/spdk/build/lib -lspdk"

go build ./nvmedrv/...
go build ./nvmeof/...
```

## SSH Access from macOS

```bash
# Get VM IP (inside VM)
ip addr show

# SSH from macOS
ssh developer@<vm-ip>

# Or setup port forwarding in UTM:
# Host: 2222 → Guest: 22
ssh -p 2222 developer@localhost
```

## Shared Folders

### UTM
1. VM Settings → Sharing → Enable Directory Sharing
2. Inside VM: `sudo mount -t 9p -o trans=virtio share /mnt/shared`

### Mount macOS folder in VM

```bash
# Install sshfs in VM
sudo apt install sshfs

# Mount macOS folder
sshfs user@host.local:/Users/srilakshmi/pstorage /mnt/pstorage
```

## Troubleshooting

### VM won't start
- Check RAM allocation (reduce if needed)
- Try "Emulate" instead of "Virtualize"

### No network
- Check UTM network mode (use "Shared Network")
- Verify: `ip addr show`

### Go build fails
- Ensure `go version` works
- Check `$GOPATH` is set
- For SPDK: verify library paths

### SPDK issues
- Run `sudo ./scripts/setup.sh` in SPDK dir
- Check hugepages: `cat /proc/meminfo | grep Huge`
