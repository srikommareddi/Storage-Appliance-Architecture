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

// FabricsController implements an NVMe-oF driver wrapper backed by C.
type FabricsController struct {
	cfg       ControllerConfig
	handle    *C.nvme_fabrics_ctrl
	queues    *queueManager
	namespace uint32
	mu        sync.Mutex
}

// NewFabricsController creates a fabrics-based controller (TCP/RDMA/FC).
func NewFabricsController(cfg ControllerConfig) (*FabricsController, error) {
	if cfg.Address == "" {
		return nil, fmt.Errorf("address is required for fabrics transport")
	}
	if cfg.HostNQN == "" || cfg.SubsystemNQN == "" {
		return nil, fmt.Errorf("host and subsystem NQN are required")
	}

	switch cfg.Transport {
	case TransportTCP:
		// Supported (emulated)
	case TransportRoCE, TransportFC:
		return nil, fmt.Errorf("transport %s not implemented; use TCP", cfg.Transport)
	default:
		return nil, fmt.Errorf("unsupported fabrics transport: %s", cfg.Transport)
	}

	if !cfg.AllowSoftwareFallback {
		return nil, fmt.Errorf("hardware fabrics path not available in C driver")
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

	cAddr := C.CString(cfg.Address)
	defer C.free(unsafe.Pointer(cAddr))
	cHost := C.CString(cfg.HostNQN)
	defer C.free(unsafe.Pointer(cHost))
	cSubsys := C.CString(cfg.SubsystemNQN)
	defer C.free(unsafe.Pointer(cSubsys))

	handle := C.nvme_fabrics_create(cAddr, cHost, cSubsys, C.uint32_t(blockSize), C.uint64_t(size))
	if handle == nil {
		return nil, fmt.Errorf("failed to create fabrics controller")
	}

	var ctrlID C.uint16_t
	if ret := C.nvme_fabrics_connect(handle, C.uint16_t(0), C.uint16_t(cfg.Queue.IOQueueDepth), &ctrlID); ret != 0 {
		C.nvme_fabrics_destroy(handle)
		return nil, fmt.Errorf("fabrics connect failed: %d", ret)
	}

	C.nvme_fabrics_queue_init(handle, C.uint16_t(cfg.Queue.IOQueueDepth))

	nsid := cfg.NamespaceID
	if nsid == 0 {
		nsid = 1
	}

	return &FabricsController{
		cfg:       cfg,
		handle:    handle,
		queues:    newQueueManager(queueCount),
		namespace: nsid,
	}, nil
}

// Identify returns best-effort controller info for fabrics.
func (fc *FabricsController) Identify(_ time.Duration) (*ControllerInfo, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	blockSize := fc.cfg.BlockSize
	if blockSize == 0 {
		blockSize = defaultBlockSize
	}

	ctrlBuf := make([]byte, 4096)
	if ret := C.nvme_fabrics_identify(fc.handle, 0, C.uint8_t(0x01), unsafe.Pointer(&ctrlBuf[0]), C.uint64_t(len(ctrlBuf))); ret == 0 {
		nn := *(*uint32)(unsafe.Pointer(&ctrlBuf[516]))
		if nn > 0 {
			_ = nn
		}
	}

	nsBuf := make([]byte, 4096)
	if ret := C.nvme_fabrics_identify(fc.handle, 1, C.uint8_t(0x00), unsafe.Pointer(&nsBuf[0]), C.uint64_t(len(nsBuf))); ret == 0 {
		lbads := nsBuf[128]
		if lbads > 0 && lbads < 32 {
			blockSize = 1 << lbads
		}
	}

	info := &ControllerInfo{
		Model:         "nvme-of-c",
		Serial:        fc.cfg.SubsystemNQN,
		Firmware:      "emulated",
		NamespaceCount: 1,
		BlockSize:     blockSize,
		MaxQueues:     fc.queues.count,
		MaxQueueDepth: fc.cfg.Queue.IOQueueDepth,
	}

	if info.MaxQueueDepth == 0 {
		info.MaxQueueDepth = 128
	}

	return info, nil
}

// Read performs an NVMe-oF read using emulated backend.
func (fc *FabricsController) Read(lba uint64, numBlocks uint32, buffer []byte) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	_ = fc.queues.next()

	blockSize := fc.cfg.BlockSize
	if blockSize == 0 {
		blockSize = defaultBlockSize
	}
	expected := uint64(numBlocks) * uint64(blockSize)
	if uint64(len(buffer)) < expected {
		return fmt.Errorf("buffer too small for read")
	}

	ret := C.nvme_fabrics_read(fc.handle, C.uint64_t(lba), C.uint32_t(numBlocks),
		unsafe.Pointer(&buffer[0]), C.uint64_t(len(buffer)))
	if ret != 0 {
		return fmt.Errorf("read failed: %d", ret)
	}
	return nil
}

// Write performs an NVMe-oF write using emulated backend.
func (fc *FabricsController) Write(lba uint64, numBlocks uint32, buffer []byte) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	_ = fc.queues.next()

	blockSize := fc.cfg.BlockSize
	if blockSize == 0 {
		blockSize = defaultBlockSize
	}
	expected := uint64(numBlocks) * uint64(blockSize)
	if uint64(len(buffer)) < expected {
		return fmt.Errorf("buffer too small for write")
	}

	ret := C.nvme_fabrics_write(fc.handle, C.uint64_t(lba), C.uint32_t(numBlocks),
		unsafe.Pointer(&buffer[0]), C.uint64_t(len(buffer)))
	if ret != 0 {
		return fmt.Errorf("write failed: %d", ret)
	}
	return nil
}

// Flush flushes pending writes if supported.
func (fc *FabricsController) Flush() error {
	return nil
}

// Stats returns aggregated statistics.
func (fc *FabricsController) Stats() IOStats {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	var stats C.nvme_stats
	C.nvme_fabrics_stats(fc.handle, &stats)

	return IOStats{
		ReadOps:      uint64(stats.read_ops),
		WriteOps:     uint64(stats.write_ops),
		BytesRead:    uint64(stats.bytes_read),
		BytesWritten: uint64(stats.bytes_written),
		QueueCount:   fc.queues.count,
	}
}

// Close closes the underlying controller.
func (fc *FabricsController) Close() error {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	if fc.handle != nil {
		C.nvme_fabrics_destroy(fc.handle)
		fc.handle = nil
	}
	return nil
}
