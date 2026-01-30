#include <linux/nvme.h>
#include <spdk/nvme.h>
#include <spdk/nvmf.h>
#include <spdk/env.h>

// NVMe-oF target for high-performance block storage
class NVMeoFTarget {
private:
    struct spdk_nvmf_tgt* tgt_;
    struct spdk_nvmf_transport* transport_;
    struct spdk_nvmf_subsystem* subsystem_;
    
    // Multi-protocol support (NVMe-oF, iSCSI, FC)
    enum TransportType {
        NVME_RDMA,
        NVME_TCP,
        ISCSI,
        FC
    };
    
    // Storage controller state
    struct ControllerState {
        uint16_t controller_id;
        std::atomic<uint64_t> io_count;
        std::atomic<uint64_t> bytes_read;
        std::atomic<uint64_t> bytes_written;
        std::chrono::steady_clock::time_point last_io;
    };
    
    std::unordered_map<uint16_t, ControllerState> controllers_;
    std::shared_mutex controllers_mutex_;
    
    // QoS management
    struct QoSPolicy {
        uint64_t max_iops;
        uint64_t max_bandwidth;  // bytes/sec
        uint32_t priority;       // 0-7
        bool burst_allowed;
    };
    
    std::unordered_map<std::string, QoSPolicy> qos_policies_;
    
public:
    NVMeoFTarget(TransportType type = NVME_RDMA) {
        // Initialize SPDK environment
        struct spdk_env_opts opts;
        spdk_env_opts_init(&opts);
        opts.name = "nvmeof_target";
        opts.shm_id = 0;
        
        if (spdk_env_init(&opts) < 0) {
            throw std::runtime_error("Failed to initialize SPDK");
        }
        
        // Create NVMe-oF target
        tgt_ = spdk_nvmf_tgt_create(NULL);
        if (!tgt_) {
            throw std::runtime_error("Failed to create NVMe-oF target");
        }
        
        // Setup transport
        setup_transport(type);
    }
    
    ~NVMeoFTarget() {
        if (subsystem_) {
            spdk_nvmf_subsystem_destroy(subsystem_);
        }
        if (tgt_) {
            spdk_nvmf_tgt_destroy(tgt_);
        }
    }
    
    void setup_transport(TransportType type) {
        struct spdk_nvmf_transport_opts opts = {};
        
        switch (type) {
        case NVME_RDMA:
            opts.max_queue_depth = 128;
            opts.max_qpairs_per_ctrlr = 64;
            opts.in_capsule_data_size = 4096;
            opts.max_io_size = 131072;
            opts.io_unit_size = 131072;
            opts.max_aq_depth = 128;
            opts.num_shared_buffers = 4095;
            break;
            
        case NVME_TCP:
            opts.max_queue_depth = 128;
            opts.max_qpairs_per_ctrlr = 16;
            opts.in_capsule_data_size = 8192;
            opts.max_io_size = 131072;
            opts.io_unit_size = 131072;
            break;
        }
        
        transport_ = spdk_nvmf_tgt_get_transport(tgt_, "RDMA");
        if (!transport_) {
            throw std::runtime_error("Failed to get transport");
        }
    }
    
    // Create subsystem (analogous to LUN in traditional storage)
    bool create_subsystem(const std::string& nqn, 
                         const std::string& serial_number,
                         uint64_t capacity_bytes) {
        
        subsystem_ = spdk_nvmf_subsystem_create(tgt_, nqn.c_str(),
                                                SPDK_NVMF_SUBTYPE_NVME,
                                                0);
        if (!subsystem_) {
            return false;
        }
        
        // Set subsystem properties
        spdk_nvmf_subsystem_set_sn(subsystem_, serial_number.c_str());
        spdk_nvmf_subsystem_set_mn(subsystem_, "PowerStore Emulator");
        
        // Allow any host to connect (in production, use authentication)
        spdk_nvmf_subsystem_set_allow_any_host(subsystem_, true);
        
        // Add namespace
        struct spdk_nvmf_ns_opts ns_opts;
        spdk_nvmf_ns_opts_get_defaults(&ns_opts, sizeof(ns_opts));
        ns_opts.nsid = 1;
        
        // In real implementation, this would point to actual storage backend
        // For now, create a bdev (block device)
        
        return true;
    }
    
    // Add listener (IP endpoint)
    bool add_listener(const std::string& traddr, uint16_t trsvcid) {
        struct spdk_nvmf_listen_opts opts = {};
        opts.traddr = traddr.c_str();
        
        char port_str[16];
        snprintf(port_str, sizeof(port_str), "%u", trsvcid);
        opts.trsvcid = port_str;
        
        if (spdk_nvmf_subsystem_add_listener(subsystem_, &opts) != 0) {
            return false;
        }
        
        return true;
    }
    
    // QoS enforcement
    void apply_qos(uint16_t controller_id, const std::string& policy_name) {
        auto policy_it = qos_policies_.find(policy_name);
        if (policy_it == qos_policies_.end()) {
            return;
        }
        
        const QoSPolicy& policy = policy_it->second;
        
        // In real implementation, this would configure rate limiting
        // in the I/O path using token bucket or similar algorithm
    }
    
    // I/O completion callback
    static void io_complete(void* cb_arg, const struct spdk_nvme_cpl* cpl) {
        auto* io_ctx = static_cast<IOContext*>(cb_arg);
        
        if (spdk_nvme_cpl_is_error(cpl)) {
            io_ctx->status = -EIO;
        } else {
            io_ctx->status = 0;
        }
        
        // Update statistics
        io_ctx->controller->io_count.fetch_add(1);
        if (io_ctx->is_read) {
            io_ctx->controller->bytes_read.fetch_add(io_ctx->length);
        } else {
            io_ctx->controller->bytes_written.fetch_add(io_ctx->length);
        }
        
        // Invoke user callback
        if (io_ctx->callback) {
            io_ctx->callback(io_ctx);
        }
        
        delete io_ctx;
    }
    
    // Get statistics
    ControllerStats get_stats(uint16_t controller_id) {
        std::shared_lock lock(controllers_mutex_);
        
        auto it = controllers_.find(controller_id);
        if (it == controllers_.end()) {
            return {};
        }
        
        const auto& state = it->second;
        return {
            .io_count = state.io_count.load(),
            .bytes_read = state.bytes_read.load(),
            .bytes_written = state.bytes_written.load(),
            .avg_latency_us = calculate_avg_latency(controller_id)
        };
    }
    
private:
    struct IOContext {
        ControllerState* controller;
        uint64_t lba;
        uint32_t length;
        bool is_read;
        int status;
        std::function<void(IOContext*)> callback;
    };
    
    uint64_t calculate_avg_latency(uint16_t controller_id);
};

// Active-Active Storage Cluster (like PowerStore X)
class ActiveActiveCluster {
private:
    struct ClusterNode {
        std::string node_id;
        std::string ip_address;
        bool is_active;
        std::atomic<bool> is_healthy;
        
        // Node-local resources
        std::vector<std::string> owned_volumes;
        std::unique_ptr<NVMeoFTarget> nvmeof_target;
        
        // Heartbeat
        std::chrono::steady_clock::time_point last_heartbeat;
    };
    
    std::vector<ClusterNode> nodes_;
    std::shared_mutex nodes_mutex_;
    
    // Distributed lock manager for coordinating volume ownership
    std::unique_ptr<DistributedLockManager> lock_manager_;
    
    // Metadata replication
    std::unique_ptr<MetadataReplicator> metadata_replicator_;
    
    // Volume routing table
    struct VolumeRoute {
        std::string volume_id;
        std::vector<std::string> owner_nodes;  // Primary and secondary
        uint32_t generation;                    // For split-brain detection
    };
    
    std::unordered_map<std::string, VolumeRoute> routing_table_;
    std::shared_mutex routing_mutex_;
    
public:
    ActiveActiveCluster(const std::vector<std::string>& node_addresses) {
        // Initialize cluster nodes
        for (size_t i = 0; i < node_addresses.size(); ++i) {
            ClusterNode node;
            node.node_id = generate_node_id();
            node.ip_address = node_addresses[i];
            node.is_active = true;
            node.is_healthy = true;
            node.last_heartbeat = std::chrono::steady_clock::now();
            
            nodes_.push_back(std::move(node));
        }
        
        // Initialize distributed lock manager (using etcd or similar)
        lock_manager_ = std::make_unique<DistributedLockManager>(node_addresses);
        
        // Initialize metadata replicator
        metadata_replicator_ = std::make_unique<MetadataReplicator>(nodes_);
        
        // Start heartbeat monitoring
        start_heartbeat_monitor();
    }
    
    // Create volume with dual ownership (active-active)
    std::string create_volume(uint64_t size_bytes, 
                             const std::string& name,
                             uint32_t replication_factor = 2) {
        
        std::string volume_id = generate_volume_id();
        
        // Select owner nodes using consistent hashing
        std::vector<std::string> owner_nodes = select_owner_nodes(
            volume_id, replication_factor);
        
        // Create volume on each owner node
        for (const auto& node_id : owner_nodes) {
            create_volume_on_node(node_id, volume_id, size_bytes);
        }
        
        // Update routing table
        VolumeRoute route;
        route.volume_id = volume_id;
        route.owner_nodes = owner_nodes;
        route.generation = 1;
        
        {
            std::unique_lock lock(routing_mutex_);
            routing_table_[volume_id] = route;
        }
        
        // Replicate metadata
        metadata_replicator_->replicate_volume_metadata(volume_id, route);
        
        return volume_id;
    }
    
    // I/O path - route to appropriate node
    int write_volume(const std::string& volume_id, uint64_t offset,
                    const void* data, size_t length) {
        
        // Get volume route
        VolumeRoute route;
        {
            std::shared_lock lock(routing_mutex_);
            auto it = routing_table_.find(volume_id);
            if (it == routing_table_.end()) {
                return -ENOENT;
            }
            route = it->second;
        }
        
        // Write to all replica nodes in parallel (active-active)
        std::vector<std::future<int>> futures;
        
        for (const auto& node_id : route.owner_nodes) {
            futures.push_back(std::async(std::launch::async,
                [this, node_id, volume_id, offset, data, length]() {
                    return write_to_node(node_id, volume_id, offset, data, length);
                }));
        }
        
        // Wait for quorum (n/2 + 1)
        size_t required_acks = (route.owner_nodes.size() / 2) + 1;
        size_t successful_writes = 0;
        
        for (auto& future : futures) {
            try {
                int result = future.get();
                if (result >= 0) {
                    successful_writes++;
                }
            } catch (...) {
                // Handle node failure
            }
        }
        
        if (successful_writes >= required_acks) {
            return length;
        }
        
        return -EIO;
    }
    
    int read_volume(const std::string& volume_id, uint64_t offset,
                   void* buffer, size_t length) {
        
        // Get volume route
        VolumeRoute route;
        {
            std::shared_lock lock(routing_mutex_);
            auto it = routing_table_.find(volume_id);
            if (it == routing_table_.end()) {
                return -ENOENT;
            }
            route = it->second;
        }
        
        // Read from closest healthy node
        for (const auto& node_id : route.owner_nodes) {
            if (is_node_healthy(node_id)) {
                return read_from_node(node_id, volume_id, offset, buffer, length);
            }
        }
        
        return -EIO;
    }
    
    // Failure handling and failover
    void handle_node_failure(const std::string& node_id) {
        std::unique_lock lock(nodes_mutex_);
        
        auto node_it = std::find_if(nodes_.begin(), nodes_.end(),
            [&node_id](const ClusterNode& n) { return n.node_id == node_id; });
        
        if (node_it == nodes_.end()) return;
        
        node_it->is_healthy = false;
        
        // Identify affected volumes
        std::vector<std::string> affected_volumes = node_it->owned_volumes;
        
        lock.unlock();
        
        // Initiate failover for each affected volume
        for (const auto& volume_id : affected_volumes) {
            failover_volume(volume_id, node_id);
        }
    }
    
private:
    void failover_volume(const std::string& volume_id, 
                        const std::string& failed_node) {
        
        std::unique_lock lock(routing_mutex_);
        
        auto it = routing_table_.find(volume_id);
        if (it == routing_table_.end()) return;
        
        VolumeRoute& route = it->second;
        
        // Remove failed node from owners
        route.owner_nodes.erase(
            std::remove(route.owner_nodes.begin(), route.owner_nodes.end(), 
                       failed_node),
            route.owner_nodes.end());
        
        // Select new owner node
        std::string new_owner = select_replacement_node(volume_id, failed_node);
        route.owner_nodes.push_back(new_owner);
        route.generation++;
        
        lock.unlock();
        
        // Trigger data rebuild on new owner
        rebuild_volume_replica(volume_id, new_owner, route.owner_nodes[0]);
    }
    
    void rebuild_volume_replica(const std::string& volume_id,
                               const std::string& target_node,
                               const std::string& source_node) {
        // Background task to copy data from source to target
        std::thread([this, volume_id, target_node, source_node]() {
            // Get volume metadata
            auto metadata = metadata_replicator_->get_volume_metadata(volume_id);
            
            uint64_t offset = 0;
            const size_t chunk_size = 1024 * 1024; // 1MB chunks
            std::vector<uint8_t> buffer(chunk_size);
            
            while (offset < metadata.size) {
                size_t to_read = std::min(chunk_size, 
                                         static_cast<size_t>(metadata.size - offset));
                
                // Read from source
                read_from_node(source_node, volume_id, offset, 
                             buffer.data(), to_read);
                
                // Write to target
                write_to_node(target_node, volume_id, offset,
                            buffer.data(), to_read);
                
                offset += to_read;
                
                // Update rebuild progress
                update_rebuild_progress(volume_id, offset, metadata.size);
            }
            
            // Mark rebuild complete
            complete_rebuild(volume_id, target_node);
        }).detach();
    }
    
    void start_heartbeat_monitor() {
        std::thread([this]() {
            while (true) {
                std::this_thread::sleep_for(std::chrono::seconds(1));
                
                std::shared_lock lock(nodes_mutex_);
                
                auto now = std::chrono::steady_clock::now();
                
                for (auto& node : nodes_) {
                    auto elapsed = std::chrono::duration_cast<std::chrono::seconds>(
                        now - node.last_heartbeat).count();
                    
                    if (elapsed > 5 && node.is_healthy.load()) {
                        // Node appears to have failed
                        lock.unlock();
                        handle_node_failure(node.node_id);
                        lock.lock();
                    }
                }
            }
        }).detach();
    }
    
    std::vector<std::string> select_owner_nodes(const std::string& volume_id,
                                                uint32_t count);
    std::string select_replacement_node(const std::string& volume_id,
                                       const std::string& failed_node);
    bool is_node_healthy(const std::string& node_id);
    int write_to_node(const std::string& node_id, const std::string& volume_id,
                     uint64_t offset, const void* data, size_t length);
    int read_from_node(const std::string& node_id, const std::string& volume_id,
                      uint64_t offset, void* buffer, size_t length);
    void create_volume_on_node(const std::string& node_id, 
                              const std::string& volume_id, uint64_t size);
    void update_rebuild_progress(const std::string& volume_id, 
                                uint64_t current, uint64_t total);
    void complete_rebuild(const std::string& volume_id, 
                         const std::string& node_id);
    std::string generate_node_id();
    std::string generate_volume_id();
};