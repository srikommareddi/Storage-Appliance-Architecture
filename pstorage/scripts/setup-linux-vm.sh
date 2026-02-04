#!/bin/bash
#
# Linux VM Setup Script for PStorage Go/SPDK Development
#
# Run this script INSIDE a fresh Ubuntu 22.04 VM (UTM, VMware, etc.)
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/srikommareddi/Storage-Appliance-Architecture/main/pstorage/scripts/setup-linux-vm.sh | bash
#
# Or manually:
#   chmod +x setup-linux-vm.sh
#   ./setup-linux-vm.sh
#

set -e

echo "=============================================="
echo "  PStorage Linux Development Environment Setup"
echo "=============================================="
echo ""

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# Check if running as root
if [ "$EUID" -eq 0 ]; then
    log_error "Please run as a regular user, not root"
    exit 1
fi

# Update system
log_info "Updating system packages..."
sudo apt-get update
sudo apt-get upgrade -y

# Install base development tools
log_info "Installing development tools..."
sudo apt-get install -y \
    build-essential \
    git \
    curl \
    wget \
    pkg-config \
    python3 \
    python3-pip \
    python3-venv \
    vim \
    htop \
    tree

# Install Go
GO_VERSION="1.21.6"
log_info "Installing Go ${GO_VERSION}..."

if [ ! -d "/usr/local/go" ]; then
    ARCH=$(dpkg --print-architecture)
    if [ "$ARCH" = "arm64" ]; then
        GO_ARCH="linux-arm64"
    else
        GO_ARCH="linux-amd64"
    fi

    wget -q "https://go.dev/dl/go${GO_VERSION}.${GO_ARCH}.tar.gz"
    sudo tar -C /usr/local -xzf "go${GO_VERSION}.${GO_ARCH}.tar.gz"
    rm "go${GO_VERSION}.${GO_ARCH}.tar.gz"
fi

# Add Go to PATH
if ! grep -q "GOPATH" ~/.bashrc; then
    cat >> ~/.bashrc << 'EOF'

# Go environment
export PATH="/usr/local/go/bin:$PATH"
export GOPATH="$HOME/go"
export PATH="$GOPATH/bin:$PATH"
EOF
fi

export PATH="/usr/local/go/bin:$PATH"
export GOPATH="$HOME/go"
export PATH="$GOPATH/bin:$PATH"

log_info "Go version: $(go version)"

# Install SPDK dependencies
log_info "Installing SPDK dependencies..."
sudo apt-get install -y \
    libaio-dev \
    libcunit1-dev \
    libnuma-dev \
    libssl-dev \
    libfuse3-dev \
    uuid-dev \
    meson \
    ninja-build \
    nasm \
    libncurses5-dev \
    libncursesw5-dev

# Install SPDK (optional - uncomment if needed)
install_spdk() {
    log_info "Installing SPDK..."

    SPDK_DIR="$HOME/spdk"

    if [ ! -d "$SPDK_DIR" ]; then
        git clone https://github.com/spdk/spdk "$SPDK_DIR"
        cd "$SPDK_DIR"
        git submodule update --init

        # Install additional dependencies
        sudo ./scripts/pkgdep.sh

        # Configure and build
        ./configure --with-shared
        make -j$(nproc)

        log_info "SPDK installed at $SPDK_DIR"
    else
        log_warn "SPDK already exists at $SPDK_DIR"
    fi
}

# Uncomment to install SPDK:
# install_spdk

# Clone PStorage repository
log_info "Setting up PStorage..."

PSTORAGE_DIR="$HOME/pstorage"

if [ ! -d "$PSTORAGE_DIR" ]; then
    git clone https://github.com/srikommareddi/Storage-Appliance-Architecture.git "$PSTORAGE_DIR"
fi

cd "$PSTORAGE_DIR/pstorage"

# Setup Python environment
log_info "Setting up Python environment..."
python3 -m venv venv
source venv/bin/activate
pip install --upgrade pip
pip install -e ".[dev]" || log_warn "Python install had warnings (may be OK)"

# Build Go components
log_info "Building Go components..."
cd "$PSTORAGE_DIR/pstorage"

# Build what we can without SPDK
go build ./powerscale/... && log_info "powerscale: OK" || log_warn "powerscale: Failed"

# These require SPDK stubs or will fail
go build ./nvmeof/... 2>/dev/null && log_info "nvmeof: OK" || log_warn "nvmeof: Skipped (needs SPDK)"
go build ./nvmedrv/... 2>/dev/null && log_info "nvmedrv: OK" || log_warn "nvmedrv: Skipped (needs SPDK)"
go build ./dpu/... 2>/dev/null && log_info "dpu: OK" || log_warn "dpu: Skipped (needs DOCA)"

# Run tests
log_info "Running Python tests..."
cd "$PSTORAGE_DIR/pstorage"
source venv/bin/activate
pytest tests/ -v --tb=short || log_warn "Some tests may have failed"

# Summary
echo ""
echo "=============================================="
echo "  Setup Complete!"
echo "=============================================="
echo ""
echo "Environment:"
echo "  - Go: $(go version)"
echo "  - Python: $(python3 --version)"
echo "  - Project: $PSTORAGE_DIR/pstorage"
echo ""
echo "Quick Start:"
echo "  cd $PSTORAGE_DIR/pstorage"
echo "  source venv/bin/activate"
echo "  pytest                           # Run tests"
echo "  uvicorn pstorage.api.main:app    # Start API"
echo ""
echo "To install SPDK (for full NVMe support):"
echo "  Edit this script and uncomment 'install_spdk'"
echo "  Or run: git clone https://github.com/spdk/spdk && cd spdk && sudo ./scripts/pkgdep.sh && ./configure && make"
echo ""
log_info "Reload shell: source ~/.bashrc"
