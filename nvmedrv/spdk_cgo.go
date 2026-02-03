//go:build cgo && spdk

package nvmedrv

/*
#cgo pkg-config: spdk

#include <stdlib.h>
#include <string.h>
#include <stdint.h>

#include <spdk/env.h>
#include <spdk/nvme.h>
#include <spdk/nvme_spec.h>

typedef struct {
    struct spdk_nvme_ctrlr *ctrlr;
    struct spdk_nvme_qpair *qpair;
    struct spdk_nvme_ns *ns;
    uint32_t nsid;
    uint32_t block_size;
    uint64_t num_blocks;
    uint64_t read_ops;
    uint64_t write_ops;
    uint64_t bytes_read;
    uint64_t bytes_written;
} nvme_spdk_ctrl;

typedef struct {
    int done;
    int status;
} nvme_spdk_completion;

static int nvme_spdk_initialized = 0;

static int nvme_spdk_init(void) {
    if (nvme_spdk_initialized) {
        return 0;
    }
    struct spdk_env_opts opts;
    spdk_env_opts_init(&opts);
    opts.name = "nvmedrv";
    if (spdk_env_init(&opts) < 0) {
        return -1;
    }
    nvme_spdk_initialized = 1;
    return 0;
}

static void nvme_spdk_completion_cb(void *arg, const struct spdk_nvme_cpl *cpl) {
    nvme_spdk_completion *comp = (nvme_spdk_completion*)arg;
    comp->done = 1;
    if (spdk_nvme_cpl_is_error(cpl)) {
        comp->status = -1;
    } else {
        comp->status = 0;
    }
}

static int nvme_spdk_poll_completion(struct spdk_nvme_qpair *qpair, nvme_spdk_completion *comp, uint32_t max_polls) {
    uint32_t polls = 0;
    while (!comp->done && polls < max_polls) {
        spdk_nvme_qpair_process_completions(qpair, 0);
        polls++;
    }
    return comp->status;
}

static nvme_spdk_ctrl* nvme_spdk_connect(const char* trtype,
                                         const char* traddr,
                                         const char* trsvcid,
                                         const char* subnqn,
                                         uint32_t nsid) {
    if (nvme_spdk_init() != 0) {
        return NULL;
    }

    struct spdk_nvme_transport_id trid = {};
    memset(&trid, 0, sizeof(trid));
    if (strcmp(trtype, "pcie") == 0) {
        trid.trtype = SPDK_NVME_TRANSPORT_PCIE;
        strncpy(trid.traddr, traddr, sizeof(trid.traddr) - 1);
    } else if (strcmp(trtype, "tcp") == 0) {
        trid.trtype = SPDK_NVME_TRANSPORT_TCP;
        strncpy(trid.traddr, traddr, sizeof(trid.traddr) - 1);
        strncpy(trid.trsvcid, trsvcid, sizeof(trid.trsvcid) - 1);
        if (subnqn && subnqn[0] != '\0') {
            strncpy(trid.subnqn, subnqn, sizeof(trid.subnqn) - 1);
        }
    } else if (strcmp(trtype, "rdma") == 0) {
        trid.trtype = SPDK_NVME_TRANSPORT_RDMA;
        strncpy(trid.traddr, traddr, sizeof(trid.traddr) - 1);
        strncpy(trid.trsvcid, trsvcid, sizeof(trid.trsvcid) - 1);
        if (subnqn && subnqn[0] != '\0') {
            strncpy(trid.subnqn, subnqn, sizeof(trid.subnqn) - 1);
        }
    } else {
        return NULL;
    }

    struct spdk_nvme_ctrlr_opts opts;
    spdk_nvme_ctrlr_opts_init(&opts, sizeof(opts));

    struct spdk_nvme_ctrlr *ctrlr = spdk_nvme_connect(&trid, &opts, sizeof(opts));
    if (!ctrlr) {
        return NULL;
    }

    struct spdk_nvme_qpair *qpair = spdk_nvme_ctrlr_alloc_io_qpair(ctrlr, NULL, 0);
    if (!qpair) {
        spdk_nvme_detach(ctrlr);
        return NULL;
    }

    uint32_t ns_count = spdk_nvme_ctrlr_get_num_ns(ctrlr);
    if (ns_count == 0) {
        spdk_nvme_ctrlr_free_io_qpair(qpair);
        spdk_nvme_detach(ctrlr);
        return NULL;
    }

    uint32_t use_nsid = nsid == 0 ? 1 : nsid;
    struct spdk_nvme_ns *ns = spdk_nvme_ctrlr_get_ns(ctrlr, use_nsid);
    if (!ns) {
        spdk_nvme_ctrlr_free_io_qpair(qpair);
        spdk_nvme_detach(ctrlr);
        return NULL;
    }

    nvme_spdk_ctrl *handle = (nvme_spdk_ctrl*)calloc(1, sizeof(nvme_spdk_ctrl));
    if (!handle) {
        spdk_nvme_ctrlr_free_io_qpair(qpair);
        spdk_nvme_detach(ctrlr);
        return NULL;
    }

    handle->ctrlr = ctrlr;
    handle->qpair = qpair;
    handle->ns = ns;
    handle->nsid = use_nsid;
    handle->block_size = spdk_nvme_ns_get_sector_size(ns);
    handle->num_blocks = spdk_nvme_ns_get_num_sectors(ns);
    return handle;
}

static void nvme_spdk_disconnect(nvme_spdk_ctrl* ctrl) {
    if (!ctrl) {
        return;
    }
    if (ctrl->qpair) {
        spdk_nvme_ctrlr_free_io_qpair(ctrl->qpair);
    }
    if (ctrl->ctrlr) {
        spdk_nvme_detach(ctrl->ctrlr);
    }
    free(ctrl);
}

static int nvme_spdk_read(nvme_spdk_ctrl* ctrl, uint64_t lba, uint32_t num_blocks,
                          void* buffer, uint64_t buffer_len) {
    if (!ctrl || !buffer) {
        return -1;
    }
    uint64_t expected = (uint64_t)num_blocks * (uint64_t)ctrl->block_size;
    if (expected > buffer_len) {
        return -2;
    }

    nvme_spdk_completion comp = {0};
    int rc = spdk_nvme_ns_cmd_read(ctrl->ns, ctrl->qpair, buffer, lba, num_blocks,
                                   nvme_spdk_completion_cb, &comp, 0);
    if (rc != 0) {
        return -3;
    }
    if (nvme_spdk_poll_completion(ctrl->qpair, &comp, 100000) != 0) {
        return -4;
    }
    ctrl->read_ops += 1;
    ctrl->bytes_read += expected;
    return 0;
}

static int nvme_spdk_write(nvme_spdk_ctrl* ctrl, uint64_t lba, uint32_t num_blocks,
                           const void* buffer, uint64_t buffer_len) {
    if (!ctrl || !buffer) {
        return -1;
    }
    uint64_t expected = (uint64_t)num_blocks * (uint64_t)ctrl->block_size;
    if (expected > buffer_len) {
        return -2;
    }

    nvme_spdk_completion comp = {0};
    int rc = spdk_nvme_ns_cmd_write(ctrl->ns, ctrl->qpair, (void*)buffer, lba, num_blocks,
                                    nvme_spdk_completion_cb, &comp, 0);
    if (rc != 0) {
        return -3;
    }
    if (nvme_spdk_poll_completion(ctrl->qpair, &comp, 100000) != 0) {
        return -4;
    }
    ctrl->write_ops += 1;
    ctrl->bytes_written += expected;
    return 0;
}

static void nvme_spdk_stats(nvme_spdk_ctrl* ctrl, uint64_t* read_ops, uint64_t* write_ops,
                            uint64_t* bytes_read, uint64_t* bytes_written,
                            uint32_t* block_size, uint64_t* num_blocks) {
    if (!ctrl) {
        return;
    }
    if (read_ops) *read_ops = ctrl->read_ops;
    if (write_ops) *write_ops = ctrl->write_ops;
    if (bytes_read) *bytes_read = ctrl->bytes_read;
    if (bytes_written) *bytes_written = ctrl->bytes_written;
    if (block_size) *block_size = ctrl->block_size;
    if (num_blocks) *num_blocks = ctrl->num_blocks;
}
*/
import "C"
import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// PCIeController implements an NVMe PCIe driver wrapper backed by SPDK.
type PCIeController struct {
	cfg    ControllerConfig
	handle *C.nvme_spdk_ctrl
	queues *queueManager
	mu     sync.Mutex
}

// FabricsController implements an NVMe-oF driver wrapper backed by SPDK.
type FabricsController struct {
	cfg       ControllerConfig
	handle    *C.nvme_spdk_ctrl
	queues    *queueManager
	namespace uint32
	mu        sync.Mutex
}

// NewPCIeController creates a PCIe NVMe controller instance using SPDK.
func NewPCIeController(cfg ControllerConfig) (*PCIeController, error) {
	if cfg.PCIAddress == "" {
		return nil, fmt.Errorf("pci address is required for PCIe transport")
	}

	queueCount := cfg.Queue.IOQueues
	if queueCount <= 0 {
		queueCount = 4
	}

	cTrtype := C.CString("pcie")
	defer C.free(unsafe.Pointer(cTrtype))
	cAddr := C.CString(cfg.PCIAddress)
	defer C.free(unsafe.Pointer(cAddr))

	handle := C.nvme_spdk_connect(cTrtype, cAddr, nil, nil, 1)
	if handle == nil {
		return nil, fmt.Errorf("spdk connect failed for PCIe")
	}

	return &PCIeController{
		cfg:    cfg,
		handle: handle,
		queues: newQueueManager(queueCount),
	}, nil
}

// NewFabricsController creates a fabrics-based controller using SPDK.
func NewFabricsController(cfg ControllerConfig) (*FabricsController, error) {
	if cfg.Address == "" {
		return nil, fmt.Errorf("address is required for fabrics transport")
	}
	if cfg.HostNQN == "" || cfg.SubsystemNQN == "" {
		return nil, fmt.Errorf("host and subsystem NQN are required")
	}

	trtype := ""
	switch cfg.Transport {
	case TransportTCP:
		trtype = "tcp"
	case TransportRoCE:
		trtype = "rdma"
	case TransportFC:
		return nil, fmt.Errorf("fc-nvme not wired in spdk wrapper yet")
	default:
		return nil, fmt.Errorf("unsupported fabrics transport: %s", cfg.Transport)
	}

	host, port, err := splitHostPort(cfg.Address)
	if err != nil {
		return nil, err
	}

	queueCount := cfg.Queue.IOQueues
	if queueCount <= 0 {
		queueCount = 4
	}

	cTrtype := C.CString(trtype)
	defer C.free(unsafe.Pointer(cTrtype))
	cAddr := C.CString(host)
	defer C.free(unsafe.Pointer(cAddr))
	cSvc := C.CString(port)
	defer C.free(unsafe.Pointer(cSvc))
	cSub := C.CString(cfg.SubsystemNQN)
	defer C.free(unsafe.Pointer(cSub))

	nsid := cfg.NamespaceID
	if nsid == 0 {
		nsid = 1
	}

	handle := C.nvme_spdk_connect(cTrtype, cAddr, cSvc, cSub, C.uint32_t(nsid))
	if handle == nil {
		return nil, fmt.Errorf("spdk connect failed for fabrics")
	}

	return &FabricsController{
		cfg:       cfg,
		handle:    handle,
		queues:    newQueueManager(queueCount),
		namespace: nsid,
	}, nil
}

// Identify returns controller info for PCIe.
func (pc *PCIeController) Identify(_ time.Duration) (*ControllerInfo, error) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	return spdkIdentify(pc.handle, pc.queues.count, pc.cfg.Queue.IOQueueDepth, "nvme-pcie-spdk", pc.cfg.PCIAddress)
}

// Read performs a PCIe NVMe read.
func (pc *PCIeController) Read(lba uint64, numBlocks uint32, buffer []byte) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	return spdkRead(pc.handle, lba, numBlocks, buffer)
}

// Write performs a PCIe NVMe write.
func (pc *PCIeController) Write(lba uint64, numBlocks uint32, buffer []byte) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	return spdkWrite(pc.handle, lba, numBlocks, buffer)
}

// Flush flushes pending writes if supported.
func (pc *PCIeController) Flush() error {
	return nil
}

// Stats returns aggregated statistics.
func (pc *PCIeController) Stats() IOStats {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	return spdkStats(pc.handle, pc.queues.count)
}

// Close closes the controller.
func (pc *PCIeController) Close() error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if pc.handle != nil {
		C.nvme_spdk_disconnect(pc.handle)
		pc.handle = nil
	}
	return nil
}

// Identify returns controller info for fabrics.
func (fc *FabricsController) Identify(_ time.Duration) (*ControllerInfo, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	return spdkIdentify(fc.handle, fc.queues.count, fc.cfg.Queue.IOQueueDepth, "nvme-of-spdk", fc.cfg.SubsystemNQN)
}

// Read performs an NVMe-oF read.
func (fc *FabricsController) Read(lba uint64, numBlocks uint32, buffer []byte) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	_ = fc.queues.next()
	return spdkRead(fc.handle, lba, numBlocks, buffer)
}

// Write performs an NVMe-oF write.
func (fc *FabricsController) Write(lba uint64, numBlocks uint32, buffer []byte) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	_ = fc.queues.next()
	return spdkWrite(fc.handle, lba, numBlocks, buffer)
}

// Flush flushes pending writes if supported.
func (fc *FabricsController) Flush() error {
	return nil
}

// Stats returns aggregated statistics.
func (fc *FabricsController) Stats() IOStats {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	return spdkStats(fc.handle, fc.queues.count)
}

// Close closes the underlying controller.
func (fc *FabricsController) Close() error {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	if fc.handle != nil {
		C.nvme_spdk_disconnect(fc.handle)
		fc.handle = nil
	}
	return nil
}

func spdkIdentify(handle *C.nvme_spdk_ctrl, queueCount int, queueDepth uint16, model string, serial string) (*ControllerInfo, error) {
	var readOps, writeOps, bytesRead, bytesWritten C.uint64_t
	var blockSize C.uint32_t
	var numBlocks C.uint64_t
	C.nvme_spdk_stats(handle, &readOps, &writeOps, &bytesRead, &bytesWritten, &blockSize, &numBlocks)

	info := &ControllerInfo{
		Model:         model,
		Serial:        serial,
		Firmware:      "spdk",
		NamespaceCount: 1,
		BlockSize:     uint32(blockSize),
		MaxQueues:     queueCount,
		MaxQueueDepth: queueDepth,
	}

	if info.BlockSize == 0 {
		info.BlockSize = defaultBlockSize
	}
	if info.MaxQueueDepth == 0 {
		info.MaxQueueDepth = 128
	}
	return info, nil
}

func spdkRead(handle *C.nvme_spdk_ctrl, lba uint64, numBlocks uint32, buffer []byte) error {
	if len(buffer) == 0 {
		return fmt.Errorf("buffer is empty")
	}
	ret := C.nvme_spdk_read(handle, C.uint64_t(lba), C.uint32_t(numBlocks),
		unsafe.Pointer(&buffer[0]), C.uint64_t(len(buffer)))
	if ret != 0 {
		return fmt.Errorf("spdk read failed: %d", ret)
	}
	return nil
}

func spdkWrite(handle *C.nvme_spdk_ctrl, lba uint64, numBlocks uint32, buffer []byte) error {
	if len(buffer) == 0 {
		return fmt.Errorf("buffer is empty")
	}
	ret := C.nvme_spdk_write(handle, C.uint64_t(lba), C.uint32_t(numBlocks),
		unsafe.Pointer(&buffer[0]), C.uint64_t(len(buffer)))
	if ret != 0 {
		return fmt.Errorf("spdk write failed: %d", ret)
	}
	return nil
}

func spdkStats(handle *C.nvme_spdk_ctrl, queueCount int) IOStats {
	var readOps, writeOps, bytesRead, bytesWritten C.uint64_t
	var blockSize C.uint32_t
	var numBlocks C.uint64_t
	C.nvme_spdk_stats(handle, &readOps, &writeOps, &bytesRead, &bytesWritten, &blockSize, &numBlocks)

	return IOStats{
		ReadOps:      uint64(readOps),
		WriteOps:     uint64(writeOps),
		BytesRead:    uint64(bytesRead),
		BytesWritten: uint64(bytesWritten),
		QueueCount:   queueCount,
	}
}

func splitHostPort(addr string) (string, string, error) {
	parts := strings.Split(addr, ":")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("address must be host:port")
	}
	return parts[0], parts[1], nil
}
