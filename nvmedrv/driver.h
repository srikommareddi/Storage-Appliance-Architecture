#ifndef NVMEDRV_DRIVER_H
#define NVMEDRV_DRIVER_H

#include <stdint.h>

typedef struct {
    uint8_t  opcode;
    uint8_t  flags;
    uint16_t cid;
    uint32_t nsid;
    uint64_t rsvd2;
    uint64_t mptr;
    uint64_t prp1;
    uint64_t prp2;
    uint32_t cdw10;
    uint32_t cdw11;
    uint32_t cdw12;
    uint32_t cdw13;
    uint32_t cdw14;
    uint32_t cdw15;
} nvme_cmd;

typedef struct {
    uint32_t dw0;
    uint32_t rsvd1;
    uint16_t sqhd;
    uint16_t sqid;
    uint16_t cid;
    uint16_t status;
} nvme_cpl;

typedef struct {
    nvme_cmd cmds[256];
    uint16_t head;
    uint16_t tail;
    uint16_t depth;
} nvme_sq;

typedef struct {
    nvme_cpl cpls[256];
    uint16_t head;
    uint16_t tail;
    uint16_t depth;
} nvme_cq;

typedef struct {
    uint64_t size_bytes;
    uint32_t block_size;
    uint8_t* data;
} nvme_emu_device;

typedef struct {
    nvme_emu_device* dev;
    char serial[32];
    nvme_sq sq;
    nvme_cq cq;
    uint64_t read_ops;
    uint64_t write_ops;
    uint64_t bytes_read;
    uint64_t bytes_written;
} nvme_pcie_ctrl;

typedef struct {
    nvme_emu_device* dev;
    char address[128];
    char host_nqn[256];
    char subsys_nqn[256];
    uint16_t controller_id;
    int connected;
    nvme_sq sq;
    nvme_cq cq;
    uint64_t read_ops;
    uint64_t write_ops;
    uint64_t bytes_read;
    uint64_t bytes_written;
} nvme_fabrics_ctrl;

typedef struct {
    uint64_t read_ops;
    uint64_t write_ops;
    uint64_t bytes_read;
    uint64_t bytes_written;
} nvme_stats;

nvme_pcie_ctrl* nvme_pcie_create(const char* pci_addr, uint32_t block_size, uint64_t size_bytes);
void nvme_pcie_destroy(nvme_pcie_ctrl* ctrl);
int nvme_pcie_read(nvme_pcie_ctrl* ctrl, uint64_t lba, uint32_t num_blocks,
                   void* buffer, uint64_t buffer_len);
int nvme_pcie_write(nvme_pcie_ctrl* ctrl, uint64_t lba, uint32_t num_blocks,
                    const void* buffer, uint64_t buffer_len);
void nvme_pcie_stats(nvme_pcie_ctrl* ctrl, nvme_stats* stats);
void nvme_pcie_geometry(nvme_pcie_ctrl* ctrl, uint32_t* block_size, uint64_t* num_blocks);
int nvme_pcie_identify(nvme_pcie_ctrl* ctrl, uint32_t nsid, uint8_t cns,
                       void* buffer, uint64_t buffer_len);
void nvme_pcie_queue_init(nvme_pcie_ctrl* ctrl, uint16_t depth);
int nvme_pcie_submit_cmd(nvme_pcie_ctrl* ctrl, const nvme_cmd* cmd,
                         void* buffer, uint64_t buffer_len);
int nvme_pcie_poll_cpl(nvme_pcie_ctrl* ctrl, nvme_cpl* cpl);

nvme_fabrics_ctrl* nvme_fabrics_create(const char* address, const char* host_nqn,
                                       const char* subsys_nqn, uint32_t block_size,
                                       uint64_t size_bytes);
void nvme_fabrics_destroy(nvme_fabrics_ctrl* ctrl);
int nvme_fabrics_read(nvme_fabrics_ctrl* ctrl, uint64_t lba, uint32_t num_blocks,
                      void* buffer, uint64_t buffer_len);
int nvme_fabrics_write(nvme_fabrics_ctrl* ctrl, uint64_t lba, uint32_t num_blocks,
                       const void* buffer, uint64_t buffer_len);
void nvme_fabrics_stats(nvme_fabrics_ctrl* ctrl, nvme_stats* stats);
void nvme_fabrics_geometry(nvme_fabrics_ctrl* ctrl, uint32_t* block_size, uint64_t* num_blocks);
int nvme_fabrics_connect(nvme_fabrics_ctrl* ctrl, uint16_t qid, uint16_t sqsize, uint16_t* ctrl_id);
int nvme_fabrics_identify(nvme_fabrics_ctrl* ctrl, uint32_t nsid, uint8_t cns,
                          void* buffer, uint64_t buffer_len);
void nvme_fabrics_queue_init(nvme_fabrics_ctrl* ctrl, uint16_t depth);
int nvme_fabrics_submit_cmd(nvme_fabrics_ctrl* ctrl, const nvme_cmd* cmd,
                            void* buffer, uint64_t buffer_len);
int nvme_fabrics_poll_cpl(nvme_fabrics_ctrl* ctrl, nvme_cpl* cpl);

#endif
