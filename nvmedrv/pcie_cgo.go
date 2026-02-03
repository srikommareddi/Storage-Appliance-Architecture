//go:build cgo && !spdk

package nvmedrv

/*
#include <stdlib.h>
#include "driver.h"
*/
import "C"
import (
	"fmt"
	"sync"
	"time"
	"unsafe"
)

// PCIeController implements an NVMe PCIe driver wrapper backed by C.
type PCIeController struct {
	cfg    ControllerConfig
	handle *C.nvme_pcie_ctrl
	queues *queueManager
	mu     sync.Mutex
}

// NewPCIeController creates a PCIe NVMe controller instance.
func NewPCIeController(cfg ControllerConfig) (*PCIeController, error) {
	if cfg.PCIAddress == "" {
		return nil, fmt.Errorf("pci address is required for PCIe transport")
	}

	if !cfg.AllowSoftwareFallback {
		return nil, fmt.Errorf("hardware controller not available in C driver")
	}

	queueCount := cfg.Queue.IOQueues
	if queueCount <= 0 {
		queueCount = 4
	}

	blockSize := cfg.BlockSize
	if blockSize == 0 {
		blockSize = defaultBlockSize
	}

	size := cfg.EmulatedSizeBytes
	if size == 0 {
		size = defaultEmulatedSize
	}

	cAddr := C.CString(cfg.PCIAddress)
	defer C.free(unsafe.Pointer(cAddr))

	handle := C.nvme_pcie_create(cAddr, C.uint32_t(blockSize), C.uint64_t(size))
	if handle == nil {
		return nil, fmt.Errorf("failed to create PCIe controller")
	}

	C.nvme_pcie_queue_init(handle, C.uint16_t(cfg.Queue.IOQueueDepth))

	return &PCIeController{
		cfg:    cfg,
		handle: handle,
		queues: newQueueManager(queueCount),
	}, nil
}

// Identify returns best-effort controller info.
func (pc *PCIeController) Identify(_ time.Duration) (*ControllerInfo, error) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	blockSize := pc.cfg.BlockSize
	if blockSize == 0 {
		blockSize = defaultBlockSize
	}

	ctrlBuf := make([]byte, 4096)
	if ret := C.nvme_pcie_identify(pc.handle, 0, C.uint8_t(0x01), unsafe.Pointer(&ctrlBuf[0]), C.uint64_t(len(ctrlBuf))); ret == 0 {
		nn := *(*uint32)(unsafe.Pointer(&ctrlBuf[516]))
		if nn > 0 {
			// Use NN if provided
			_ = nn
		}
	}

	nsBuf := make([]byte, 4096)
	if ret := C.nvme_pcie_identify(pc.handle, 1, C.uint8_t(0x00), unsafe.Pointer(&nsBuf[0]), C.uint64_t(len(nsBuf))); ret == 0 {
		lbads := nsBuf[128]
		if lbads > 0 && lbads < 32 {
			blockSize = 1 << lbads
		}
	}

	info := &ControllerInfo{
		Model:         "nvme-pcie-c",
		Serial:        pc.cfg.PCIAddress,
		Firmware:      "emulated",
		NamespaceCount: 1,
		BlockSize:     blockSize,
		MaxQueues:     pc.queues.count,
		MaxQueueDepth: pc.cfg.Queue.IOQueueDepth,
	}

	if info.MaxQueueDepth == 0 {
		info.MaxQueueDepth = 128
	}

	return info, nil
}

// Read performs a PCIe NVMe read.
func (pc *PCIeController) Read(lba uint64, numBlocks uint32, buffer []byte) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	blockSize := pc.cfg.BlockSize
	if blockSize == 0 {
		blockSize = defaultBlockSize
	}
	expected := uint64(numBlocks) * uint64(blockSize)
	if uint64(len(buffer)) < expected {
		return fmt.Errorf("buffer too small for read")
	}

	ret := C.nvme_pcie_read(pc.handle, C.uint64_t(lba), C.uint32_t(numBlocks),
		unsafe.Pointer(&buffer[0]), C.uint64_t(len(buffer)))
	if ret != 0 {
		return fmt.Errorf("read failed: %d", ret)
	}
	return nil
}

// Write performs a PCIe NVMe write.
func (pc *PCIeController) Write(lba uint64, numBlocks uint32, buffer []byte) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	blockSize := pc.cfg.BlockSize
	if blockSize == 0 {
		blockSize = defaultBlockSize
	}
	expected := uint64(numBlocks) * uint64(blockSize)
	if uint64(len(buffer)) < expected {
		return fmt.Errorf("buffer too small for write")
	}

	ret := C.nvme_pcie_write(pc.handle, C.uint64_t(lba), C.uint32_t(numBlocks),
		unsafe.Pointer(&buffer[0]), C.uint64_t(len(buffer)))
	if ret != 0 {
		return fmt.Errorf("write failed: %d", ret)
	}
	return nil
}

// Flush flushes pending writes if supported.
func (pc *PCIeController) Flush() error {
	return nil
}

// Stats returns aggregated statistics.
func (pc *PCIeController) Stats() IOStats {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	var stats C.nvme_stats
	C.nvme_pcie_stats(pc.handle, &stats)

	return IOStats{
		ReadOps:      uint64(stats.read_ops),
		WriteOps:     uint64(stats.write_ops),
		BytesRead:    uint64(stats.bytes_read),
		BytesWritten: uint64(stats.bytes_written),
		QueueCount:   pc.queues.count,
	}
}

// Close closes the controller.
func (pc *PCIeController) Close() error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if pc.handle != nil {
		C.nvme_pcie_destroy(pc.handle)
		pc.handle = nil
	}
	return nil
}
