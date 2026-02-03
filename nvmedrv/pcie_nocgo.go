//go:build !cgo

package nvmedrv

import (
	"fmt"
	"time"
)

// PCIeController implements an NVMe PCIe driver wrapper (CGO required).
type PCIeController struct {
	cfg ControllerConfig
}

// NewPCIeController creates a PCIe NVMe controller instance.
func NewPCIeController(cfg ControllerConfig) (*PCIeController, error) {
	return nil, fmt.Errorf("PCIe driver requires CGO enabled")
}

// Identify returns best-effort controller info.
func (pc *PCIeController) Identify(_ time.Duration) (*ControllerInfo, error) {
	return nil, fmt.Errorf("PCIe driver requires CGO enabled")
}

// Read performs a PCIe NVMe read.
func (pc *PCIeController) Read(lba uint64, numBlocks uint32, buffer []byte) error {
	return fmt.Errorf("PCIe driver requires CGO enabled")
}

// Write performs a PCIe NVMe write.
func (pc *PCIeController) Write(lba uint64, numBlocks uint32, buffer []byte) error {
	return fmt.Errorf("PCIe driver requires CGO enabled")
}

// Flush flushes pending writes if supported.
func (pc *PCIeController) Flush() error {
	return fmt.Errorf("PCIe driver requires CGO enabled")
}

// Stats returns aggregated statistics.
func (pc *PCIeController) Stats() IOStats {
	return IOStats{}
}

// Close closes the controller.
func (pc *PCIeController) Close() error {
	return nil
}
