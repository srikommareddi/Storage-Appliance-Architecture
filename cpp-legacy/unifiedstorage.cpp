#include <memory>
#include <variant>
#include <optional>

// Unified storage platform supporting multiple protocols
class UnityStorageController {
private:
    // Core storage pool (shared by all protocols)
    std::unique_ptr<UnifiedStoragePool> storage_pool_;
    
    // Protocol engines
    std::unique_ptr<BlockProtocolEngine> block_engine_;    // iSCSI, FC, NVMe-oF
    std::unique_ptr<FileProtocolEngine> file_engine_;      // NFS, SMB/CIFS
    std::unique_ptr<ObjectProtocolEngine> object_engine_;  // S3
    
    // Unified namespace
    std::unique_ptr<UnifiedNamespace> namespace_;
    
    // Snapshot manager (protocol-agnostic)
    std::unique_ptr<SnapshotManager> snapshot_mgr_;
    
    // Replication engine
    std::unique_ptr<ReplicationEngine> replication_engine_;
    
    // QoS enforcement
    std::unique_ptr<UnifiedQoS> qos_engine_;
    
    // Data services
    std::unique_ptr<CompressionEngine> compression_;
    std::unique_ptr<DeduplicationEngine> dedup_;
    std::unique_ptr<EncryptionEngine> encryption_;
    
public:
    UnityStorageController(const StorageConfig& config) {
        // Initialize storage pool
        storage_pool_ = std::make_unique<UnifiedStoragePool>(
            config.pool_size, config.raid_type);
        
        // Initialize protocol engines
        block_engine_ = std::make_unique<BlockProtocolEngine>(storage_pool_.get());
        file_engine_ = std::make_unique<FileProtocolEngine>(storage_pool_.get());
        object_engine_ = std::make_unique<ObjectProtocolEngine>(storage_pool_.get());
        
        // Initialize namespace (unified view across protocols)
        namespace_ = std::make_unique<UnifiedNamespace>(storage_pool_.get());
        
        // Initialize data services
        compression_ = std::make_unique<CompressionEngine>(
            CompressionAlgorithm::LZ4);
        dedup_ = std::make_unique<DeduplicationEngine>(
            DedupGranularity::VARIABLE_BLOCK);
        encryption_ = std::make_unique<EncryptionEngine>(
            EncryptionType::AES_256_XTS);
        
        // Initialize snapshot manager
        snapshot_mgr_ = std::make_unique<SnapshotManager>(storage_pool_.get());
        
        // Initialize QoS
        qos_engine_ = std::make_unique<UnifiedQoS>();
    }
    
    // Create LUN (block storage)
    std::string create_lun(const LUNConfig& config) {
        return block_engine_->create_lun(config);
    }
    
    // Create filesystem (file storage)
    std::string create_filesystem(const FilesystemConfig& config) {
        return file_engine_->create_filesystem(config);
    }
    
    // Create bucket (object storage)
    std::string create_bucket(const BucketConfig& config) {
        return object_engine_->create_bucket(config);
    }
    
    // Unified snapshot across protocols
    std::string create_snapshot(const std::string& resource_id, 
                               const std::string& name) {
        return snapshot_mgr_->create_snapshot(resource_id, name);
    }
};

// Unified storage pool (used by all protocols)
class UnifiedStoragePool {
private:
    // Physical storage layout
    struct PoolExtent {
        uint64_t start_lba;
        uint64_t length;
        std::atomic<bool> allocated;
        std::string owner_resource;  // LUN, filesystem, or bucket ID
        ProtocolType protocol;
    };
    
    std::vector<PoolExtent> extents_;
    std::shared_mutex extents_mutex_;
    
    // RAID implementation
    std::unique_ptr<RAIDEngine> raid_engine_;
    
    // Thin provisioning
    struct ThinProvisioningMap {
        std::unordered_map<uint64_t, uint64_t> virtual_to_physical;
        std::shared_mutex mutex;
    };
    
    std::unordered_map<std::string, ThinProvisioningMap> thin_maps_;
    
    // Space management
    uint64_t total_capacity_;
    std::atomic<uint64_t> used_capacity_;
    std::atomic<uint64_t> allocated_capacity_;  // Thin provisioned
    
    // Free space management (bitmap)
    std::vector<std::atomic<uint64_t>> allocation_bitmap_;
    uint64_t block_size_;
    
    // Data services pipeline
    struct DataPipeline {
        bool compression_enabled;
        bool deduplication_enabled;
        bool encryption_enabled;
        
        CompressionEngine* compression;
        DeduplicationEngine* dedup;
        EncryptionEngine* encryption;
    };
    
    DataPipeline pipeline_;
    
public:
    UnifiedStoragePool(uint64_t capacity, RAIDType raid_type) 
        : total_capacity_(capacity), 
          used_capacity_(0),
          allocated_capacity_(0),
          block_size_(4096) {
        
        // Initialize RAID
        raid_engine_ = std::make_unique<RAIDEngine>(raid_type);
        
        // Initialize allocation bitmap
        size_t num_blocks = capacity / block_size_;
        size_t bitmap_size = (num_blocks + 63) / 64;
        allocation_bitmap_.resize(bitmap_size);
        
        for (auto& entry : allocation_bitmap_) {
            entry.store(0);
        }
    }
    
    // Allocate space from pool (protocol-agnostic)
    std::optional<uint64_t> allocate_space(const std::string& resource_id,
                                          uint64_t size,
                                          bool thin = true) {
        uint64_t num_blocks = (size + block_size_ - 1) / block_size_;
        
        if (thin) {
            // Thin provisioning - just reserve virtual space
            allocated_capacity_.fetch_add(size);
            
            // Create thin mapping
            ThinProvisioningMap thin_map;
            thin_maps_[resource_id] = std::move(thin_map);
            
            return 0; // Virtual address
        } else {
            // Thick provisioning - allocate physical space
            std::unique_lock lock(extents_mutex_);
            
            uint64_t start_block = find_free_blocks(num_blocks);
            if (start_block == UINT64_MAX) {
                return std::nullopt; // Out of space
            }
            
            // Mark blocks as allocated
            mark_blocks_allocated(start_block, num_blocks);
            
            // Create extent
            PoolExtent extent = {
                .start_lba = start_block * block_size_,
                .length = size,
                .allocated = true,
                .owner_resource = resource_id
            };
            
            extents_.push_back(extent);
            
            used_capacity_.fetch_add(size);
            allocated_capacity_.fetch_add(size);
            
            return extent.start_lba;
        }
    }
    
    // Write data through pipeline (compression, dedup, encryption)
    int write_with_services(const std::string& resource_id,
                           uint64_t virtual_offset,
                           const void* data,
                           size_t length) {
        
        std::vector<uint8_t> processed_data(
            static_cast<const uint8_t*>(data),
            static_cast<const uint8_t*>(data) + length);
        
        // Apply data services pipeline
        if (pipeline_.deduplication_enabled) {
            // Check if data already exists
            auto fingerprint = pipeline_.dedup->calculate_fingerprint(
                processed_data.data(), processed_data.size());
            
            auto existing_location = pipeline_.dedup->lookup(fingerprint);
            if (existing_location) {
                // Data already exists - just update reference
                return update_thin_mapping(resource_id, virtual_offset, 
                                         *existing_location, true);
            }
        }
        
        if (pipeline_.compression_enabled) {
            processed_data = pipeline_.compression->compress(processed_data);
        }
        
        if (pipeline_.encryption_enabled) {
            processed_data = pipeline_.encryption->encrypt(processed_data);
        }
        
        // Allocate physical space for processed data
        uint64_t physical_offset = allocate_physical_block(processed_data.size());
        
        // Write to physical storage
        int ret = write_physical(physical_offset, processed_data.data(), 
                                processed_data.size());
        
        if (ret < 0) return ret;
        
        // Update thin mapping
        update_thin_mapping(resource_id, virtual_offset, physical_offset, false);
        
        // Update dedup index
        if (pipeline_.deduplication_enabled) {
            auto fingerprint = pipeline_.dedup->calculate_fingerprint(
                static_cast<const uint8_t*>(data), length);
            pipeline_.dedup->insert(fingerprint, physical_offset);
        }
        
        return length;
    }
    
    // Read data through pipeline
    int read_with_services(const std::string& resource_id,
                          uint64_t virtual_offset,
                          void* buffer,
                          size_t length) {
        
        // Lookup physical location via thin mapping
        auto phys_offset_opt = lookup_thin_mapping(resource_id, virtual_offset);
        if (!phys_offset_opt) {
            // Unallocated block - return zeros
            memset(buffer, 0, length);
            return length;
        }
        
        uint64_t physical_offset = *phys_offset_opt;
        
        // Read from physical storage
        std::vector<uint8_t> data(length);
        int ret = read_physical(physical_offset, data.data(), data.size());
        
        if (ret < 0) return ret;
        
        // Reverse pipeline
        if (pipeline_.encryption_enabled) {
            data = pipeline_.encryption->decrypt(data);
        }
        
        if (pipeline_.compression_enabled) {
            data = pipeline_.compression->decompress(data);
        }
        
        memcpy(buffer, data.data(), std::min(data.size(), length));
        return length;
    }
    
    // Space reclamation
    void reclaim_space(const std::string& resource_id) {
        auto it = thin_maps_.find(resource_id);
        if (it == thin_maps_.end()) return;
        
        std::unique_lock lock(it->second.mutex);
        
        // Free all physical blocks
        for (const auto& [virt, phys] : it->second.virtual_to_physical) {
            free_physical_block(phys);
        }
        
        thin_maps_.erase(it);
    }
    
    // Get space statistics
    struct SpaceStats {
        uint64_t total_capacity;
        uint64_t used_capacity;
        uint64_t allocated_capacity;  // Thin provisioned
        uint64_t free_capacity;
        double savings_ratio;  // From compression/dedup
    };
    
    SpaceStats get_space_stats() const {
        SpaceStats stats;
        stats.total_capacity = total_capacity_;
        stats.used_capacity = used_capacity_.load();
        stats.allocated_capacity = allocated_capacity_.load();
        stats.free_capacity = total_capacity_ - used_capacity_.load();
        
        // Calculate savings from compression/dedup
        stats.savings_ratio = 
            static_cast<double>(allocated_capacity_.load()) / 
            static_cast<double>(used_capacity_.load());
        
        return stats;
    }
    
private:
    uint64_t find_free_blocks(uint64_t count);
    void mark_blocks_allocated(uint64_t start, uint64_t count);
    uint64_t allocate_physical_block(size_t size);
    void free_physical_block(uint64_t offset);
    int write_physical(uint64_t offset, const void* data, size_t len);
    int read_physical(uint64_t offset, void* buffer, size_t len);
    
    int update_thin_mapping(const std::string& resource_id,
                           uint64_t virtual_offset,
                           uint64_t physical_offset,
                           bool is_dedup_ref);
    
    std::optional<uint64_t> lookup_thin_mapping(const std::string& resource_id,
                                               uint64_t virtual_offset);
};

// Block protocol engine (iSCSI, FC, NVMe-oF)
class BlockProtocolEngine {
private:
    UnifiedStoragePool* pool_;
    
    struct LUN {
        std::string id;
        std::string name;
        uint64_t capacity;
        uint32_t block_size;
        
        // Multi-protocol support
        std::vector<std::string> iscsi_targets;
        std::vector<uint64_t> fc_wwpns;
        std::vector<std::string> nvmeof_subsystems;
        
        // Host access control
        std::vector<HostAccessRule> access_rules;
        
        // Performance tier
        StorageTier tier;
        
        // Snapshot info
        bool is_snapshot;
        std::string parent_lun;
    };
    
    std::unordered_map<std::string, LUN> luns_;
    std::shared_mutex luns_mutex_;
    
    // iSCSI target
    std::unique_ptr<iSCSITarget> iscsi_target_;
    
    // Fibre Channel initiator
    std::unique_ptr<FCTarget> fc_target_;
    
    // NVMe-oF target
    std::unique_ptr<NVMeoFTarget> nvmeof_target_;
    
public:
    BlockProtocolEngine(UnifiedStoragePool* pool) : pool_(pool) {
        // Initialize protocol handlers
        iscsi_target_ = std::make_unique<iSCSITarget>();
        fc_target_ = std::make_unique<FCTarget>();
        nvmeof_target_ = std::make_unique<NVMeoFTarget>();
    }
    
    std::string create_lun(const LUNConfig& config) {
        std::string lun_id = generate_lun_id();
        
        // Allocate space from pool
        auto offset_opt = pool_->allocate_space(lun_id, config.capacity, 
                                               config.thin_provisioned);
        
        if (!offset_opt) {
            throw std::runtime_error("Failed to allocate space");
        }
        
        LUN lun;
        lun.id = lun_id;
        lun.name = config.name;
        lun.capacity = config.capacity;
        lun.block_size = config.block_size;
        lun.tier = config.tier;
        lun.is_snapshot = false;
        
        // Setup protocol access
        if (config.enable_iscsi) {
            std::string target_iqn = create_iscsi_target(lun_id, config);
            lun.iscsi_targets.push_back(target_iqn);
        }
        
        if (config.enable_fc) {
            uint64_t wwpn = create_fc_target(lun_id, config);
            lun.fc_wwpns.push_back(wwpn);
        }
        
        if (config.enable_nvmeof) {
            std::string nqn = create_nvmeof_subsystem(lun_id, config);
            lun.nvmeof_subsystems.push_back(nqn);
        }
        
        // Store LUN metadata
        {
            std::unique_lock lock(luns_mutex_);
            luns_[lun_id] = std::move(lun);
        }
        
        return lun_id;
    }
    
    // SCSI command processing
    int process_scsi_command(const std::string& lun_id,
                            const SCSICommand& cmd,
                            void* buffer,
                            size_t buffer_size) {
        
        std::shared_lock lock(luns_mutex_);
        auto it = luns_.find(lun_id);
        if (it == luns_.end()) {
            return SCSI_STATUS_CHECK_CONDITION;
        }
        
        const LUN& lun = it->second;
        lock.unlock();
        
        switch (cmd.opcode) {
        case SCSI_READ_10:
        case SCSI_READ_16: {
            uint64_t lba = cmd.lba;
            uint32_t num_blocks = cmd.transfer_length;
            uint64_t offset = lba * lun.block_size;
            size_t length = num_blocks * lun.block_size;
            
            // Read from pool
            int ret = pool_->read_with_services(lun_id, offset, buffer, length);
            return ret >= 0 ? SCSI_STATUS_GOOD : SCSI_STATUS_CHECK_CONDITION;
        }
        
        case SCSI_WRITE_10:
        case SCSI_WRITE_16: {
            uint64_t lba = cmd.lba;
            uint32_t num_blocks = cmd.transfer_length;
            uint64_t offset = lba * lun.block_size;
            size_t length = num_blocks * lun.block_size;
            
            // Write to pool
            int ret = pool_->write_with_services(lun_id, offset, buffer, length);
            return ret >= 0 ? SCSI_STATUS_GOOD : SCSI_STATUS_CHECK_CONDITION;
        }
        
        case SCSI_INQUIRY: {
            // Return LUN information
            SCSIInquiryData* inq = static_cast<SCSIInquiryData*>(buffer);
            memset(inq, 0, sizeof(*inq));
            inq->device_type = 0x00;  // Direct access block device
            inq->version = 0x05;       // SPC-3
            strncpy(inq->vendor_id, "UNITY   ", 8);
            strncpy(inq->product_id, "Virtual LUN     ", 16);
            return SCSI_STATUS_GOOD;
        }
        
        case SCSI_READ_CAPACITY_10:
        case SCSI_READ_CAPACITY_16: {
            // Return capacity
            uint64_t num_blocks = lun.capacity / lun.block_size;
            if (cmd.opcode == SCSI_READ_CAPACITY_10) {
                auto* cap = static_cast<SCSIReadCapacity10Data*>(buffer);
                cap->num_blocks = htonl(std::min(num_blocks, 0xFFFFFFFFULL));
                cap->block_size = htonl(lun.block_size);
            } else {
                auto* cap = static_cast<SCSIReadCapacity16Data*>(buffer);
                cap->num_blocks = htobe64(num_blocks);
                cap->block_size = htonl(lun.block_size);
            }
            return SCSI_STATUS_GOOD;
        }
        
        case SCSI_UNMAP: {
            // TRIM/UNMAP support
            return handle_unmap(lun_id, cmd, buffer);
        }
        
        default:
            return SCSI_STATUS_CHECK_CONDITION;
        }
    }
    
private:
    std::string create_iscsi_target(const std::string& lun_id, 
                                   const LUNConfig& config);
    uint64_t create_fc_target(const std::string& lun_id,
                             const LUNConfig& config);
    std::string create_nvmeof_subsystem(const std::string& lun_id,
                                       const LUNConfig& config);
    int handle_unmap(const std::string& lun_id, const SCSICommand& cmd,
                    void* buffer);
    std::string generate_lun_id();
};

// File protocol engine (NFS, SMB)
class FileProtocolEngine {
private:
    UnifiedStoragePool* pool_;
    
    struct Filesystem {
        std::string id;
        std::string name;
        uint64_t capacity;
        
        // Protocols enabled
        bool nfs_enabled;
        bool smb_enabled;
        
        // NFS exports
        std::vector<NFSExport> nfs_exports;
        
        // SMB shares
        std::vector<SMBShare> smb_shares;
        
        // Namespace root
        std::string root_path;
        
        // Quota
        uint64_t quota_hard_limit;
        uint64_t quota_soft_limit;
        
        // Performance tier
        StorageTier tier;
    };
    
    std::unordered_map<std::string, Filesystem> filesystems_;
    std::shared_mutex fs_mutex_;
    
    // File metadata store (B-tree or similar)
    struct FileMetadata {
        uint64_t inode;
        std::string name;
        FileType type;
        uint64_t size;
        uint32_t uid;
        uint32_t gid;
        uint32_t mode;
        timespec atime;
        timespec mtime;
        timespec ctime;
        
        // Extended attributes
        std::unordered_map<std::string, std::vector<uint8_t>> xattrs;
        
        // Block map (file data location)
        std::vector<FileExtent> extents;
    };
    
    struct FileExtent {
        uint64_t file_offset;
        uint64_t storage_offset;  // In pool
        uint64_t length;
    };
    
    // Inode table
    std::unordered_map<uint64_t, FileMetadata> inode_table_;
    std::shared_mutex inode_mutex_;
    std::atomic<uint64_t> next_inode_;
    
    // Directory structure (path -> inode mapping)
    std::unordered_map<std::string, uint64_t> path_to_inode_;
    std::shared_mutex path_mutex_;
    
    // NFS server
    std::unique_ptr<NFSServer> nfs_server_;
    
    // SMB server
    std::unique_ptr<SMBServer> smb_server_;
    
public:
    FileProtocolEngine(UnifiedStoragePool* pool) 
        : pool_(pool), next_inode_(1) {
        
        // Initialize protocol servers
        nfs_server_ = std::make_unique<NFSServer>(this);
        smb_server_ = std::make_unique<SMBServer>(this);
    }
    
    std::string create_filesystem(const FilesystemConfig& config) {
        std::string fs_id = generate_fs_id();
        
        // Allocate space from pool
        auto offset_opt = pool_->allocate_space(fs_id, config.capacity, true);
        
        if (!offset_opt) {
            throw std::runtime_error("Failed to allocate space");
        }
        
        Filesystem fs;
        fs.id = fs_id;
        fs.name = config.name;
        fs.capacity = config.capacity;
        fs.nfs_enabled = config.enable_nfs;
        fs.smb_enabled = config.enable_smb;
        fs.root_path = "/" + config.name;
        fs.quota_hard_limit = config.capacity;
        fs.quota_soft_limit = config.capacity * 9 / 10;  // 90%
        fs.tier = config.tier;
        
        // Create root directory
        uint64_t root_inode = create_directory("/", 0755);
        
        // Setup NFS export
        if (config.enable_nfs) {
            NFSExport export_config;
            export_config.path = fs.root_path;
            export_config.options = "rw,sync,no_root_squash";
            fs.nfs_exports.push_back(export_config);
            
            nfs_server_->add_export(export_config);
        }
        
        // Setup SMB share
        if (config.enable_smb) {
            SMBShare share;
            share.name = config.name;
            share.path = fs.root_path;
            share.read_only = false;
            fs.smb_shares.push_back(share);
            
            smb_server_->add_share(share);
        }
        
        // Store filesystem metadata
        {
            std::unique_lock lock(fs_mutex_);
            filesystems_[fs_id] = std::move(fs);
        }
        
        return fs_id;
    }
    
    // File operations
    int create_file(const std::string& fs_id, const std::string& path,
                   uint32_t mode, uint32_t uid, uint32_t gid) {
        
        uint64_t inode = next_inode_.fetch_add(1);
        
        FileMetadata metadata;
        metadata.inode = inode;
        metadata.name = basename(path);
        metadata.type = FileType::REGULAR;
        metadata.size = 0;
        metadata.uid = uid;
        metadata.gid = gid;
        metadata.mode = mode;
        
        auto now = current_timespec();
        metadata.atime = now;
        metadata.mtime = now;
        metadata.ctime = now;
        
        // Store in inode table
        {
            std::unique_lock lock(inode_mutex_);
            inode_table_[inode] = metadata;
        }
        
        // Add to directory
        {
            std::unique_lock lock(path_mutex_);
            path_to_inode_[path] = inode;
        }
        
        return 0;
    }
    
    ssize_t write_file(const std::string& fs_id, const std::string& path,
                      const void* data, size_t length, off_t offset) {
        
        // Lookup inode
        uint64_t inode;
        {
            std::shared_lock lock(path_mutex_);
            auto it = path_to_inode_.find(path);
            if (it == path_to_inode_.end()) {
                return -ENOENT;
            }
            inode = it->second;
        }
        
        // Get file metadata
        std::unique_lock meta_lock(inode_mutex_);
        auto meta_it = inode_table_.find(inode);
        if (meta_it == inode_table_.end()) {
            return -ENOENT;
        }
        
        FileMetadata& metadata = meta_it->second;
        
        // Allocate extents if needed
        uint64_t end_offset = offset + length;
        if (end_offset > metadata.size) {
            // Extend file
            allocate_extents(metadata, offset, length);
            metadata.size = end_offset;
        }
        
        // Find extent covering this offset
        FileExtent* extent = find_extent(metadata, offset);
        if (!extent) {
            return -EIO;
        }
        
        meta_lock.unlock();
        
        // Write to storage pool
        uint64_t storage_offset = extent->storage_offset + 
                                 (offset - extent->file_offset);
        
        std::string resource_id = fs_id + ":" + std::to_string(inode);
        int ret = pool_->write_with_services(resource_id, storage_offset, 
                                            data, length);
        
        if (ret >= 0) {
            // Update mtime
            meta_lock.lock();
            metadata.mtime = current_timespec();
        }
        
        return ret;
    }
    
    ssize_t read_file(const std::string& fs_id, const std::string& path,
                     void* buffer, size_t length, off_t offset) {
        
        // Lookup inode
        uint64_t inode;
        {
            std::shared_lock lock(path_mutex_);
            auto it = path_to_inode_.find(path);
            if (it == path_to_inode_.end()) {
                return -ENOENT;
            }
            inode = it->second;
        }
        
        // Get file metadata
        std::shared_lock meta_lock(inode_mutex_);
        auto meta_it = inode_table_.find(inode);
        if (meta_it == inode_table_.end()) {
            return -ENOENT;
        }
        
        const FileMetadata& metadata = meta_it->second;
        
        // Check bounds
        if (offset >= static_cast<off_t>(metadata.size)) {
            return 0;  // EOF
        }
        
        size_t to_read = std::min(length, 
                                 static_cast<size_t>(metadata.size - offset));
        
        // Find extent
        const FileExtent* extent = find_extent_const(metadata, offset);
        if (!extent) {
            return -EIO;
        }
        
        meta_lock.unlock();
        
        // Read from storage pool
        uint64_t storage_offset = extent->storage_offset + 
                                 (offset - extent->file_offset);
        
        std::string resource_id = fs_id + ":" + std::to_string(inode);
        return pool_->read_with_services(resource_id, storage_offset, 
                                        buffer, to_read);
    }
    
    // NFS v3 operations
    int nfs_lookup(const std::string& dir_path, const std::string& name,
                  uint64_t& inode_out) {
        std::string full_path = dir_path + "/" + name;
        
        std::shared_lock lock(path_mutex_);
        auto it = path_to_inode_.find(full_path);
        if (it == path_to_inode_.end()) {
            return -ENOENT;
        }
        
        inode_out = it->second;
        return 0;
    }
    
    int nfs_getattr(uint64_t inode, struct stat& st) {
        std::shared_lock lock(inode_mutex_);
        auto it = inode_table_.find(inode);
        if (it == inode_table_.end()) {
            return -ENOENT;
        }
        
        const FileMetadata& meta = it->second;
        
        memset(&st, 0, sizeof(st));
        st.st_ino = meta.inode;
        st.st_mode = meta.mode;
        st.st_uid = meta.uid;
        st.st_gid = meta.gid;
        st.st_size = meta.size;
        st.st_atim = meta.atime;
        st.st_mtim = meta.mtime;
        st.st_ctim = meta.ctime;
        
        return 0;
    }
    
private:
    uint64_t create_directory(const std::string& path, uint32_t mode);
    void allocate_extents(FileMetadata& metadata, uint64_t offset, 
                         size_t length);
    FileExtent* find_extent(FileMetadata& metadata, uint64_t offset);
    const FileExtent* find_extent_const(const FileMetadata& metadata, 
                                       uint64_t offset) const;
    std::string generate_fs_id();
    std::string basename(const std::string& path);
    timespec current_timespec();
};