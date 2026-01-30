#include <rdma/ib_verbs.h>
#include <linux/nvme-fc.h>

// NVMe over Fabrics target (supports RDMA, TCP, FC)
class NVMeoFTarget {
private:
    enum TransportType {
        NVMEOF_RDMA,
        NVMEOF_TCP,
        NVMEOF_FC
    };
    
    struct Subsystem {
        std::string nqn;  // NVMe Qualified Name
        std::string serial_number;
        std::string model_number;
        
        // Namespaces in this subsystem
        std::vector<uint32_t> namespace_ids;
        
        // Host access control
        std::vector<std::string> allowed_host_nqns;
        bool allow_any_host;
        
        // I/O queues
        std::vector<std::unique_ptr<NVMeoFQueue>> io_queues;
    };
    
    std::unordered_map<std::string, Subsystem> subsystems_;
    std::shared_mutex subsystems_mutex_;
    
    // Backend storage
    NVMeController* backend_ctrl_;
    
    // Transport-specific handlers
    std::unique_ptr<RDMATransport> rdma_transport_;
    std::unique_ptr<TCPTransport> tcp_transport_;
    
public:
    NVMeoFTarget(NVMeController* backend) : backend_ctrl_(backend) {
        // Initialize transports
        rdma_transport_ = std::make_unique<RDMATransport>(this);
        tcp_transport_ = std::make_unique<TCPTransport>(this);
    }
    
    // Create subsystem
    std::string create_subsystem(const std::string& nqn,
                                const std::string& serial) {
        
        Subsystem subsys;
        subsys.nqn = nqn;
        subsys.serial_number = serial;
        subsys.model_number = "NVMeoF Target";
        subsys.allow_any_host = false;
        
        std::unique_lock lock(subsystems_mutex_);
        subsystems_[nqn] = std::move(subsys);
        
        return nqn;
    }
    
    // Add namespace to subsystem
    void add_namespace(const std::string& nqn, uint32_t nsid) {
        std::unique_lock lock(subsystems_mutex_);
        
        auto it = subsystems_.find(nqn);
        if (it == subsystems_.end()) {
            throw std::runtime_error("Subsystem not found");
        }
        
        it->second.namespace_ids.push_back(nsid);
    }
    
    // Process NVMe command from fabric
    void process_fabric_command(const std::string& nqn,
                                uint16_t queue_id,
                                const nvme_command& cmd,
                                void* data_buffer,
                                size_t data_len,
                                std::function<void(const nvme_completion&)> completion) {
        
        std::shared_lock lock(subsystems_mutex_);
        auto it = subsystems_.find(nqn);
        
        if (it == subsystems_.end()) {
            // Send error completion
            nvme_completion cqe = {};
            cqe.status = cpu_to_le16(NVME_SC_INTERNAL << 1);
            completion(cqe);
            return;
        }
        
        const Subsystem& subsys = it->second;
        lock.unlock();
        
        // Handle fabric-specific commands
        if (cmd.common.opcode == nvme_fabrics_command) {
            process_fabrics_command(subsys, cmd, completion);
            return;
        }
        
        // Forward I/O command to backend
        forward_to_backend(subsys, cmd, data_buffer, data_len, completion);
    }
    
private:
    void process_fabrics_command(const Subsystem& subsys,
                                 const nvme_command& cmd,
                                 std::function<void(const nvme_completion&)> completion) {
        
        uint8_t fctype = cmd.fabrics.fctype;
        
        switch (fctype) {
        case nvme_fabrics_type_connect: {
            // Handle connect request
            handle_connect(subsys, cmd, completion);
            break;
        }
        
        case nvme_fabrics_type_property_get: {
            // Get controller property
            handle_property_get(subsys, cmd, completion);
            break;
        }
        
        case nvme_fabrics_type_property_set: {
            // Set controller property
            handle_property_set(subsys, cmd, completion);
            break;
        }
        
        default: {
            nvme_completion cqe = {};
            cqe.status = cpu_to_le16(NVME_SC_INVALID_OPCODE << 1);
            completion(cqe);
        }
        }
    }
    
    void handle_connect(const Subsystem& subsys,
                       const nvme_command& cmd,
                       std::function<void(const nvme_completion&)> completion) {
        
        // Extract connect data
        const nvme_fabrics_connect_cmd& connect = cmd.connect;
        
        uint16_t queue_id = le16_to_cpu(connect.qid);
        uint16_t queue_size = le16_to_cpu(connect.sqsize);
        
        // Validate host NQN (if access control enabled)
        // ... (host authentication logic)
        
        // Create queue
        auto queue = std::make_unique<NVMeoFQueue>(queue_id, queue_size);
        
        // Success response
        nvme_completion cqe = {};
        cqe.result.u16 = cpu_to_le16(queue_id);  // Controller ID
        cqe.status = 0;
        
        completion(cqe);
    }
    
    void forward_to_backend(const Subsystem& subsys,
                           const nvme_command& cmd,
                           void* data_buffer,
                           size_t data_len,
                           std::function<void(const nvme_completion&)> completion) {
        
        // Get backend namespace
        uint32_t nsid = le32_to_cpu(cmd.common.nsid);
        auto* ns = backend_ctrl_->get_namespace(nsid);
        
        if (!ns) {
            nvme_completion cqe = {};
            cqe.status = cpu_to_le16(NVME_SC_INVALID_NS << 1);
            completion(cqe);
            return;
        }
        
        // Forward based on opcode
        switch (cmd.common.opcode) {
        case nvme_cmd_read: {
            uint64_t lba = le64_to_cpu(cmd.rw.slba);
            uint16_t num_blocks = le16_to_cpu(cmd.rw.length) + 1;
            
            ns->read_async(lba, num_blocks, data_buffer,
                [completion](int result) {
                    nvme_completion cqe = {};
                    cqe.status = cpu_to_le16(
                        (result == 0 ? 0 : NVME_SC_INTERNAL) << 1);
                    completion(cqe);
                });
            break;
        }
        
        case nvme_cmd_write: {
            uint64_t lba = le64_to_cpu(cmd.rw.slba);
            uint16_t num_blocks = le16_to_cpu(cmd.rw.length) + 1;
            
            ns->write_async(lba, num_blocks, data_buffer,
                [completion](int result) {
                    nvme_completion cqe = {};
                    cqe.status = cpu_to_le16(
                        (result == 0 ? 0 : NVME_SC_INTERNAL) << 1);
                    completion(cqe);
                });
            break;
        }
        
        case nvme_cmd_flush: {
            ns->flush_async([completion](int result) {
                nvme_completion cqe = {};
                cqe.status = cpu_to_le16(
                    (result == 0 ? 0 : NVME_SC_INTERNAL) << 1);
                completion(cqe);
            });
            break;
        }
        
        default: {
            nvme_completion cqe = {};
            cqe.status = cpu_to_le16(NVME_SC_INVALID_OPCODE << 1);
            completion(cqe);
        }
        }
    }
    
    void handle_property_get(const Subsystem& subsys,
                            const nvme_command& cmd,
                            std::function<void(const nvme_completion&)> completion);
    
    void handle_property_set(const Subsystem& subsys,
                            const nvme_command& cmd,
                            std::function<void(const nvme_completion&)> completion);
};

struct NVMeoFQueue {
    uint16_t qid;
    uint16_t size;
    
    // Statistics
    std::atomic<uint64_t> commands_processed;
    std::atomic<uint64_t> bytes_transferred;
    
    NVMeoFQueue(uint16_t id, uint16_t sz) 
        : qid(id), size(sz), commands_processed(0), bytes_transferred(0) {}
};

// RDMA transport for NVMe-oF
class RDMATransport {
private:
    NVMeoFTarget* target_;
    
    struct ib_device* ib_dev_;
    struct ib_pd* pd_;
    struct ib_cq* cq_;
    
    // RDMA connections (one per initiator)
    struct RDMAConnection {
        struct ib_qp* qp;
        std::string host_nqn;
        std::string subsystem_nqn;
        
        // Receive buffers (for commands)
        std::vector<struct ib_mr*> recv_mrs;
        
        // Send buffers (for completions)
        std::vector<struct ib_mr*> send_mrs;
    };
    
    std::vector<std::unique_ptr<RDMAConnection>> connections_;
    std::mutex connections_mutex_;
    
public:
    RDMATransport(NVMeoFTarget* target) : target_(target) {
        // Initialize RDMA device
        struct ib_device** dev_list = ib_get_device_list(nullptr);
        if (!dev_list || !dev_list[0]) {
            throw std::runtime_error("No RDMA devices found");
        }
        
        ib_dev_ = dev_list[0];
        
        // Allocate protection domain
        pd_ = ib_alloc_pd(ib_dev_);
        if (!pd_) {
            throw std::runtime_error("Failed to allocate PD");
        }
        
        // Create completion queue
        cq_ = ib_create_cq(ib_dev_, 128, nullptr, nullptr, 0);
        if (!cq_) {
            throw std::runtime_error("Failed to create CQ");
        }
        
        // Start listening for connections
        listen_for_connections();
    }
    
    void listen_for_connections() {
        // Implementation would use RDMA CM (connection manager)
        // to listen for incoming connection requests
    }
    
    void handle_rdma_completion(struct ib_wc* wc) {
        // Process RDMA work completion
        if (wc->opcode == IB_WC_RECV) {
            // Received NVMe command
            process_received_command(wc);
        } else if (wc->opcode == IB_WC_SEND) {
            // Completion sent
        }
    }
    
    void process_received_command(struct ib_wc* wc) {
        // Extract NVMe command from RDMA buffer
        // Forward to target for processing
    }
};