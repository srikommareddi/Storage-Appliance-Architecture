#include <linux/nvme.h>
#include <linux/pci.h>
#include <linux/interrupt.h>
#include <linux/blk-mq.h>

// Custom NVMe controller driver for storage appliances
class NVMeController {
private:
    // PCIe device handle
    struct pci_dev* pci_dev_;
    
    // Controller registers (memory-mapped)
    volatile struct nvme_bar* bar_;
    
    // Admin queue pair
    struct NVMeQueue* admin_queue_;
    
    // I/O queue pairs (one per CPU core for parallelism)
    std::vector<std::unique_ptr<NVMeQueue>> io_queues_;
    
    // Controller capabilities
    struct nvme_id_ctrl ctrl_id_;
    uint32_t max_transfer_size_;
    uint32_t max_hw_queues_;
    uint32_t page_size_;
    
    // Namespace management
    std::unordered_map<uint32_t, std::unique_ptr<NVMeNamespace>> namespaces_;
    
    // MSI-X vectors for interrupts
    std::vector<int> msix_vectors_;
    
    // Performance monitoring
    struct ControllerStats {
        std::atomic<uint64_t> commands_submitted;
        std::atomic<uint64_t> commands_completed;
        std::atomic<uint64_t> total_latency_ns;
        std::atomic<uint64_t> read_bytes;
        std::atomic<uint64_t> write_bytes;
    } stats_;
    
public:
    NVMeController(struct pci_dev* pdev) : pci_dev_(pdev) {
        // Enable PCIe device
        if (pci_enable_device(pci_dev_) < 0) {
            throw std::runtime_error("Failed to enable PCIe device");
        }
        
        // Set DMA mask (64-bit addressing)
        if (dma_set_mask_and_coherent(&pci_dev_->dev, DMA_BIT_MASK(64)) < 0) {
            throw std::runtime_error("Failed to set DMA mask");
        }
        
        // Request memory regions
        if (pci_request_regions(pci_dev_, "nvme") < 0) {
            throw std::runtime_error("Failed to request regions");
        }
        
        // Map BAR0 (controller registers)
        bar_ = static_cast<nvme_bar*>(
            pci_ioremap_bar(pci_dev_, 0));
        
        if (!bar_) {
            throw std::runtime_error("Failed to map BAR0");
        }
        
        // Read controller capabilities
        read_capabilities();
        
        // Reset controller
        reset_controller();
        
        // Create admin queue
        admin_queue_ = create_admin_queue();
        
        // Identify controller
        identify_controller();
        
        // Setup MSI-X interrupts
        setup_msix_interrupts();
        
        // Create I/O queues (one per CPU)
        create_io_queues();
        
        // Identify and initialize namespaces
        initialize_namespaces();
    }
    
    ~NVMeController() {
        // Cleanup
        for (auto& queue : io_queues_) {
            delete_queue(queue.get());
        }
        
        delete_queue(admin_queue_);
        
        if (bar_) {
            iounmap(bar_);
        }
        
        pci_release_regions(pci_dev_);
        pci_disable_device(pci_dev_);
    }
    
private:
    void read_capabilities() {
        uint64_t cap = readq(&bar_->cap);
        
        max_hw_queues_ = (cap >> 16) & 0xFFFF;  // MQES
        page_size_ = 1UL << (12 + ((cap >> 48) & 0xF));  // MPSMIN
        
        // Maximum transfer size (MDTS)
        uint8_t mdts = (cap >> 54) & 0xF;
        if (mdts) {
            max_transfer_size_ = page_size_ << mdts;
        } else {
            max_transfer_size_ = UINT32_MAX;
        }
        
        printf("NVMe Controller Capabilities:\n");
        printf("  Max HW Queues: %u\n", max_hw_queues_);
        printf("  Page Size: %u\n", page_size_);
        printf("  Max Transfer: %u bytes\n", max_transfer_size_);
    }
    
    void reset_controller() {
        // Disable controller
        uint32_t cc = readl(&bar_->cc);
        cc &= ~NVME_CC_ENABLE;
        writel(cc, &bar_->cc);
        
        // Wait for controller to be ready
        uint32_t csts;
        int timeout = 1000; // 1 second
        
        do {
            csts = readl(&bar_->csts);
            if (!(csts & NVME_CSTS_RDY)) {
                break;
            }
            usleep(1000);
        } while (--timeout > 0);
        
        if (timeout == 0) {
            throw std::runtime_error("Controller reset timeout");
        }
    }
    
    struct NVMeQueue* create_admin_queue() {
        auto queue = new NVMeQueue(this, 0, NVME_AQ_DEPTH);
        
        // Configure admin queue in controller
        writeq(queue->sq_dma_addr, &bar_->asq);
        writeq(queue->cq_dma_addr, &bar_->acq);
        
        uint32_t aqa = (NVME_AQ_DEPTH - 1) | 
                      ((NVME_AQ_DEPTH - 1) << 16);
        writel(aqa, &bar_->aqa);
        
        // Enable controller
        uint32_t cc = NVME_CC_ENABLE | 
                     (NVME_CC_CSS_NVM << NVME_CC_CSS_SHIFT) |
                     (6 << NVME_CC_IOSQES_SHIFT) |  // 64 bytes
                     (4 << NVME_CC_IOCQES_SHIFT);   // 16 bytes
        writel(cc, &bar_->cc);
        
        // Wait for ready
        int timeout = 1000;
        uint32_t csts;
        
        do {
            csts = readl(&bar_->csts);
            if (csts & NVME_CSTS_RDY) {
                break;
            }
            usleep(1000);
        } while (--timeout > 0);
        
        if (timeout == 0) {
            throw std::runtime_error("Controller ready timeout");
        }
        
        return queue;
    }
    
    void identify_controller() {
        // Allocate DMA buffer for identify data
        void* identify_buf = dma_alloc_coherent(&pci_dev_->dev, 
                                               4096, &dma_addr, 
                                               GFP_KERNEL);
        
        // Build identify command
        struct nvme_command cmd = {};
        cmd.identify.opcode = nvme_admin_identify;
        cmd.identify.nsid = 0;
        cmd.identify.cns = NVME_ID_CNS_CTRL;
        cmd.identify.dptr.prp1 = dma_addr;
        
        // Submit and wait for completion
        auto result = submit_admin_command_sync(&cmd);
        
        if (result.status != 0) {
            dma_free_coherent(&pci_dev_->dev, 4096, identify_buf, dma_addr);
            throw std::runtime_error("Identify controller failed");
        }
        
        // Copy identify data
        memcpy(&ctrl_id_, identify_buf, sizeof(ctrl_id_));
        
        dma_free_coherent(&pci_dev_->dev, 4096, identify_buf, dma_addr);
        
        // Print controller info
        printf("Model: %.40s\n", ctrl_id_.mn);
        printf("Serial: %.20s\n", ctrl_id_.sn);
        printf("Firmware: %.8s\n", ctrl_id_.fr);
    }
    
    void setup_msix_interrupts() {
        int num_queues = std::min(max_hw_queues_, 
                                 static_cast<uint32_t>(num_online_cpus()));
        
        // Allocate MSI-X vectors (one per queue + admin)
        msix_vectors_.resize(num_queues + 1);
        
        int nvec = pci_alloc_irq_vectors(pci_dev_, num_queues + 1, 
                                        num_queues + 1, PCI_IRQ_MSIX);
        
        if (nvec < 0) {
            throw std::runtime_error("Failed to allocate MSI-X vectors");
        }
        
        // Register interrupt handlers
        for (int i = 0; i < nvec; ++i) {
            int irq = pci_irq_vector(pci_dev_, i);
            msix_vectors_[i] = irq;
            
            if (request_irq(irq, nvme_irq_handler, 0, 
                          "nvme-queue", this) < 0) {
                throw std::runtime_error("Failed to request IRQ");
            }
        }
    }
    
    void create_io_queues() {
        int num_cpus = num_online_cpus();
        
        for (int i = 0; i < num_cpus; ++i) {
            // Create completion queue
            uint16_t cqid = i + 1;
            auto cq_result = create_completion_queue(cqid, NVME_Q_DEPTH, 
                                                    msix_vectors_[i + 1]);
            
            // Create submission queue
            uint16_t sqid = i + 1;
            auto sq_result = create_submission_queue(sqid, cqid, NVME_Q_DEPTH);
            
            // Create queue object
            auto queue = std::make_unique<NVMeQueue>(this, sqid, NVME_Q_DEPTH);
            queue->cqid = cqid;
            queue->cpu_affinity = i;
            
            io_queues_.push_back(std::move(queue));
        }
    }
    
    CommandResult create_completion_queue(uint16_t qid, uint16_t qsize, 
                                         int irq_vector) {
        // Allocate DMA memory for CQ
        dma_addr_t cq_dma;
        void* cq_mem = dma_alloc_coherent(&pci_dev_->dev, 
                                         qsize * sizeof(nvme_completion),
                                         &cq_dma, GFP_KERNEL);
        
        // Build create CQ command
        struct nvme_command cmd = {};
        cmd.create_cq.opcode = nvme_admin_create_cq;
        cmd.create_cq.cqid = qid;
        cmd.create_cq.qsize = qsize - 1;
        cmd.create_cq.cq_flags = NVME_QUEUE_PHYS_CONTIG | NVME_CQ_IRQ_ENABLED;
        cmd.create_cq.irq_vector = irq_vector;
        cmd.create_cq.dptr.prp1 = cq_dma;
        
        return submit_admin_command_sync(&cmd);
    }
    
    CommandResult create_submission_queue(uint16_t sqid, uint16_t cqid, 
                                         uint16_t qsize) {
        // Allocate DMA memory for SQ
        dma_addr_t sq_dma;
        void* sq_mem = dma_alloc_coherent(&pci_dev_->dev,
                                         qsize * sizeof(nvme_command),
                                         &sq_dma, GFP_KERNEL);
        
        // Build create SQ command
        struct nvme_command cmd = {};
        cmd.create_sq.opcode = nvme_admin_create_sq;
        cmd.create_sq.sqid = sqid;
        cmd.create_sq.qsize = qsize - 1;
        cmd.create_sq.sq_flags = NVME_QUEUE_PHYS_CONTIG;
        cmd.create_sq.cqid = cqid;
        cmd.create_sq.dptr.prp1 = sq_dma;
        
        // High priority for low latency
        cmd.create_sq.sq_flags |= (NVME_SQ_PRIO_HIGH << 1);
        
        return submit_admin_command_sync(&cmd);
    }
    
    void initialize_namespaces() {
        // Get number of namespaces
        uint32_t nn = ctrl_id_.nn;
        
        for (uint32_t nsid = 1; nsid <= nn; ++nsid) {
            auto ns = std::make_unique<NVMeNamespace>(this, nsid);
            
            if (ns->identify()) {
                namespaces_[nsid] = std::move(ns);
            }
        }
    }
    
    static irqreturn_t nvme_irq_handler(int irq, void* data) {
        auto* ctrl = static_cast<NVMeController*>(data);
        
        // Find which queue triggered this interrupt
        for (auto& queue : ctrl->io_queues_) {
            if (ctrl->msix_vectors_[queue->qid] == irq) {
                queue->process_completions();
                break;
            }
        }
        
        return IRQ_HANDLED;
    }
};

// NVMe queue pair implementation
struct NVMeQueue {
    NVMeController* ctrl;
    uint16_t qid;
    uint16_t size;
    
    // Submission queue
    struct nvme_command* sq;
    dma_addr_t sq_dma_addr;
    uint16_t sq_head;
    uint16_t sq_tail;
    
    // Completion queue
    struct nvme_completion* cq;
    dma_addr_t cq_dma_addr;
    uint16_t cq_head;
    uint16_t cq_phase;
    
    // Completion queue ID (for SQ)
    uint16_t cqid;
    
    // CPU affinity
    int cpu_affinity;
    
    // Doorbell registers
    volatile uint32_t* sq_doorbell;
    volatile uint32_t* cq_doorbell;
    
    // Command tracking
    struct CommandContext {
        uint16_t command_id;
        std::function<void(const nvme_completion&)> callback;
        std::chrono::steady_clock::time_point submit_time;
    };
    
    std::unordered_map<uint16_t, CommandContext> pending_commands_;
    std::mutex commands_mutex_;
    uint16_t next_command_id_;
    
    NVMeQueue(NVMeController* c, uint16_t id, uint16_t sz) 
        : ctrl(c), qid(id), size(sz), sq_head(0), sq_tail(0),
          cq_head(0), cq_phase(1), next_command_id_(0) {
        
        // Allocate submission queue
        sq = static_cast<nvme_command*>(
            dma_alloc_coherent(&ctrl->pci_dev_->dev,
                             size * sizeof(nvme_command),
                             &sq_dma_addr, GFP_KERNEL));
        
        // Allocate completion queue
        cq = static_cast<nvme_completion*>(
            dma_alloc_coherent(&ctrl->pci_dev_->dev,
                             size * sizeof(nvme_completion),
                             &cq_dma_addr, GFP_KERNEL));
        
        // Calculate doorbell addresses
        uint32_t db_stride = 1 << (2 + ((ctrl->bar_->cap >> 32) & 0xF));
        sq_doorbell = ctrl->bar_->dbs + (qid * 2 * db_stride);
        cq_doorbell = ctrl->bar_->dbs + (qid * 2 * db_stride + db_stride);
    }
    
    ~NVMeQueue() {
        if (sq) {
            dma_free_coherent(&ctrl->pci_dev_->dev,
                            size * sizeof(nvme_command),
                            sq, sq_dma_addr);
        }
        
        if (cq) {
            dma_free_coherent(&ctrl->pci_dev_->dev,
                            size * sizeof(nvme_completion),
                            cq, cq_dma_addr);
        }
    }
    
    // Submit command (async)
    void submit_command(const nvme_command& cmd,
                       std::function<void(const nvme_completion&)> callback) {
        
        std::lock_guard lock(commands_mutex_);
        
        // Get next command ID
        uint16_t cid = next_command_id_++;
        if (next_command_id_ >= size) {
            next_command_id_ = 0;
        }
        
        // Store command context
        CommandContext ctx;
        ctx.command_id = cid;
        ctx.callback = callback;
        ctx.submit_time = std::chrono::steady_clock::now();
        pending_commands_[cid] = ctx;
        
        // Copy command to SQ
        nvme_command* sq_cmd = &sq[sq_tail];
        memcpy(sq_cmd, &cmd, sizeof(nvme_command));
        sq_cmd->common.command_id = cid;
        
        // Advance tail
        sq_tail = (sq_tail + 1) % size;
        
        // Ring doorbell
        writel(sq_tail, sq_doorbell);
        
        ctrl->stats_.commands_submitted.fetch_add(1);
    }
    
    // Process completions
    void process_completions() {
        while (true) {
            // Check phase bit
            nvme_completion* cqe = &cq[cq_head];
            
            if ((le16_to_cpu(cqe->status) & 1) != cq_phase) {
                break;  // No more completions
            }
            
            // Extract command ID and status
            uint16_t cid = le16_to_cpu(cqe->command_id);
            uint16_t status = le16_to_cpu(cqe->status) >> 1;
            
            // Find pending command
            std::lock_guard lock(commands_mutex_);
            auto it = pending_commands_.find(cid);
            
            if (it != pending_commands_.end()) {
                // Calculate latency
                auto now = std::chrono::steady_clock::now();
                auto latency = std::chrono::duration_cast<std::chrono::nanoseconds>(
                    now - it->second.submit_time).count();
                
                ctrl->stats_.total_latency_ns.fetch_add(latency);
                ctrl->stats_.commands_completed.fetch_add(1);
                
                // Invoke callback
                if (it->second.callback) {
                    it->second.callback(*cqe);
                }
                
                pending_commands_.erase(it);
            }
            
            // Advance head
            cq_head = (cq_head + 1) % size;
            
            // Flip phase at wrap
            if (cq_head == 0) {
                cq_phase = !cq_phase;
            }
        }
        
        // Update doorbell
        if (cq_head != 0 || cq_phase != 1) {
            writel(cq_head, cq_doorbell);
        }
    }
};

// NVMe namespace
class NVMeNamespace {
private:
    NVMeController* ctrl_;
    uint32_t nsid_;
    
    struct nvme_id_ns ns_id_;
    
    uint64_t capacity_blocks_;
    uint32_t block_size_;
    uint64_t capacity_bytes_;
    
public:
    NVMeNamespace(NVMeController* ctrl, uint32_t nsid)
        : ctrl_(ctrl), nsid_(nsid) {}
    
    bool identify() {
        // Allocate DMA buffer
        dma_addr_t dma_addr;
        void* buf = dma_alloc_coherent(&ctrl_->pci_dev_->dev,
                                      4096, &dma_addr, GFP_KERNEL);
        
        // Build identify namespace command
        struct nvme_command cmd = {};
        cmd.identify.opcode = nvme_admin_identify;
        cmd.identify.nsid = nsid_;
        cmd.identify.cns = NVME_ID_CNS_NS;
        cmd.identify.dptr.prp1 = dma_addr;
        
        auto result = ctrl_->submit_admin_command_sync(&cmd);
        
        if (result.status != 0) {
            dma_free_coherent(&ctrl_->pci_dev_->dev, 4096, buf, dma_addr);
            return false;
        }
        
        // Copy namespace data
        memcpy(&ns_id_, buf, sizeof(ns_id_));
        
        dma_free_coherent(&ctrl_->pci_dev_->dev, 4096, buf, dma_addr);
        
        // Extract capacity and block size
        capacity_blocks_ = le64_to_cpu(ns_id_.nsze);
        
        uint8_t lba_format = ns_id_.flbas & 0xF;
        block_size_ = 1 << ns_id_.lbaf[lba_format].ds;
        
        capacity_bytes_ = capacity_blocks_ * block_size_;
        
        printf("Namespace %u: %llu blocks, %u bytes/block (%llu GB)\n",
               nsid_, capacity_blocks_, block_size_,
               capacity_bytes_ / (1024ULL * 1024 * 1024));
        
        return true;
    }
    
    // Async read
    void read_async(uint64_t lba, uint32_t num_blocks, void* buffer,
                   std::function<void(int)> callback) {
        
        // Get I/O queue for current CPU
        int cpu = smp_processor_id();
        auto* queue = ctrl_->get_io_queue(cpu);
        
        // Allocate DMA buffer if needed
        dma_addr_t dma_addr;
        bool need_copy = !virt_addr_valid(buffer);
        void* dma_buffer = buffer;
        
        if (need_copy) {
            dma_buffer = dma_alloc_coherent(&ctrl_->pci_dev_->dev,
                                           num_blocks * block_size_,
                                           &dma_addr, GFP_KERNEL);
        } else {
            dma_addr = virt_to_phys(buffer);
        }
        
        // Build read command
        struct nvme_command cmd = {};
        cmd.rw.opcode = nvme_cmd_read;
        cmd.rw.nsid = nsid_;
        cmd.rw.slba = cpu_to_le64(lba);
        cmd.rw.length = cpu_to_le16(num_blocks - 1);
        cmd.rw.dptr.prp1 = cpu_to_le64(dma_addr);
        
        // Handle large transfers (PRP2 or SGL)
        if (num_blocks * block_size_ > PAGE_SIZE) {
            setup_prp_list(&cmd, dma_addr, num_blocks * block_size_);
        }
        
        // Submit command
        queue->submit_command(cmd, [=](const nvme_completion& cqe) {
            uint16_t status = le16_to_cpu(cqe.status) >> 1;
            
            // Copy data if needed
            if (need_copy && status == 0) {
                memcpy(buffer, dma_buffer, num_blocks * block_size_);
                dma_free_coherent(&ctrl_->pci_dev_->dev,
                                num_blocks * block_size_,
                                dma_buffer, dma_addr);
            }
            
            // Update stats
            if (status == 0) {
                ctrl_->stats_.read_bytes.fetch_add(num_blocks * block_size_);
            }
            
            callback(status == 0 ? 0 : -EIO);
        });
    }
    
    // Async write
    void write_async(uint64_t lba, uint32_t num_blocks, const void* buffer,
                    std::function<void(int)> callback) {
        
        int cpu = smp_processor_id();
        auto* queue = ctrl_->get_io_queue(cpu);
        
        dma_addr_t dma_addr;
        bool need_copy = !virt_addr_valid(buffer);
        void* dma_buffer = const_cast<void*>(buffer);
        
        if (need_copy) {
            dma_buffer = dma_alloc_coherent(&ctrl_->pci_dev_->dev,
                                           num_blocks * block_size_,
                                           &dma_addr, GFP_KERNEL);
            memcpy(dma_buffer, buffer, num_blocks * block_size_);
        } else {
            dma_addr = virt_to_phys(buffer);
        }
        
        // Build write command
        struct nvme_command cmd = {};
        cmd.rw.opcode = nvme_cmd_write;
        cmd.rw.nsid = nsid_;
        cmd.rw.slba = cpu_to_le64(lba);
        cmd.rw.length = cpu_to_le16(num_blocks - 1);
        cmd.rw.dptr.prp1 = cpu_to_le64(dma_addr);
        
        if (num_blocks * block_size_ > PAGE_SIZE) {
            setup_prp_list(&cmd, dma_addr, num_blocks * block_size_);
        }
        
        queue->submit_command(cmd, [=](const nvme_completion& cqe) {
            uint16_t status = le16_to_cpu(cqe.status) >> 1;
            
            if (need_copy) {
                dma_free_coherent(&ctrl_->pci_dev_->dev,
                                num_blocks * block_size_,
                                dma_buffer, dma_addr);
            }
            
            if (status == 0) {
                ctrl_->stats_.write_bytes.fetch_add(num_blocks * block_size_);
            }
            
            callback(status == 0 ? 0 : -EIO);
        });
    }
    
    // Flush (FUA bit alternative)
    void flush_async(std::function<void(int)> callback) {
        int cpu = smp_processor_id();
        auto* queue = ctrl_->get_io_queue(cpu);
        
        struct nvme_command cmd = {};
        cmd.common.opcode = nvme_cmd_flush;
        cmd.common.nsid = nsid_;
        
        queue->submit_command(cmd, [callback](const nvme_completion& cqe) {
            uint16_t status = le16_to_cpu(cqe.status) >> 1;
            callback(status == 0 ? 0 : -EIO);
        });
    }
    
    // Deallocate/TRIM
    void deallocate_async(uint64_t lba, uint32_t num_blocks,
                         std::function<void(int)> callback) {
        
        int cpu = smp_processor_id();
        auto* queue = ctrl_->get_io_queue(cpu);
        
        // Allocate range structure
        struct nvme_dsm_range {
            uint32_t cattr;
            uint32_t nlb;
            uint64_t slba;
        };
        
        dma_addr_t dma_addr;
        nvme_dsm_range* range = static_cast<nvme_dsm_range*>(
            dma_alloc_coherent(&ctrl_->pci_dev_->dev,
                             sizeof(nvme_dsm_range),
                             &dma_addr, GFP_KERNEL));
        
        range->slba = cpu_to_le64(lba);
        range->nlb = cpu_to_le32(num_blocks);
        range->cattr = 0;
        
        struct nvme_command cmd = {};
        cmd.dsm.opcode = nvme_cmd_dsm;
        cmd.dsm.nsid = nsid_;
        cmd.dsm.nr = 0;  // One range
        cmd.dsm.attributes = NVME_DSMGMT_AD;  // Deallocate
        cmd.dsm.dptr.prp1 = cpu_to_le64(dma_addr);
        
        queue->submit_command(cmd, [=](const nvme_completion& cqe) {
            uint16_t status = le16_to_cpu(cqe.status) >> 1;
            
            dma_free_coherent(&ctrl_->pci_dev_->dev,
                            sizeof(nvme_dsm_range),
                            range, dma_addr);
            
            callback(status == 0 ? 0 : -EIO);
        });
    }
    
    uint64_t capacity_bytes() const { return capacity_bytes_; }
    uint32_t block_size() const { return block_size_; }
    
private:
    void setup_prp_list(nvme_command* cmd, dma_addr_t addr, size_t len);
};