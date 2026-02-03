package nvmedrv

import (
	"fmt"
	"time"
)

// TransportType defines the NVMe transport in use.
type TransportType string

const (
	TransportPCIe TransportType = "pcie"
	TransportTCP  TransportType = "tcp"
	TransportRoCE TransportType = "roce"
	TransportFC   TransportType = "fc"
)

// QueueConfig controls admin and I/O queue sizing.
type QueueConfig struct {
	AdminQueueDepth      uint16
	IOQueueDepth         uint16
	IOQueues             int
	UseSharedCompletion  bool
}

// MSIXConfig describes MSI-X vector mapping.
type MSIXConfig struct {
	Enabled    bool
	Vectors    int
	VectorCPUs map[int]int
}

// NUMAPolicy configures NUMA affinity for queues and interrupts.
type NUMAPolicy struct {
	Mode          string
	PreferredNode int
	AllowedNodes  []int
	CPUAffinity   []int
}

// ControllerConfig is the primary configuration for NVMe controllers.
type ControllerConfig struct {
	Transport             TransportType
	Address               string // fabric address for TCP/RDMA/FC
	HostNQN               string
	SubsystemNQN          string
	PCIAddress            string // PCIe BDF address
	NamespaceID           uint32
	BlockSize             uint32
	Queue                 QueueConfig
	MSIX                  MSIXConfig
	NUMA                  NUMAPolicy
	Multipath             bool
	MaxInflight           int
	KeepAlive             time.Duration
	Timeout               time.Duration
	AllowSoftwareFallback bool
	EmulatedSizeBytes     uint64
}

// ControllerInfo provides basic identify data.
type ControllerInfo struct {
	Model         string
	Serial        string
	Firmware      string
	NamespaceCount int
	BlockSize     uint32
	MaxQueues     int
	MaxQueueDepth uint16
}

// IOStats captures aggregated I/O metrics.
type IOStats struct {
	ReadOps      uint64
	WriteOps     uint64
	BytesRead    uint64
	BytesWritten uint64
	QueueCount   int
}

// NVMeController is a common driver interface.
type NVMeController interface {
	Identify(timeout time.Duration) (*ControllerInfo, error)
	Read(lba uint64, numBlocks uint32, buffer []byte) error
	Write(lba uint64, numBlocks uint32, buffer []byte) error
	Flush() error
	Stats() IOStats
	Close() error
}

// NewController constructs a controller based on transport config.
func NewController(cfg ControllerConfig) (NVMeController, error) {
	switch cfg.Transport {
	case TransportPCIe:
		return NewPCIeController(cfg)
	case TransportTCP, TransportRoCE, TransportFC:
		return NewFabricsController(cfg)
	default:
		return nil, fmt.Errorf("unsupported transport: %s", cfg.Transport)
	}
}
