// +build cgo

package cgobridge

/*
#cgo CXXFLAGS: -std=c++17 -I../cpp-legacy
#cgo LDFLAGS: -lstdc++ -lspdk -lnvme

#include <stdint.h>
#include <stdlib.h>

// Forward declarations for C++ functions
// These would link to actual C++ implementations in cpp-legacy/

typedef struct {
    uint16_t controller_id;
    uint64_t io_count;
    uint64_t bytes_read;
    uint64_t bytes_written;
} ControllerStats;

typedef struct {
    void* handle;
} NVMeControllerHandle;

// Stub implementations (would link to actual C++ code)
static inline NVMeControllerHandle* nvme_init_controller(const char* pci_addr) {
    // Would call actual C++ NVMeController constructor
    return NULL;
}

static inline int nvme_read_blocks(NVMeControllerHandle* ctrl, uint32_t nsid,
                                   uint64_t lba, uint32_t num_blocks,
                                   void* buffer, uint32_t buffer_size) {
    // Would call actual C++ read method
    return -1; // Not implemented in stub
}

static inline int nvme_write_blocks(NVMeControllerHandle* ctrl, uint32_t nsid,
                                    uint64_t lba, uint32_t num_blocks,
                                    const void* buffer, uint32_t buffer_size) {
    // Would call actual C++ write method
    return -1; // Not implemented in stub
}

static inline void nvme_get_stats(NVMeControllerHandle* ctrl, ControllerStats* stats) {
    // Would call actual C++ stats method
    stats->controller_id = 0;
    stats->io_count = 0;
    stats->bytes_read = 0;
    stats->bytes_written = 0;
}

static inline void nvme_destroy_controller(NVMeControllerHandle* ctrl) {
    // Would call actual C++ destructor
}
*/
import "C"
import (
	"fmt"
	"unsafe"
)

// NVMeHardwareController wraps the C++ NVMe controller
type NVMeHardwareController struct {
	handle *C.NVMeControllerHandle
	pciAddr string
}

// HardwareStats represents hardware statistics
type HardwareStats struct {
	ControllerID uint16
	IOCount      uint64
	BytesRead    uint64
	BytesWritten uint64
}

// NewNVMeHardwareController creates a new hardware controller
func NewNVMeHardwareController(pciAddr string) (*NVMeHardwareController, error) {
	cAddr := C.CString(pciAddr)
	defer C.free(unsafe.Pointer(cAddr))

	handle := C.nvme_init_controller(cAddr)
	if handle == nil {
		return nil, fmt.Errorf("failed to initialize NVMe controller at %s", pciAddr)
	}

	return &NVMeHardwareController{
		handle:  handle,
		pciAddr: pciAddr,
	}, nil
}

// ReadBlocks reads blocks from the hardware
func (nc *NVMeHardwareController) ReadBlocks(nsid uint32, lba uint64, numBlocks uint32, buffer []byte) error {
	if nc.handle == nil {
		return fmt.Errorf("controller not initialized")
	}

	ret := C.nvme_read_blocks(
		nc.handle,
		C.uint32_t(nsid),
		C.uint64_t(lba),
		C.uint32_t(numBlocks),
		unsafe.Pointer(&buffer[0]),
		C.uint32_t(len(buffer)),
	)

	if ret != 0 {
		return fmt.Errorf("read failed with code: %d", ret)
	}

	return nil
}

// WriteBlocks writes blocks to the hardware
func (nc *NVMeHardwareController) WriteBlocks(nsid uint32, lba uint64, numBlocks uint32, buffer []byte) error {
	if nc.handle == nil {
		return fmt.Errorf("controller not initialized")
	}

	ret := C.nvme_write_blocks(
		nc.handle,
		C.uint32_t(nsid),
		C.uint64_t(lba),
		C.uint32_t(numBlocks),
		unsafe.Pointer(&buffer[0]),
		C.uint32_t(len(buffer)),
	)

	if ret != 0 {
		return fmt.Errorf("write failed with code: %d", ret)
	}

	return nil
}

// GetStats retrieves hardware statistics
func (nc *NVMeHardwareController) GetStats() (*HardwareStats, error) {
	if nc.handle == nil {
		return nil, fmt.Errorf("controller not initialized")
	}

	var cStats C.ControllerStats
	C.nvme_get_stats(nc.handle, &cStats)

	return &HardwareStats{
		ControllerID: uint16(cStats.controller_id),
		IOCount:      uint64(cStats.io_count),
		BytesRead:    uint64(cStats.bytes_read),
		BytesWritten: uint64(cStats.bytes_written),
	}, nil
}

// Close closes the hardware controller
func (nc *NVMeHardwareController) Close() error {
	if nc.handle != nil {
		C.nvme_destroy_controller(nc.handle)
		nc.handle = nil
	}
	return nil
}
