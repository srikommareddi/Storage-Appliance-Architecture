// +build !cgo

package cgobridge

import "fmt"

// NVMeHardwareController software fallback (when CGO is disabled)
type NVMeHardwareController struct {
	pciAddr string
}

// HardwareStats represents hardware statistics
type HardwareStats struct {
	ControllerID uint16
	IOCount      uint64
	BytesRead    uint64
	BytesWritten uint64
}

// NewNVMeHardwareController creates a software fallback controller
func NewNVMeHardwareController(pciAddr string) (*NVMeHardwareController, error) {
	return &NVMeHardwareController{
		pciAddr: pciAddr,
	}, nil
}

// ReadBlocks software implementation
func (nc *NVMeHardwareController) ReadBlocks(nsid uint32, lba uint64, numBlocks uint32, buffer []byte) error {
	return fmt.Errorf("hardware acceleration not available (CGO disabled)")
}

// WriteBlocks software implementation
func (nc *NVMeHardwareController) WriteBlocks(nsid uint32, lba uint64, numBlocks uint32, buffer []byte) error {
	return fmt.Errorf("hardware acceleration not available (CGO disabled)")
}

// GetStats software implementation
func (nc *NVMeHardwareController) GetStats() (*HardwareStats, error) {
	return &HardwareStats{}, fmt.Errorf("hardware acceleration not available (CGO disabled)")
}

// Close software implementation
func (nc *NVMeHardwareController) Close() error {
	return nil
}
