//go:build cgo && !spdk

package nvmedrv

/*
#include <stdlib.h>
#include <string.h>

#include "driver.h"

static nvme_emu_device* nvme_emu_create(uint64_t size_bytes, uint32_t block_size) {
    nvme_emu_device* dev = (nvme_emu_device*)calloc(1, sizeof(nvme_emu_device));
    if (!dev) {
        return NULL;
    }
    dev->data = (uint8_t*)calloc(1, size_bytes);
    if (!dev->data) {
        free(dev);
        return NULL;
    }
    dev->size_bytes = size_bytes;
    dev->block_size = block_size;
    return dev;
}

static void nvme_emu_destroy(nvme_emu_device* dev) {
    if (!dev) {
        return;
    }
    free(dev->data);
    free(dev);
}

static int nvme_emu_read(nvme_emu_device* dev, uint64_t lba, uint32_t num_blocks,
                         void* buffer, uint64_t buffer_len) {
    if (!dev || !buffer) {
        return -1;
    }
    uint64_t offset = lba * (uint64_t)dev->block_size;
    uint64_t length = (uint64_t)num_blocks * (uint64_t)dev->block_size;
    if (length > buffer_len) {
        return -2;
    }
    if (offset + length > dev->size_bytes) {
        return -3;
    }
    memcpy(buffer, dev->data + offset, length);
    return 0;
}

static int nvme_emu_write(nvme_emu_device* dev, uint64_t lba, uint32_t num_blocks,
                          const void* buffer, uint64_t buffer_len) {
    if (!dev || !buffer) {
        return -1;
    }
    uint64_t offset = lba * (uint64_t)dev->block_size;
    uint64_t length = (uint64_t)num_blocks * (uint64_t)dev->block_size;
    if (length > buffer_len) {
        return -2;
    }
    if (offset + length > dev->size_bytes) {
        return -3;
    }
    memcpy(dev->data + offset, buffer, length);
    return 0;
}

static uint8_t nvme_log2_u32(uint32_t value) {
    uint8_t shift = 0;
    while ((value >> shift) > 1 && shift < 31) {
        shift++;
    }
    return shift;
}

static void nvme_fill_identify_ctrl(nvme_emu_device* dev, void* buffer, uint64_t buffer_len) {
    if (!buffer || buffer_len < 4096) {
        return;
    }
    memset(buffer, 0, 4096);
    uint32_t nn = 1;
    memcpy((uint8_t*)buffer + 516, &nn, sizeof(uint32_t));
}

static void nvme_fill_identify_ns(nvme_emu_device* dev, void* buffer, uint64_t buffer_len) {
    if (!dev || !buffer || buffer_len < 4096) {
        return;
    }
    memset(buffer, 0, 4096);
    uint64_t nsze = dev->size_bytes / dev->block_size;
    memcpy((uint8_t*)buffer + 0, &nsze, sizeof(uint64_t));

    uint8_t lbads = nvme_log2_u32(dev->block_size);
    ((uint8_t*)buffer)[128] = lbads;
    ((uint8_t*)buffer)[26] = 0;
}

static void nvme_queue_init(nvme_sq* sq, nvme_cq* cq, uint16_t depth) {
    if (depth == 0 || depth > 256) {
        depth = 64;
    }
    sq->head = 0;
    sq->tail = 0;
    sq->depth = depth;
    cq->head = 0;
    cq->tail = 0;
    cq->depth = depth;
}

static int nvme_queue_push(nvme_cq* cq, const nvme_cpl* cpl) {
    uint16_t next = (cq->tail + 1) % cq->depth;
    if (next == cq->head) {
        return -1;
    }
    cq->cpls[cq->tail] = *cpl;
    cq->tail = next;
    return 0;
}

static int nvme_queue_pop(nvme_cq* cq, nvme_cpl* cpl) {
    if (cq->head == cq->tail) {
        return -1;
    }
    *cpl = cq->cpls[cq->head];
    cq->head = (cq->head + 1) % cq->depth;
    return 0;
}

static int nvme_cmd_rw_fields(const nvme_cmd* cmd, uint64_t* slba, uint32_t* nlb) {
    if (!cmd || !slba || !nlb) {
        return -1;
    }
    *slba = ((uint64_t)cmd->cdw11 << 32) | cmd->cdw10;
    *nlb = (cmd->cdw12 & 0xFFFF) + 1;
    return 0;
}

static void nvme_build_cpl(nvme_cpl* cpl, const nvme_cmd* cmd, uint16_t status) {
    memset(cpl, 0, sizeof(*cpl));
    cpl->cid = cmd->cid;
    cpl->sqid = 1;
    cpl->sqhd = 0;
    cpl->status = status;
}

static int nvme_submit_cmd_common(nvme_emu_device* dev,
                                  uint64_t* read_ops,
                                  uint64_t* write_ops,
                                  uint64_t* bytes_read,
                                  uint64_t* bytes_written,
                                  nvme_cq* cq,
                                  const nvme_cmd* cmd,
                                  void* buffer,
                                  uint64_t buffer_len) {
    if (!dev || !cmd || !cq) {
        return -1;
    }
    nvme_cpl cpl;
    uint16_t status = 0;

    switch (cmd->opcode) {
    case 0x02: { // READ
        uint64_t slba = 0;
        uint32_t nlb = 0;
        if (nvme_cmd_rw_fields(cmd, &slba, &nlb) != 0) {
            status = 1;
            break;
        }
        if (nvme_emu_read(dev, slba, nlb, buffer, buffer_len) != 0) {
            status = 2;
            break;
        }
        uint64_t bytes = (uint64_t)nlb * (uint64_t)dev->block_size;
        *read_ops += 1;
        *bytes_read += bytes;
        break;
    }
    case 0x01: { // WRITE
        uint64_t slba = 0;
        uint32_t nlb = 0;
        if (nvme_cmd_rw_fields(cmd, &slba, &nlb) != 0) {
            status = 1;
            break;
        }
        if (nvme_emu_write(dev, slba, nlb, buffer, buffer_len) != 0) {
            status = 2;
            break;
        }
        uint64_t bytes = (uint64_t)nlb * (uint64_t)dev->block_size;
        *write_ops += 1;
        *bytes_written += bytes;
        break;
    }
    case 0x00: // FLUSH
        status = 0;
        break;
    case 0x06: { // IDENTIFY (Admin)
        uint8_t cns = (uint8_t)(cmd->cdw10 & 0xFF);
        if (cns == 0x01) {
            nvme_fill_identify_ctrl(dev, buffer, buffer_len);
        } else if (cns == 0x00) {
            nvme_fill_identify_ns(dev, buffer, buffer_len);
        } else {
            status = 1;
        }
        break;
    }
    default:
        status = 1;
    }

    nvme_build_cpl(&cpl, cmd, status);
    if (nvme_queue_push(cq, &cpl) != 0) {
        return -3;
    }
    return 0;
}

nvme_pcie_ctrl* nvme_pcie_create(const char* pci_addr, uint32_t block_size, uint64_t size_bytes) {
    nvme_pcie_ctrl* ctrl = (nvme_pcie_ctrl*)calloc(1, sizeof(nvme_pcie_ctrl));
    if (!ctrl) {
        return NULL;
    }
    ctrl->dev = nvme_emu_create(size_bytes, block_size);
    if (!ctrl->dev) {
        free(ctrl);
        return NULL;
    }
    if (pci_addr) {
        strncpy(ctrl->serial, pci_addr, sizeof(ctrl->serial) - 1);
    }
    nvme_queue_init(&ctrl->sq, &ctrl->cq, 64);
    return ctrl;
}

void nvme_pcie_geometry(nvme_pcie_ctrl* ctrl, uint32_t* block_size, uint64_t* num_blocks) {
    if (!ctrl || !ctrl->dev) {
        return;
    }
    if (block_size) {
        *block_size = ctrl->dev->block_size;
    }
    if (num_blocks) {
        *num_blocks = ctrl->dev->size_bytes / ctrl->dev->block_size;
    }
}

int nvme_pcie_identify(nvme_pcie_ctrl* ctrl, uint32_t nsid, uint8_t cns,
                       void* buffer, uint64_t buffer_len) {
    if (!ctrl || !ctrl->dev) {
        return -1;
    }
    if (cns == 0x01) {
        nvme_fill_identify_ctrl(ctrl->dev, buffer, buffer_len);
        return 0;
    }
    if (cns == 0x00) {
        nvme_fill_identify_ns(ctrl->dev, buffer, buffer_len);
        return 0;
    }
    return -2;
}

void nvme_pcie_queue_init(nvme_pcie_ctrl* ctrl, uint16_t depth) {
    if (!ctrl) {
        return;
    }
    nvme_queue_init(&ctrl->sq, &ctrl->cq, depth);
}

int nvme_pcie_submit_cmd(nvme_pcie_ctrl* ctrl, const nvme_cmd* cmd,
                         void* buffer, uint64_t buffer_len) {
    if (!ctrl) {
        return -1;
    }
    return nvme_submit_cmd_common(ctrl->dev, &ctrl->read_ops, &ctrl->write_ops,
                                  &ctrl->bytes_read, &ctrl->bytes_written,
                                  &ctrl->cq, cmd, buffer, buffer_len);
}

int nvme_pcie_poll_cpl(nvme_pcie_ctrl* ctrl, nvme_cpl* cpl) {
    if (!ctrl || !cpl) {
        return -1;
    }
    return nvme_queue_pop(&ctrl->cq, cpl);
}

void nvme_pcie_destroy(nvme_pcie_ctrl* ctrl) {
    if (!ctrl) {
        return;
    }
    nvme_emu_destroy(ctrl->dev);
    free(ctrl);
}

int nvme_pcie_read(nvme_pcie_ctrl* ctrl, uint64_t lba, uint32_t num_blocks,
                          void* buffer, uint64_t buffer_len) {
    int ret = nvme_emu_read(ctrl->dev, lba, num_blocks, buffer, buffer_len);
    if (ret == 0) {
        uint64_t bytes = (uint64_t)num_blocks * (uint64_t)ctrl->dev->block_size;
        ctrl->read_ops += 1;
        ctrl->bytes_read += bytes;
    }
    return ret;
}

int nvme_pcie_write(nvme_pcie_ctrl* ctrl, uint64_t lba, uint32_t num_blocks,
                           const void* buffer, uint64_t buffer_len) {
    int ret = nvme_emu_write(ctrl->dev, lba, num_blocks, buffer, buffer_len);
    if (ret == 0) {
        uint64_t bytes = (uint64_t)num_blocks * (uint64_t)ctrl->dev->block_size;
        ctrl->write_ops += 1;
        ctrl->bytes_written += bytes;
    }
    return ret;
}

void nvme_pcie_stats(nvme_pcie_ctrl* ctrl, nvme_stats* stats) {
    stats->read_ops = ctrl->read_ops;
    stats->write_ops = ctrl->write_ops;
    stats->bytes_read = ctrl->bytes_read;
    stats->bytes_written = ctrl->bytes_written;
}

nvme_fabrics_ctrl* nvme_fabrics_create(const char* address,
                                              const char* host_nqn,
                                              const char* subsys_nqn,
                                              uint32_t block_size,
                                              uint64_t size_bytes) {
    nvme_fabrics_ctrl* ctrl = (nvme_fabrics_ctrl*)calloc(1, sizeof(nvme_fabrics_ctrl));
    if (!ctrl) {
        return NULL;
    }
    ctrl->dev = nvme_emu_create(size_bytes, block_size);
    if (!ctrl->dev) {
        free(ctrl);
        return NULL;
    }
    if (address) {
        strncpy(ctrl->address, address, sizeof(ctrl->address) - 1);
    }
    if (host_nqn) {
        strncpy(ctrl->host_nqn, host_nqn, sizeof(ctrl->host_nqn) - 1);
    }
    if (subsys_nqn) {
        strncpy(ctrl->subsys_nqn, subsys_nqn, sizeof(ctrl->subsys_nqn) - 1);
    }
    ctrl->controller_id = 1;
    ctrl->connected = 0;
    nvme_queue_init(&ctrl->sq, &ctrl->cq, 64);
    return ctrl;
}

void nvme_fabrics_geometry(nvme_fabrics_ctrl* ctrl, uint32_t* block_size, uint64_t* num_blocks) {
    if (!ctrl || !ctrl->dev) {
        return;
    }
    if (block_size) {
        *block_size = ctrl->dev->block_size;
    }
    if (num_blocks) {
        *num_blocks = ctrl->dev->size_bytes / ctrl->dev->block_size;
    }
}

int nvme_fabrics_connect(nvme_fabrics_ctrl* ctrl, uint16_t qid, uint16_t sqsize, uint16_t* ctrl_id) {
    if (!ctrl) {
        return -1;
    }
    ctrl->connected = 1;
    if (ctrl_id) {
        *ctrl_id = ctrl->controller_id;
    }
    return 0;
}

int nvme_fabrics_identify(nvme_fabrics_ctrl* ctrl, uint32_t nsid, uint8_t cns,
                          void* buffer, uint64_t buffer_len) {
    if (!ctrl || !ctrl->dev || !ctrl->connected) {
        return -1;
    }
    if (cns == 0x01) {
        nvme_fill_identify_ctrl(ctrl->dev, buffer, buffer_len);
        return 0;
    }
    if (cns == 0x00) {
        nvme_fill_identify_ns(ctrl->dev, buffer, buffer_len);
        return 0;
    }
    return -2;
}

void nvme_fabrics_queue_init(nvme_fabrics_ctrl* ctrl, uint16_t depth) {
    if (!ctrl) {
        return;
    }
    nvme_queue_init(&ctrl->sq, &ctrl->cq, depth);
}

int nvme_fabrics_submit_cmd(nvme_fabrics_ctrl* ctrl, const nvme_cmd* cmd,
                            void* buffer, uint64_t buffer_len) {
    if (!ctrl || !ctrl->connected) {
        return -5;
    }
    return nvme_submit_cmd_common(ctrl->dev, &ctrl->read_ops, &ctrl->write_ops,
                                  &ctrl->bytes_read, &ctrl->bytes_written,
                                  &ctrl->cq, cmd, buffer, buffer_len);
}

int nvme_fabrics_poll_cpl(nvme_fabrics_ctrl* ctrl, nvme_cpl* cpl) {
    if (!ctrl || !cpl || !ctrl->connected) {
        return -5;
    }
    return nvme_queue_pop(&ctrl->cq, cpl);
}

void nvme_fabrics_destroy(nvme_fabrics_ctrl* ctrl) {
    if (!ctrl) {
        return;
    }
    nvme_emu_destroy(ctrl->dev);
    free(ctrl);
}

int nvme_fabrics_read(nvme_fabrics_ctrl* ctrl, uint64_t lba, uint32_t num_blocks,
                             void* buffer, uint64_t buffer_len) {
    if (!ctrl || !ctrl->connected) {
        return -5;
    }
    int ret = nvme_emu_read(ctrl->dev, lba, num_blocks, buffer, buffer_len);
    if (ret == 0) {
        uint64_t bytes = (uint64_t)num_blocks * (uint64_t)ctrl->dev->block_size;
        ctrl->read_ops += 1;
        ctrl->bytes_read += bytes;
    }
    return ret;
}

int nvme_fabrics_write(nvme_fabrics_ctrl* ctrl, uint64_t lba, uint32_t num_blocks,
                              const void* buffer, uint64_t buffer_len) {
    if (!ctrl || !ctrl->connected) {
        return -5;
    }
    int ret = nvme_emu_write(ctrl->dev, lba, num_blocks, buffer, buffer_len);
    if (ret == 0) {
        uint64_t bytes = (uint64_t)num_blocks * (uint64_t)ctrl->dev->block_size;
        ctrl->write_ops += 1;
        ctrl->bytes_written += bytes;
    }
    return ret;
}

void nvme_fabrics_stats(nvme_fabrics_ctrl* ctrl, nvme_stats* stats) {
    stats->read_ops = ctrl->read_ops;
    stats->write_ops = ctrl->write_ops;
    stats->bytes_read = ctrl->bytes_read;
    stats->bytes_written = ctrl->bytes_written;
}
*/
import "C"
