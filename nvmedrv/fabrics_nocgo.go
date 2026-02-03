//go:build !cgo

package nvmedrv

import (
	"fmt"
	"time"
)

// FabricsController implements an NVMe-oF driver wrapper (CGO required).
type FabricsController struct {
	cfg ControllerConfig
}

// NewFabricsController creates a fabrics-based controller (TCP/RDMA/FC).
func NewFabricsController(cfg ControllerConfig) (*FabricsController, error) {
	return nil, fmt.Errorf("fabrics driver requires CGO enabled")
}

// Identify returns best-effort controller info for fabrics.
func (fc *FabricsController) Identify(_ time.Duration) (*ControllerInfo, error) {
	return nil, fmt.Errorf("fabrics driver requires CGO enabled")
}

// Read performs an NVMe-oF read.
func (fc *FabricsController) Read(lba uint64, numBlocks uint32, buffer []byte) error {
	return fmt.Errorf("fabrics driver requires CGO enabled")
}

// Write performs an NVMe-oF write.
func (fc *FabricsController) Write(lba uint64, numBlocks uint32, buffer []byte) error {
	return fmt.Errorf("fabrics driver requires CGO enabled")
}

// Flush flushes pending writes if supported.
func (fc *FabricsController) Flush() error {
	return fmt.Errorf("fabrics driver requires CGO enabled")
}

// Stats returns aggregated statistics.
func (fc *FabricsController) Stats() IOStats {
	return IOStats{}
}

// Close closes the underlying controller.
func (fc *FabricsController) Close() error {
	return nil
}
