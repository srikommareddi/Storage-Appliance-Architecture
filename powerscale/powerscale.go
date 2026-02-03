package powerscale

import (
    "context"
    "crypto/sha256"
    "fmt"
    "hash/fnv"
    "io"
    "sync"
    "time"

    "github.com/klauspost/reedsolomon"
	"github.com/srilakshmi/storage/dpu"
)

// OneFS-inspired distributed filesystem
type OneFS struct {
    // Cluster configuration
    nodes           []*Node
    nodeCount       int
    
    // Protection level (N+M encoding)
    dataStripes     int  // N
    parityStripes   int  // M
    
    // Layout algorithm (striping across nodes)
    stripeWidth     int64
    
    // Distributed lock service
    lockService     *DistributedLockService
    
    // Transaction log for consistency
    txnLog          *TransactionLog
    
    // SmartPools-style tiering
    storagePools    map[string]*StoragePool
    
    // Global namespace
    namespace       *DistributedNamespace

	// Optional DPU erasure coding offload
	dpuEncoder      DPUEncoder

	// Optional DPU client for offload orchestration
	dpuClient       *dpu.Client

	// Guards DPU encoder/client updates
	dpuMu           sync.RWMutex
}

type Node struct {
    ID              int
    Hostname        string
    IPAddress       string
    
    // Local storage
    diskGroups      []*DiskGroup
    availableSpace  int64
    totalSpace      int64
    
    // Node health
    healthy         bool
    lastHeartbeat   time.Time
    
    // Cache
    localCache      *NodeCache
}

type DiskGroup struct {
    ID              string
    Disks           []string
    TierType        TierType  // SSD, SAS, SATA, Archive
    TotalCapacity   int64
    UsedCapacity    int64
    FaultDomain     int       // For failure isolation
}

type TierType int

const (
    TierSSD TierType = iota
    TierSAS
    TierSATA
    TierArchive
)

// SmartPools - automated tiering
type StoragePool struct {
    Name            string
    ID              string
    
    // Protection policy
    Protection      ProtectionLevel
    
    // Tiering policy
    TierPreference  []TierType
    AutoTiering     bool
    
    // Performance SLO
    MinIOPS         int64
    MaxIOPS         int64
    
    // Nodes participating in this pool
    NodeIDs         []int
}

type ProtectionLevel struct {
    DataStripes     int
    ParityStripes   int
    Description     string  // e.g., "N+2:1" (2 parity, 1 spare)
}

// File layout - how data is distributed across cluster
type FileLayout struct {
    InodeNumber     uint64
    FileSize        int64
    BlockSize       int32
    
    // Protection level for this file
    Protection      ProtectionLevel
    
    // Block map
    Blocks          []BlockDescriptor
    
    // Metadata
    Owner           uint32
    Group           uint32
    Mode            uint32
    AccessTime      time.Time
    ModifyTime      time.Time
    ChangeTime      time.Time
    
    // Extended attributes
    ExtendedAttrs   map[string][]byte
}

type BlockDescriptor struct {
    BlockNumber     int64
    
    // Stripe layout (which nodes hold which pieces)
    StripeMembers   []StripeMember
    
    // Protection info
    DataShards      []int  // Node IDs holding data shards
    ParityShards    []int  // Node IDs holding parity shards
    
    // Checksum for integrity
    Checksum        []byte
    ChecksumType    string  // "sha256", "crc32c"
}

type StripeMember struct {
    NodeID          int
    LocalPath       string
    ShardIndex      int
    IsParityShard   bool
}

// Distributed namespace (directory tree)
type DistributedNamespace struct {
    root            *DirectoryInode
    inodeTable      sync.Map  // inode -> DirectoryInode or FileInode
    pathCache       *LRUCache // path -> inode (cache)

    // Directory sharding for scale
    shardCount      int
    nextInode       uint64
    mu              sync.Mutex
}

type DirectoryInode struct {
    InodeNumber     uint64
    Name            string
    Parent          uint64
    
    // Entries in this directory
    Entries         sync.Map  // name -> inode
    
    // Which node "owns" this directory
    OwnerNode       int
    
    // For very large directories, shard across multiple nodes
    IsSharded       bool
    ShardMap        map[uint64]int  // hash -> node
    
    // Metadata
    Mode            uint32
    Owner           uint32
    Group           uint32
    CreateTime      time.Time
    ModifyTime      time.Time
}

func NewOneFS(nodes []*Node, dataStripes, parityStripes int) *OneFS {
    ofs := &OneFS{
        nodes:         nodes,
        nodeCount:     len(nodes),
        dataStripes:   dataStripes,
        parityStripes: parityStripes,
        stripeWidth:   1024 * 1024, // 1MB stripe unit
        lockService:   NewDistributedLockService(nodes),
        txnLog:        NewTransactionLog(),
        storagePools:  make(map[string]*StoragePool),
    }
    
    // Initialize namespace
    ofs.namespace = NewDistributedNamespace(nodes)
    
    // Create default storage pool
    defaultPool := &StoragePool{
        Name: "default",
        Protection: ProtectionLevel{
            DataStripes:   dataStripes,
            ParityStripes: parityStripes,
            Description:   fmt.Sprintf("N+%d:1", parityStripes),
        },
        TierPreference: []TierType{TierSSD, TierSAS, TierSATA},
        AutoTiering:    true,
    }
    
    ofs.storagePools["default"] = defaultPool
    
    return ofs
}

// Write file with erasure coding across cluster
func (ofs *OneFS) WriteFile(ctx context.Context, path string, 
                           reader io.Reader, size int64) error {
    
    // Allocate inode
    inode := ofs.namespace.AllocateInode()
    
    // Determine protection level (from SmartPool policy)
    pool := ofs.selectStoragePool(path, size)
    protection := pool.Protection
    
    // Calculate number of stripes needed
    stripeSize := ofs.stripeWidth * int64(protection.DataStripes)
    numStripes := (size + stripeSize - 1) / stripeSize
    
    // Create file layout
    layout := &FileLayout{
        InodeNumber: inode,
        FileSize:    size,
        BlockSize:   int32(ofs.stripeWidth),
        Protection:  protection,
        Blocks:      make([]BlockDescriptor, numStripes),
    }
    
    // Write each stripe
    buffer := make([]byte, stripeSize)
    
    for stripeNum := int64(0); stripeNum < numStripes; stripeNum++ {
        // Read stripe worth of data
        n, err := io.ReadFull(reader, buffer)
        if err != nil && err != io.ErrUnexpectedEOF {
            return err
        }
        
        // Encode with erasure coding
        shards, err := ofs.encodeStripe(buffer[:n], protection)
        if err != nil {
            return err
        }
        
        // Select nodes for stripe placement
        nodes := ofs.selectNodesForStripe(stripeNum, protection)
        
        // Write shards to nodes in parallel
        blockDesc, err := ofs.writeStripeToNodes(ctx, shards, nodes, stripeNum)
        if err != nil {
            return err
        }
        
        layout.Blocks[stripeNum] = *blockDesc
    }
    
    // Commit file metadata
    if err := ofs.namespace.CommitFile(path, layout); err != nil {
        return err
    }
    
    return nil
}

func (ofs *OneFS) ReadFile(ctx context.Context, path string, 
                          writer io.Writer) error {
    
    // Lookup file layout
    layout, err := ofs.namespace.LookupFile(path)
    if err != nil {
        return err
    }
    
    // Read each stripe
    for _, block := range layout.Blocks {
        // Read shards from nodes
        shards, err := ofs.readStripeFromNodes(ctx, block)
        if err != nil {
            return err
        }
        
        // Decode stripe
        data, err := ofs.decodeStripe(shards, layout.Protection)
        if err != nil {
            return err
        }
        
        // Write to output
        if _, err := writer.Write(data); err != nil {
            return err
        }
    }
    
    return nil
}

// Stripe encoding with Reed-Solomon
func (ofs *OneFS) encodeStripe(data []byte, protection ProtectionLevel) ([][]byte, error) {
	ofs.dpuMu.RLock()
	encoder := ofs.dpuEncoder
	ofs.dpuMu.RUnlock()
	if encoder != nil {
		return encoder.Encode(data, protection)
	}

    totalShards := protection.DataStripes + protection.ParityStripes
    
    // Create Reed-Solomon encoder
    enc, err := reedsolomon.New(protection.DataStripes, protection.ParityStripes)
    if err != nil {
        return nil, err
    }
    
    // Calculate shard size
    shardSize := (len(data) + protection.DataStripes - 1) / protection.DataStripes
    
    // Create shards
    shards := make([][]byte, totalShards)
    for i := 0; i < protection.DataStripes; i++ {
        shards[i] = make([]byte, shardSize)
        start := i * shardSize
        end := start + shardSize
        if end > len(data) {
            end = len(data)
        }
        copy(shards[i], data[start:end])
    }
    
    // Create parity shards
    for i := protection.DataStripes; i < totalShards; i++ {
        shards[i] = make([]byte, shardSize)
    }
    
    // Encode
    if err := enc.Encode(shards); err != nil {
        return nil, err
    }
    
    return shards, nil
}

// Node selection algorithm (ensures failure domain diversity)
func (ofs *OneFS) selectNodesForStripe(stripeNum int64, 
                                      protection ProtectionLevel) []int {
    
    totalShards := protection.DataStripes + protection.ParityStripes
    selectedNodes := make([]int, totalShards)
    usedFaultDomains := make(map[int]bool)
    
    // Hash-based deterministic placement with failure domain awareness
    for i := 0; i < totalShards; i++ {
        hash := fnv.New64a()
        hash.Write([]byte(fmt.Sprintf("%d-%d", stripeNum, i)))
        nodeIndex := int(hash.Sum64() % uint64(ofs.nodeCount))
        
        // Ensure different fault domains
        node := ofs.nodes[nodeIndex]
        faultDomain := ofs.getFaultDomain(node)
        
        // If fault domain already used, find next available
        attempts := 0
        for usedFaultDomains[faultDomain] && attempts < ofs.nodeCount {
            nodeIndex = (nodeIndex + 1) % ofs.nodeCount
            node = ofs.nodes[nodeIndex]
            faultDomain = ofs.getFaultDomain(node)
            attempts++
        }
        
        selectedNodes[i] = nodeIndex
        usedFaultDomains[faultDomain] = true
    }
    
    return selectedNodes
}

// Write shards to selected nodes
func (ofs *OneFS) writeStripeToNodes(ctx context.Context, shards [][]byte, 
                                    nodes []int, stripeNum int64) (*BlockDescriptor, error) {
    
    block := &BlockDescriptor{
        BlockNumber:  stripeNum,
        DataShards:   make([]int, 0, len(shards)),
        ParityShards: make([]int, 0, len(shards)),
        StripeMembers: make([]StripeMember, len(shards)),
    }

    // Write shards in parallel
    var wg sync.WaitGroup
    var blockMu sync.Mutex  // Protect block descriptor updates
    errChan := make(chan error, len(shards))

    for i, shard := range shards {
        wg.Add(1)
        go func(shardIdx int, shardData []byte, nodeID int) {
            defer wg.Done()

            isParity := shardIdx >= ofs.dataStripes

            // Generate local path for shard
            localPath := ofs.generateShardPath(stripeNum, shardIdx, nodeID)

            // Write shard to node
            if err := ofs.writeShardToNode(ctx, nodeID, localPath, shardData); err != nil {
                errChan <- err
                return
            }

            // Update block descriptor under lock
            member := StripeMember{
                NodeID:       nodeID,
                LocalPath:    localPath,
                ShardIndex:   shardIdx,
                IsParityShard: isParity,
            }

            blockMu.Lock()
            block.StripeMembers[shardIdx] = member

            if isParity {
                block.ParityShards = append(block.ParityShards, nodeID)
            } else {
                block.DataShards = append(block.DataShards, nodeID)
            }
            blockMu.Unlock()

        }(i, shard, nodes[i])
    }

    wg.Wait()
    close(errChan)
    
    // Check for errors
    for err := range errChan {
        if err != nil {
            return nil, err
        }
    }
    
    // Calculate checksum of entire stripe
    block.Checksum = ofs.calculateStripeChecksum(shards)
    block.ChecksumType = "sha256"
    
    return block, nil
}

// Automatic healing when node fails
func (ofs *OneFS) HealCluster(ctx context.Context) {
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            ofs.checkAndHealFiles()
        }
    }
}

func (ofs *OneFS) checkAndHealFiles() {
    // Scan all files and check if they need healing
    ofs.namespace.WalkFiles(func(path string, layout *FileLayout) error {
        // Check each block
        for i, block := range layout.Blocks {
            needsHealing, failedNodes := ofs.checkBlockHealth(block)
            
            if needsHealing {
                // Reconstruct missing shards
                if err := ofs.healBlock(&layout.Blocks[i], failedNodes); err != nil {
                    fmt.Printf("Failed to heal block %d: %v\n", i, err)
                }
            }
        }
        return nil
    })
}

func (ofs *OneFS) checkBlockHealth(block BlockDescriptor) (bool, []int) {
    failedNodes := make([]int, 0)
    
    for _, member := range block.StripeMembers {
        node := ofs.nodes[member.NodeID]
        if !node.healthy {
            failedNodes = append(failedNodes, member.NodeID)
        }
    }
    
    // Need healing if we have failures but still within tolerance
    maxFailures := len(block.ParityShards)
    return len(failedNodes) > 0 && len(failedNodes) <= maxFailures, failedNodes
}

func (ofs *OneFS) healBlock(block *BlockDescriptor, failedNodes []int) error {
    // Read available shards
    shards := make([][]byte, len(block.StripeMembers))
    
    for i, member := range block.StripeMembers {
        if !contains(failedNodes, member.NodeID) {
            // Read shard from healthy node
            shard, err := ofs.readShardFromNode(member.NodeID, member.LocalPath)
            if err != nil {
                return err
            }
            shards[i] = shard
        } else {
            shards[i] = nil
        }
    }
    
    // Reconstruct missing shards
    enc, _ := reedsolomon.New(ofs.dataStripes, ofs.parityStripes)
    if err := enc.Reconstruct(shards); err != nil {
        return err
    }
    
    // Write reconstructed shards to new nodes
    for _, failedNodeID := range failedNodes {
        // Find which shard was on the failed node
        for i, member := range block.StripeMembers {
            if member.NodeID == failedNodeID {
                // Select new node
                newNodeID := ofs.selectReplacementNode(failedNodeID)
                
                // Write reconstructed shard
                localPath := ofs.generateShardPath(block.BlockNumber, i, newNodeID)
                if err := ofs.writeShardToNode(context.Background(), 
                    newNodeID, localPath, shards[i]); err != nil {
                    return err
                }
                
                // Update block descriptor
                block.StripeMembers[i].NodeID = newNodeID
                block.StripeMembers[i].LocalPath = localPath
            }
        }
    }
    
    return nil
}

func (ofs *OneFS) getFaultDomain(node *Node) int {
    // In real implementation, this would use rack/datacenter topology
    // For simplicity, use node ID mod some factor
    return node.ID / 4  // Every 4 nodes in same fault domain
}

func (ofs *OneFS) selectStoragePool(path string, size int64) *StoragePool {
    // Simple policy: use default pool
    // In real implementation, match path pattern or size to pool
    return ofs.storagePools["default"]
}

func (ofs *OneFS) generateShardPath(stripeNum int64, shardIdx, nodeID int) string {
    return fmt.Sprintf("/ifs/.ifsvar/stripe-%d/shard-%d", stripeNum, shardIdx)
}

func (ofs *OneFS) writeShardToNode(ctx context.Context, nodeID int,
                                  path string, data []byte) error {
    // RPC call to node to write shard
    // Implementation would use gRPC or similar
    return nil
}

func (ofs *OneFS) readShardFromNode(nodeID int, path string) ([]byte, error) {
    // RPC call to node to read shard
    return nil, nil
}

func contains(slice []int, val int) bool {
    for _, v := range slice {
        if v == val {
            return true
        }
    }
    return false
}

// DistributedLockService provides distributed locking
type DistributedLockService struct {
    locks sync.Map
}

// NewDistributedLockService creates a new distributed lock service
func NewDistributedLockService(nodes []*Node) *DistributedLockService {
    return &DistributedLockService{}
}

// TransactionLog manages transaction logging for consistency
type TransactionLog struct {
    entries []TxnEntry
    mu      sync.RWMutex
}

// TxnEntry represents a single transaction log entry
type TxnEntry struct {
    ID        uint64
    Operation string
    Timestamp time.Time
}

// NewTransactionLog creates a new transaction log
func NewTransactionLog() *TransactionLog {
    return &TransactionLog{
        entries: make([]TxnEntry, 0),
    }
}

// NodeCache provides local caching on a node
type NodeCache struct {
    cache sync.Map
}

// LRUCache implements a simple LRU cache
type LRUCache struct {
    cache sync.Map
}

// NewDistributedNamespace creates a new distributed namespace
func NewDistributedNamespace(nodes []*Node) *DistributedNamespace {
    return &DistributedNamespace{
        pathCache:  &LRUCache{},
        shardCount: len(nodes),
        nextInode:  1,
    }
}

// AllocateInode allocates a new inode number
func (ns *DistributedNamespace) AllocateInode() uint64 {
    ns.mu.Lock()
    defer ns.mu.Unlock()
    inode := ns.nextInode
    ns.nextInode++
    return inode
}

// CommitFile commits a file layout to the namespace
func (ns *DistributedNamespace) CommitFile(path string, layout *FileLayout) error {
    ns.inodeTable.Store(layout.InodeNumber, layout)
    ns.pathCache.cache.Store(path, layout.InodeNumber)
    return nil
}

// LookupFile looks up a file by path
func (ns *DistributedNamespace) LookupFile(path string) (*FileLayout, error) {
    if inode, ok := ns.pathCache.cache.Load(path); ok {
        if layout, ok := ns.inodeTable.Load(inode); ok {
            return layout.(*FileLayout), nil
        }
    }
    return nil, fmt.Errorf("file not found: %s", path)
}

// WalkFiles walks through all files in the namespace
func (ns *DistributedNamespace) WalkFiles(fn func(string, *FileLayout) error) {
    ns.pathCache.cache.Range(func(key, value interface{}) bool {
        path := key.(string)
        inode := value.(uint64)
        if layout, ok := ns.inodeTable.Load(inode); ok {
            fn(path, layout.(*FileLayout))
        }
        return true
    })
}

// decodeStripe decodes data from shards using Reed-Solomon
func (ofs *OneFS) decodeStripe(shards [][]byte, protection ProtectionLevel) ([]byte, error) {
    enc, err := reedsolomon.New(protection.DataStripes, protection.ParityStripes)
    if err != nil {
        return nil, err
    }

    // Reconstruct data if needed
    if err := enc.ReconstructData(shards); err != nil {
        return nil, err
    }

    // Concatenate data shards
    var result []byte
    for i := 0; i < protection.DataStripes; i++ {
        result = append(result, shards[i]...)
    }
    return result, nil
}

// readStripeFromNodes reads shards from nodes for a stripe
func (ofs *OneFS) readStripeFromNodes(ctx context.Context, block BlockDescriptor) ([][]byte, error) {
    shards := make([][]byte, len(block.StripeMembers))

    var wg sync.WaitGroup
    errChan := make(chan error, len(block.StripeMembers))

    for i, member := range block.StripeMembers {
        wg.Add(1)
        go func(idx int, m StripeMember) {
            defer wg.Done()

            shard, err := ofs.readShardFromNode(m.NodeID, m.LocalPath)
            if err != nil {
                errChan <- err
                return
            }
            shards[idx] = shard
        }(i, member)
    }

    wg.Wait()
    close(errChan)

    // Check for errors
    for err := range errChan {
        if err != nil {
            return nil, err
        }
    }

    return shards, nil
}

// calculateStripeChecksum calculates a checksum for a stripe
func (ofs *OneFS) calculateStripeChecksum(shards [][]byte) []byte {
    h := sha256.New()
    for _, shard := range shards {
        h.Write(shard)
    }
    return h.Sum(nil)
}

// selectReplacementNode selects a replacement node for a failed node
func (ofs *OneFS) selectReplacementNode(failedNodeID int) int {
    // Select a healthy node different from the failed one
    for _, node := range ofs.nodes {
        if node.ID != failedNodeID && node.healthy {
            return node.ID
        }
    }
    return 0
}

// ============================================================================
// VPLEX Metro Clustering Implementation
// ============================================================================

// VPLEXMetroCluster provides active-active metro clustering with synchronous replication
type VPLEXMetroCluster struct {
    // Cluster sites
    sites           map[string]*ClusterSite
    sitesMu         sync.RWMutex

    // Witness service for arbitration
    witness         *WitnessService

    // Distributed cache coherence
    cacheCoherence  *DistributedCache

    // Consistency groups
    consistencyGroups map[string]*ConsistencyGroup
    cgMu            sync.RWMutex

    // Replication engine
    replication     *MetroReplication

    // Cluster configuration
    config          *MetroClusterConfig
}

// ClusterSite represents a data center site in metro cluster
type ClusterSite struct {
    SiteID          string
    Name            string
    Location        string

    // Storage arrays at this site
    Arrays          []*StorageArray

    // Site health
    Healthy         bool
    LastHeartbeat   time.Time

    // Network connectivity
    Latency         time.Duration
    Bandwidth       int64

    // Cache at this site
    LocalCache      *SiteCache
}

// StorageArray represents a storage array at a site
type StorageArray struct {
    ArrayID         string
    Type            string  // "PowerStore", "Unity", etc.
    Capacity        int64
    Used            int64

    // Virtual volumes
    VirtualVolumes  map[string]*VirtualVolume
    vvMu            sync.RWMutex
}

// VirtualVolume represents a distributed volume across sites
type VirtualVolume struct {
    VolumeID        string
    Name            string
    Size            int64

    // Distribution across sites
    Extents         map[string]*VolumeExtent  // siteID -> extent

    // Access mode
    AccessMode      AccessMode

    // Consistency group membership
    ConsistencyGroupID string

    // Replication state
    ReplicationState ReplicationState
}

// VolumeExtent represents portion of volume at a site
type VolumeExtent struct {
    SiteID          string
    ArrayID         string
    LocalPath       string
    Size            int64

    // Mirror relationship
    MirrorSiteID    string
    InSync          bool
    LastSyncTime    time.Time
}

type AccessMode int

const (
    AccessModeReadWrite AccessMode = iota
    AccessModeReadOnly
    AccessModeActiveActive
)

type ReplicationState int

const (
    ReplicationSynced ReplicationState = iota
    ReplicationSyncing
    ReplicationDegraded
    ReplicationFailed
)

// ConsistencyGroup ensures crash-consistent replication
type ConsistencyGroup struct {
    GroupID         string
    Name            string

    // Volumes in this group
    Volumes         []string  // Volume IDs

    // Synchronization
    SyncInterval    time.Duration
    LastSync        time.Time

    // Recovery point objective
    RPO             time.Duration
}

// WitnessService provides arbitration for split-brain scenarios
type WitnessService struct {
    WitnessID       string
    Location        string

    // Site registrations
    RegisteredSites map[string]*SiteRegistration
    regMu           sync.RWMutex

    // Quorum management
    quorum          *QuorumManager
}

// SiteRegistration tracks site registration with witness
type SiteRegistration struct {
    SiteID          string
    RegisteredAt    time.Time
    LastHeartbeat   time.Time
    Healthy         bool
}

// QuorumManager handles site quorum and failover decisions
type QuorumManager struct {
    mu              sync.RWMutex
    votes           map[string]int  // siteID -> vote count
    quorumThreshold int
}

// DistributedCache provides cache coherence across sites
type DistributedCache struct {
    // Local cache entries
    entries         sync.Map  // key -> *CacheEntry

    // Coherence protocol (write-through, write-back)
    protocol        CacheProtocol

    // Invalidation tracking
    invalidations   chan *CacheInvalidation
}

type CacheProtocol int

const (
    CacheProtocolWriteThrough CacheProtocol = iota
    CacheProtocolWriteBack
)

// CacheEntry represents a cached data block
type CacheEntry struct {
    Key             string
    Data            []byte

    // Ownership and coherence
    OwnerSiteID     string
    State           CacheState
    Version         uint64

    // Timing
    CreatedAt       time.Time
    LastAccessed    time.Time
    Dirty           bool
}

type CacheState int

const (
    CacheStateInvalid CacheState = iota
    CacheStateShared
    CacheStateExclusive
    CacheStateModified
)

// CacheInvalidation represents a cache invalidation message
type CacheInvalidation struct {
    Key             string
    Version         uint64
    SourceSiteID    string
}

// SiteCache provides local caching at a site
type SiteCache struct {
    entries         sync.Map
    maxSize         int64
    currentSize     int64
    mu              sync.RWMutex
}

// MetroReplication handles synchronous replication between sites
type MetroReplication struct {
    // Replication channels
    writeLog        *ReplicationLog

    // Replication workers
    workers         []*ReplicationWorker

    // Synchronization
    syncMode        SyncMode
}

type SyncMode int

const (
    SyncModeSynchronous SyncMode = iota
    SyncModeAsynchronous
)

// ReplicationLog tracks writes for replication
type ReplicationLog struct {
    entries         []*ReplicationEntry
    mu              sync.RWMutex
    nextSequence    uint64
}

// ReplicationEntry represents a write to be replicated
type ReplicationEntry struct {
    SequenceNumber  uint64
    VolumeID        string
    Offset          int64
    Data            []byte
    Timestamp       time.Time

    // Acknowledgements from sites
    Acks            map[string]bool  // siteID -> acked
    ackMu           sync.RWMutex
}

// ReplicationWorker replicates data to remote sites
type ReplicationWorker struct {
    WorkerID        int
    SourceSite      string
    TargetSite      string

    // Work queue
    queue           chan *ReplicationEntry

    // Statistics
    BytesReplicated int64
    Latency         time.Duration
}

// MetroClusterConfig holds metro cluster configuration
type MetroClusterConfig struct {
    ClusterName     string
    MaxLatency      time.Duration  // Maximum tolerable latency between sites
    SyncTimeout     time.Duration

    // Failover settings
    AutoFailover    bool
    FailoverTimeout time.Duration

    // Witness configuration
    WitnessEnabled  bool
    WitnessURL      string
}

// NewVPLEXMetroCluster creates a new metro cluster
func NewVPLEXMetroCluster(config *MetroClusterConfig) *VPLEXMetroCluster {
    cluster := &VPLEXMetroCluster{
        sites:             make(map[string]*ClusterSite),
        consistencyGroups: make(map[string]*ConsistencyGroup),
        config:            config,
    }

    // Initialize witness service
    if config.WitnessEnabled {
        cluster.witness = &WitnessService{
            WitnessID:       "witness-1",
            Location:        config.WitnessURL,
            RegisteredSites: make(map[string]*SiteRegistration),
            quorum:          &QuorumManager{
                votes:           make(map[string]int),
                quorumThreshold: 2,
            },
        }
    }

    // Initialize distributed cache
    cluster.cacheCoherence = &DistributedCache{
        protocol:      CacheProtocolWriteThrough,
        invalidations: make(chan *CacheInvalidation, 1000),
    }

    // Initialize replication engine
    cluster.replication = &MetroReplication{
        writeLog: &ReplicationLog{
            entries: make([]*ReplicationEntry, 0),
        },
        syncMode: SyncModeSynchronous,
    }

    // Start cache coherence handler
    go cluster.handleCacheInvalidations()

    return cluster
}

// AddSite adds a new site to the metro cluster
func (vmc *VPLEXMetroCluster) AddSite(site *ClusterSite) error {
    vmc.sitesMu.Lock()
    defer vmc.sitesMu.Unlock()

    if _, exists := vmc.sites[site.SiteID]; exists {
        return fmt.Errorf("site already exists: %s", site.SiteID)
    }

    vmc.sites[site.SiteID] = site

    // Register with witness
    if vmc.witness != nil {
        vmc.witness.RegisterSite(site.SiteID)
    }

    return nil
}

// CreateVirtualVolume creates a distributed virtual volume
func (vmc *VPLEXMetroCluster) CreateVirtualVolume(name string, size int64,
                                                  primarySite, secondarySite string) (*VirtualVolume, error) {

    vv := &VirtualVolume{
        VolumeID:         generateVolumeID(),
        Name:             name,
        Size:             size,
        Extents:          make(map[string]*VolumeExtent),
        AccessMode:       AccessModeActiveActive,
        ReplicationState: ReplicationSynced,
    }

    // Create extent on primary site
    primaryExtent := &VolumeExtent{
        SiteID:       primarySite,
        LocalPath:    fmt.Sprintf("/vplex/%s/primary", vv.VolumeID),
        Size:         size,
        MirrorSiteID: secondarySite,
        InSync:       true,
        LastSyncTime: time.Now(),
    }
    vv.Extents[primarySite] = primaryExtent

    // Create extent on secondary site
    secondaryExtent := &VolumeExtent{
        SiteID:       secondarySite,
        LocalPath:    fmt.Sprintf("/vplex/%s/secondary", vv.VolumeID),
        Size:         size,
        MirrorSiteID: primarySite,
        InSync:       true,
        LastSyncTime: time.Now(),
    }
    vv.Extents[secondarySite] = secondaryExtent

    return vv, nil
}

// WriteToVolume performs synchronous write to distributed volume
func (vmc *VPLEXMetroCluster) WriteToVolume(ctx context.Context, volumeID string,
                                           offset int64, data []byte) error {

    // Create replication entry
    entry := &ReplicationEntry{
        SequenceNumber: vmc.replication.writeLog.GetNextSequence(),
        VolumeID:       volumeID,
        Offset:         offset,
        Data:           data,
        Timestamp:      time.Now(),
        Acks:           make(map[string]bool),
    }

    // Write to all sites synchronously
    vmc.sitesMu.RLock()
    sites := make([]*ClusterSite, 0, len(vmc.sites))
    for _, site := range vmc.sites {
        if site.Healthy {
            sites = append(sites, site)
        }
    }
    vmc.sitesMu.RUnlock()

    // Parallel write to all sites
    var wg sync.WaitGroup
    errChan := make(chan error, len(sites))

    for _, site := range sites {
        wg.Add(1)
        go func(s *ClusterSite) {
            defer wg.Done()

            // Write to site (simulated)
            if err := vmc.writeToSite(ctx, s.SiteID, volumeID, offset, data); err != nil {
                errChan <- err
                return
            }

            // Mark as acknowledged
            entry.ackMu.Lock()
            entry.Acks[s.SiteID] = true
            entry.ackMu.Unlock()

        }(site)
    }

    // Wait for all writes with timeout
    done := make(chan struct{})
    go func() {
        wg.Wait()
        close(done)
    }()

    select {
    case <-done:
        // All writes completed
    case <-time.After(vmc.config.SyncTimeout):
        return fmt.Errorf("synchronous write timeout")
    case err := <-errChan:
        return fmt.Errorf("write failed: %w", err)
    }

    // Invalidate cache across all sites
    vmc.cacheCoherence.invalidations <- &CacheInvalidation{
        Key:     fmt.Sprintf("%s:%d", volumeID, offset),
        Version: entry.SequenceNumber,
    }

    return nil
}

// writeToSite writes data to a specific site
func (vmc *VPLEXMetroCluster) writeToSite(ctx context.Context, siteID, volumeID string,
                                         offset int64, data []byte) error {
    // Simulate RPC to site
    // In production, this would use gRPC or similar
    return nil
}

// handleCacheInvalidations processes cache invalidation messages
func (vmc *VPLEXMetroCluster) handleCacheInvalidations() {
    for invalidation := range vmc.cacheCoherence.invalidations {
        // Invalidate cache entry across all sites
        vmc.cacheCoherence.entries.Delete(invalidation.Key)
    }
}

// PerformFailover performs site failover
func (vmc *VPLEXMetroCluster) PerformFailover(failedSite, targetSite string) error {
    vmc.sitesMu.Lock()
    defer vmc.sitesMu.Unlock()

    failed, exists := vmc.sites[failedSite]
    if !exists {
        return fmt.Errorf("site not found: %s", failedSite)
    }

    target, exists := vmc.sites[targetSite]
    if !exists {
        return fmt.Errorf("target site not found: %s", targetSite)
    }

    // Mark failed site as unhealthy
    failed.Healthy = false

    // Check quorum with witness
    if vmc.witness != nil {
        hasQuorum := vmc.witness.CheckQuorum(targetSite)
        if !hasQuorum {
            return fmt.Errorf("insufficient quorum for failover")
        }
    }

    // Promote target site volumes
    for _, array := range target.Arrays {
        array.vvMu.Lock()
        for _, vv := range array.VirtualVolumes {
            if vv.ReplicationState == ReplicationSynced {
                vv.AccessMode = AccessModeReadWrite
            }
        }
        array.vvMu.Unlock()
    }

    return nil
}

// RegisterSite registers a site with the witness
func (ws *WitnessService) RegisterSite(siteID string) {
    ws.regMu.Lock()
    defer ws.regMu.Unlock()

    ws.RegisteredSites[siteID] = &SiteRegistration{
        SiteID:       siteID,
        RegisteredAt: time.Now(),
        LastHeartbeat: time.Now(),
        Healthy:      true,
    }
}

// CheckQuorum checks if a site has quorum
func (ws *WitnessService) CheckQuorum(siteID string) bool {
    ws.regMu.RLock()
    defer ws.regMu.RUnlock()

    healthySites := 0
    for _, reg := range ws.RegisteredSites {
        if reg.Healthy {
            healthySites++
        }
    }

    return healthySites >= ws.quorum.quorumThreshold
}

// GetNextSequence returns next sequence number for replication
func (rl *ReplicationLog) GetNextSequence() uint64 {
    rl.mu.Lock()
    defer rl.mu.Unlock()

    seq := rl.nextSequence
    rl.nextSequence++
    return seq
}

func generateVolumeID() string {
    return fmt.Sprintf("vol-%d", time.Now().UnixNano())
}

// ============================================================================
// NVMe Deep Dive Implementation
// ============================================================================

// NVMeSubsystem represents an NVMe storage subsystem
type NVMeSubsystem struct {
    // Subsystem identification
    SubsystemNQN    string  // NVMe Qualified Name
    SubsystemID     string

    // Controllers
    controllers     map[int]*NVMeController
    controllersMu   sync.RWMutex

    // Namespaces
    namespaces      map[uint32]*NVMeNamespace
    namespacesMu    sync.RWMutex

    // NVMe-oF (NVMe over Fabrics)
    fabricsEnabled  bool
    transports      map[string]*NVMeFabricsTransport

    // Performance
    queues          *NVMeQueueManager

    // Configuration
    config          *NVMeConfig
}

// NVMeController represents an NVMe controller
type NVMeController struct {
    ControllerID    int
    PCIeAddress     string

    // Controller capabilities
    MaxQueueEntries uint16
    MaxNamespaces   uint32
    MaxTransferSize uint32

    // I/O Queue Pairs
    ioQueues        []*NVMeQueuePair
    adminQueue      *NVMeQueuePair

    // Controller state
    State           ControllerState
    Enabled         bool

    // Statistics
    Stats           *ControllerStats
}

type ControllerState int

const (
    ControllerStateIdle ControllerState = iota
    ControllerStateActive
    ControllerStateFailed
)

// NVMeNamespace represents an NVMe namespace (equivalent to a LUN)
type NVMeNamespace struct {
    NamespaceID     uint32
    NGUID           string  // Namespace Globally Unique Identifier
    UUID            string

    // Capacity
    Size            uint64  // in bytes
    BlockSize       uint32  // typically 4096
    NumBlocks       uint64

    // Namespace type
    Type            NamespaceType

    // ZNS (Zoned Namespace) specific
    ZNSConfig       *ZonedNamespaceConfig

    // Data protection
    PIType          uint8   // Protection Information Type
    MetadataSize    uint16

    // Access
    SharedNamespace bool
    Multipath       bool

    // Performance
    IOPSLimit       uint64
    BandwidthLimit  uint64

    // State
    State           NamespaceState
    Attached        bool
}

type NamespaceType int

const (
    NamespaceTypeBlock NamespaceType = iota
    NamespaceTypeZoned  // ZNS
    NamespaceTypeKeyValue
)

type NamespaceState int

const (
    NamespaceStateActive NamespaceState = iota
    NamespaceStateInactive
    NamespaceStateDegraded
)

// ZonedNamespaceConfig for ZNS (Zoned Namespace) support
type ZonedNamespaceConfig struct {
    ZoneSize        uint64
    MaxOpenZones    uint32
    MaxActiveZones  uint32

    // Zone types
    ZoneCapacity    uint64
    ZoneDescriptors []*ZoneDescriptor

    // ZNS operations
    mu              sync.RWMutex
}

// ZoneDescriptor describes a single zone in ZNS
type ZoneDescriptor struct {
    ZoneID          uint64
    ZoneType        ZoneType
    ZoneState       ZoneState

    // Addressing
    StartLBA        uint64
    Capacity        uint64
    WritePointer    uint64

    // Attributes
    FinishedRecommended bool
    ResetRecommended    bool
}

type ZoneType int

const (
    ZoneTypeSequentialWriteRequired ZoneType = iota
    ZoneTypeSequentialWritePreferred
    ZoneTypeRandom
)

type ZoneState int

const (
    ZoneStateEmpty ZoneState = iota
    ZoneStateImplicitlyOpen
    ZoneStateExplicitlyOpen
    ZoneStateClosed
    ZoneStateFull
    ZoneStateReadOnly
    ZoneStateOffline
)

// NVMeQueuePair represents submission and completion queues
type NVMeQueuePair struct {
    QueueID         uint16

    // Submission Queue
    SubmissionQueue *SubmissionQueue

    // Completion Queue
    CompletionQueue *CompletionQueue

    // Queue depth
    QueueDepth      uint16

    // Performance
    Stats           *QueueStats
}

// SubmissionQueue holds NVMe commands to be executed
type SubmissionQueue struct {
    QueueID         uint16
    Entries         []*NVMeCommand
    Head            uint16
    Tail            uint16
    Size            uint16

    // Doorbell
    DoorbellAddr    uint64

    mu              sync.Mutex
}

// CompletionQueue holds command completion status
type CompletionQueue struct {
    QueueID         uint16
    Entries         []*NVMeCompletion
    Head            uint16
    Tail            uint16
    Size            uint16

    // Doorbell
    DoorbellAddr    uint64

    // Interrupt
    InterruptVector uint16

    mu              sync.Mutex
}

// NVMeCommand represents an NVMe command
type NVMeCommand struct {
    Opcode          uint8
    CommandID       uint16
    NamespaceID     uint32

    // Data pointer (PRP or SGL)
    DataPtr         uint64
    DataLength      uint32

    // Command specific
    CDW10           uint32
    CDW11           uint32
    CDW12           uint32
    CDW13           uint32
    CDW14           uint32
    CDW15           uint32

    // Timing
    SubmittedAt     time.Time
}

// NVMeCompletion represents a command completion entry
type NVMeCompletion struct {
    CommandID       uint16
    Status          uint16
    SquareHead      uint16

    // Result
    DW0             uint32

    // Phase tag
    PhaseTag        bool

    // Timing
    CompletedAt     time.Time
}

// NVMe Command Opcodes
const (
    // Admin commands
    NVMeAdminCreateIOCQ     = 0x05
    NVMeAdminCreateIOSQ     = 0x01
    NVMeAdminDeleteIOCQ     = 0x04
    NVMeAdminDeleteIOSQ     = 0x00
    NVMeAdminIdentify       = 0x06
    NVMeAdminGetLogPage     = 0x02
    NVMeAdminNamespaceAttach = 0x15
    NVMeAdminNamespaceManage = 0x0D

    // I/O commands
    NVMeIORead              = 0x02
    NVMeIOWrite             = 0x01
    NVMeIOFlush             = 0x00
    NVMeIOCompare           = 0x05
    NVMeIOWriteZeroes       = 0x08

    // ZNS commands
    NVMeZNSZoneAppend       = 0x7D
    NVMeZNSZoneManagementSend = 0x79
    NVMeZNSZoneManagementRecv = 0x7A
)

// NVMeFabricsTransport handles NVMe-oF
type NVMeFabricsTransport struct {
    TransportType   TransportType
    Address         string
    Port            int

    // Connection management
    connections     map[string]*NVMeFabricsConnection
    connMu          sync.RWMutex

    // RDMA specific
    rdmaConfig      *RDMAConfig

    // TCP specific
    tcpConfig       *TCPConfig
}

type TransportType int

const (
    TransportRDMA TransportType = iota
    TransportTCP
    TransportFC  // Fibre Channel
)

// NVMeFabricsConnection represents a fabric connection
type NVMeFabricsConnection struct {
    ConnectionID    string
    HostNQN         string
    SubsystemNQN    string

    // Transport
    Transport       TransportType
    RemoteAddr      string

    // Queue pairs over fabric
    QueuePairs      []*NVMeQueuePair

    // State
    Connected       bool
    LastHeartbeat   time.Time

    // Performance
    Latency         time.Duration
    Bandwidth       uint64
}

// RDMAConfig for RDMA transport
type RDMAConfig struct {
    MaxInlineData   uint32
    MaxSGE          uint32
    CompletionQueueDepth uint32
}

// TCPConfig for TCP transport
type TCPConfig struct {
    TLS             bool
    InOrderDelivery bool
    HeaderDigest    bool
    DataDigest      bool
}

// NVMeQueueManager manages queue allocation
type NVMeQueueManager struct {
    queues          sync.Map  // queueID -> *NVMeQueuePair
    nextQueueID     uint16
    mu              sync.Mutex

    // Multi-queue
    numQueues       int
    queuesPerCore   int
}

// NVMeConfig holds NVMe subsystem configuration
type NVMeConfig struct {
    // Queue configuration
    NumIOQueues     int
    QueueDepth      uint16

    // Namespace defaults
    DefaultBlockSize uint32

    // Performance
    EnableMultiQueue bool
    QueuesPerCPU     int

    // Features
    EnableZNS        bool
    EnableMultipath  bool
    EnableFabrics    bool

    // Timeouts
    IOTimeout        time.Duration
}

// ControllerStats tracks controller performance
type ControllerStats struct {
    TotalCommands   uint64
    CompletedCommands uint64
    FailedCommands  uint64

    TotalBytesRead  uint64
    TotalBytesWritten uint64

    AverageLatency  time.Duration
    mu              sync.RWMutex
}

// QueueStats tracks queue performance
type QueueStats struct {
    CommandsSubmitted uint64
    CommandsCompleted uint64
    QueueDepth        uint64

    mu              sync.RWMutex
}

// NewNVMeSubsystem creates a new NVMe subsystem
func NewNVMeSubsystem(nqn string, config *NVMeConfig) *NVMeSubsystem {
    subsystem := &NVMeSubsystem{
        SubsystemNQN:   nqn,
        SubsystemID:    generateSubsystemID(),
        controllers:    make(map[int]*NVMeController),
        namespaces:     make(map[uint32]*NVMeNamespace),
        transports:     make(map[string]*NVMeFabricsTransport),
        fabricsEnabled: config.EnableFabrics,
        config:         config,
        queues:         &NVMeQueueManager{
            numQueues:     config.NumIOQueues,
            queuesPerCore: config.QueuesPerCPU,
        },
    }

    return subsystem
}

// CreateController creates a new NVMe controller
func (ns *NVMeSubsystem) CreateController(controllerID int) (*NVMeController, error) {
    ns.controllersMu.Lock()
    defer ns.controllersMu.Unlock()

    if _, exists := ns.controllers[controllerID]; exists {
        return nil, fmt.Errorf("controller %d already exists", controllerID)
    }

    controller := &NVMeController{
        ControllerID:    controllerID,
        MaxQueueEntries: ns.config.QueueDepth,
        MaxNamespaces:   256,
        MaxTransferSize: 128 * 1024, // 128KB
        ioQueues:        make([]*NVMeQueuePair, 0),
        State:           ControllerStateActive,
        Enabled:         true,
        Stats:           &ControllerStats{},
    }

    // Create admin queue (QID 0)
    controller.adminQueue = &NVMeQueuePair{
        QueueID:    0,
        QueueDepth: 64,
        SubmissionQueue: &SubmissionQueue{
            QueueID: 0,
            Size:    64,
            Entries: make([]*NVMeCommand, 64),
        },
        CompletionQueue: &CompletionQueue{
            QueueID: 0,
            Size:    64,
            Entries: make([]*NVMeCompletion, 64),
        },
        Stats: &QueueStats{},
    }

    // Create I/O queues
    for i := 1; i <= ns.config.NumIOQueues; i++ {
        qp := ns.createQueuePair(uint16(i), ns.config.QueueDepth)
        controller.ioQueues = append(controller.ioQueues, qp)
    }

    ns.controllers[controllerID] = controller
    return controller, nil
}

// createQueuePair creates a submission/completion queue pair
func (ns *NVMeSubsystem) createQueuePair(queueID uint16, depth uint16) *NVMeQueuePair {
    return &NVMeQueuePair{
        QueueID:    queueID,
        QueueDepth: depth,
        SubmissionQueue: &SubmissionQueue{
            QueueID: queueID,
            Size:    depth,
            Entries: make([]*NVMeCommand, depth),
        },
        CompletionQueue: &CompletionQueue{
            QueueID:         queueID,
            Size:            depth,
            Entries:         make([]*NVMeCompletion, depth),
            InterruptVector: queueID,
        },
        Stats: &QueueStats{},
    }
}

// CreateNamespace creates a new NVMe namespace
func (ns *NVMeSubsystem) CreateNamespace(size uint64, nsType NamespaceType) (*NVMeNamespace, error) {
    ns.namespacesMu.Lock()
    defer ns.namespacesMu.Unlock()

    nsID := uint32(len(ns.namespaces) + 1)
    blockSize := ns.config.DefaultBlockSize
    if blockSize == 0 {
        blockSize = 4096
    }

    namespace := &NVMeNamespace{
        NamespaceID: nsID,
        NGUID:       generateNGUID(),
        UUID:        generateNamespaceUUID(),
        Size:        size,
        BlockSize:   blockSize,
        NumBlocks:   size / uint64(blockSize),
        Type:        nsType,
        State:       NamespaceStateActive,
        Attached:    true,
    }

    // Configure ZNS if requested
    if nsType == NamespaceTypeZoned && ns.config.EnableZNS {
        namespace.ZNSConfig = ns.createZNSConfig(size)
    }

    ns.namespaces[nsID] = namespace
    return namespace, nil
}

// createZNSConfig creates a Zoned Namespace configuration
func (ns *NVMeSubsystem) createZNSConfig(totalSize uint64) *ZonedNamespaceConfig {
    zoneSize := uint64(256 * 1024 * 1024) // 256MB zones
    numZones := totalSize / zoneSize

    znsConfig := &ZonedNamespaceConfig{
        ZoneSize:       zoneSize,
        MaxOpenZones:   14,
        MaxActiveZones: 32,
        ZoneCapacity:   zoneSize,
        ZoneDescriptors: make([]*ZoneDescriptor, numZones),
    }

    // Initialize zones
    for i := uint64(0); i < numZones; i++ {
        znsConfig.ZoneDescriptors[i] = &ZoneDescriptor{
            ZoneID:       i,
            ZoneType:     ZoneTypeSequentialWriteRequired,
            ZoneState:    ZoneStateEmpty,
            StartLBA:     i * (zoneSize / 4096), // Assuming 4K blocks
            Capacity:     zoneSize,
            WritePointer: i * (zoneSize / 4096),
        }
    }

    return znsConfig
}

// SubmitCommand submits an NVMe command
func (nc *NVMeController) SubmitCommand(cmd *NVMeCommand) error {
    // Select queue (round-robin across I/O queues)
    queueIdx := int(cmd.CommandID) % len(nc.ioQueues)
    queue := nc.ioQueues[queueIdx]

    queue.SubmissionQueue.mu.Lock()
    defer queue.SubmissionQueue.mu.Unlock()

    // Add command to submission queue
    cmd.SubmittedAt = time.Now()
    queue.SubmissionQueue.Entries[queue.SubmissionQueue.Tail] = cmd

    // Update tail
    queue.SubmissionQueue.Tail = (queue.SubmissionQueue.Tail + 1) % queue.SubmissionQueue.Size

    // Update stats
    queue.Stats.mu.Lock()
    queue.Stats.CommandsSubmitted++
    queue.Stats.QueueDepth++
    queue.Stats.mu.Unlock()

    // Ring doorbell (simulated)
    go nc.processCommand(queue, cmd)

    return nil
}

// processCommand processes an NVMe command
func (nc *NVMeController) processCommand(qp *NVMeQueuePair, cmd *NVMeCommand) {
    // Simulate command processing
    time.Sleep(100 * time.Microsecond) // Simulate fast NVMe latency

    // Create completion entry
    completion := &NVMeCompletion{
        CommandID:   cmd.CommandID,
        Status:      0, // Success
        CompletedAt: time.Now(),
    }

    // Add to completion queue
    qp.CompletionQueue.mu.Lock()
    qp.CompletionQueue.Entries[qp.CompletionQueue.Tail] = completion
    qp.CompletionQueue.Tail = (qp.CompletionQueue.Tail + 1) % qp.CompletionQueue.Size
    qp.CompletionQueue.mu.Unlock()

    // Update stats
    qp.Stats.mu.Lock()
    qp.Stats.CommandsCompleted++
    qp.Stats.QueueDepth--
    qp.Stats.mu.Unlock()

    nc.Stats.mu.Lock()
    nc.Stats.CompletedCommands++
    latency := time.Since(cmd.SubmittedAt)
    nc.Stats.AverageLatency = (nc.Stats.AverageLatency + latency) / 2
    nc.Stats.mu.Unlock()
}

// WriteZone writes to a ZNS zone (zone append)
func (ns *NVMeNamespace) WriteZone(zoneID uint64, data []byte) error {
    if ns.Type != NamespaceTypeZoned || ns.ZNSConfig == nil {
        return fmt.Errorf("namespace is not ZNS-enabled")
    }

    ns.ZNSConfig.mu.Lock()
    defer ns.ZNSConfig.mu.Unlock()

    if zoneID >= uint64(len(ns.ZNSConfig.ZoneDescriptors)) {
        return fmt.Errorf("invalid zone ID: %d", zoneID)
    }

    zone := ns.ZNSConfig.ZoneDescriptors[zoneID]

    // Check zone state
    if zone.ZoneState != ZoneStateEmpty && zone.ZoneState != ZoneStateImplicitlyOpen {
        return fmt.Errorf("zone %d not in writable state: %d", zoneID, zone.ZoneState)
    }

    // Zone append - write at write pointer
    dataBlocks := uint64(len(data)) / uint64(ns.BlockSize)
    zone.WritePointer += dataBlocks

    // Update zone state
    if zone.WritePointer >= zone.StartLBA+(zone.Capacity/uint64(ns.BlockSize)) {
        zone.ZoneState = ZoneStateFull
    } else {
        zone.ZoneState = ZoneStateImplicitlyOpen
    }

    return nil
}

// EnableNVMeOverFabrics enables NVMe-oF
func (ns *NVMeSubsystem) EnableNVMeOverFabrics(transport TransportType, address string, port int) error {
    fabricsTransport := &NVMeFabricsTransport{
        TransportType: transport,
        Address:       address,
        Port:          port,
        connections:   make(map[string]*NVMeFabricsConnection),
    }

    // Configure transport-specific settings
    switch transport {
    case TransportRDMA:
        fabricsTransport.rdmaConfig = &RDMAConfig{
            MaxInlineData:        4096,
            MaxSGE:               16,
            CompletionQueueDepth: 1024,
        }
    case TransportTCP:
        fabricsTransport.tcpConfig = &TCPConfig{
            TLS:             true,
            InOrderDelivery: true,
            HeaderDigest:    true,
            DataDigest:      true,
        }
    }

    key := fmt.Sprintf("%s:%d", address, port)
    ns.transports[key] = fabricsTransport

    return nil
}

// ConnectFabrics establishes an NVMe-oF connection
func (nft *NVMeFabricsTransport) ConnectFabrics(hostNQN, subsystemNQN string) (*NVMeFabricsConnection, error) {
    connID := generateConnectionID()

    conn := &NVMeFabricsConnection{
        ConnectionID:  connID,
        HostNQN:       hostNQN,
        SubsystemNQN:  subsystemNQN,
        Transport:     nft.TransportType,
        RemoteAddr:    fmt.Sprintf("%s:%d", nft.Address, nft.Port),
        Connected:     true,
        LastHeartbeat: time.Now(),
        QueuePairs:    make([]*NVMeQueuePair, 0),
    }

    nft.connMu.Lock()
    nft.connections[connID] = conn
    nft.connMu.Unlock()

    return conn, nil
}

// Helper functions
func generateSubsystemID() string {
    return fmt.Sprintf("subsys-%d", time.Now().UnixNano())
}

func generateNGUID() string {
    return fmt.Sprintf("%032x", time.Now().UnixNano())
}

func generateNamespaceUUID() string {
    return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
        time.Now().Unix(),
        uint16(time.Now().Nanosecond()),
        uint16(0x4000|time.Now().Nanosecond()>>16),
        uint16(0x8000|(time.Now().Nanosecond()>>8)&0x3FFF),
        time.Now().UnixNano()&0xFFFFFFFFFFFF)
}

func generateConnectionID() string {
    return fmt.Sprintf("conn-%d", time.Now().UnixNano())
}