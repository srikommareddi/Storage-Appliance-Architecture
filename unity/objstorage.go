package unity

import (
    "bytes"
    "context"
    "crypto/md5"
    "crypto/rand"
    "encoding/hex"
    "fmt"
    "io"
    "net/http"
    "sort"
    "strings"
    "sync"
    "time"

    "github.com/gorilla/mux"
)

// S3-compatible object storage engine
type ObjectProtocolEngine struct {
    pool      *UnifiedStoragePool
    
    // Buckets
    buckets   map[string]*Bucket
    bucketsMu sync.RWMutex
    
    // Object metadata
    objects   map[string]*ObjectMetadata
    objectsMu sync.RWMutex
    
    // Multipart uploads
    uploads   map[string]*MultipartUpload
    uploadsMu sync.RWMutex
    
    // Access control
    acl       *AccessControlList
    
    // Versioning support
    versions  map[string][]*ObjectVersion
    versionsMu sync.RWMutex
}

type Bucket struct {
    Name           string
    CreationDate   time.Time
    Owner          string
    
    // Versioning
    VersioningEnabled bool
    
    // Lifecycle rules
    LifecycleRules []LifecycleRule
    
    // Replication
    ReplicationConfig *ReplicationConfig
    
    // Storage tier
    DefaultTier    StorageTier
    
    // Encryption
    EncryptionEnabled bool
    EncryptionType    string
}

type ObjectMetadata struct {
    Key            string
    Bucket         string
    VersionID      string
    Size           int64
    ETag           string
    ContentType    string
    LastModified   time.Time
    StorageClass   string
    
    // Storage location in pool
    StorageID      string
    StorageOffset  uint64
    
    // User metadata
    Metadata       map[string]string
    
    // Tags
    Tags           map[string]string
    
    // ACL
    ACL            *ObjectACL
}

type ObjectVersion struct {
    VersionID      string
    Metadata       *ObjectMetadata
    IsLatest       bool
    IsDeleteMarker bool
}

type LifecycleRule struct {
    ID             string
    Status         string  // Enabled/Disabled
    Prefix         string
    
    // Transition rules
    Transitions    []Transition
    
    // Expiration
    ExpirationDays int
    
    // Abort incomplete multipart uploads
    AbortMultipartDays int
}

type Transition struct {
    Days         int
    StorageClass string
}

func NewObjectProtocolEngine(pool *UnifiedStoragePool) *ObjectProtocolEngine {
    return &ObjectProtocolEngine{
        pool:     pool,
        buckets:  make(map[string]*Bucket),
        objects:  make(map[string]*ObjectMetadata),
        uploads:  make(map[string]*MultipartUpload),
        versions: make(map[string][]*ObjectVersion),
        acl:      NewAccessControlList(),
    }
}

// S3 REST API handlers
func (ope *ObjectProtocolEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    router := mux.NewRouter()

    // Bucket operations
    router.HandleFunc("/", ope.listBucketsHandler).Methods("GET")
    router.HandleFunc("/{bucket}", ope.createBucketHandler).Methods("PUT")
    router.HandleFunc("/{bucket}", ope.deleteBucketHandler).Methods("DELETE")
    router.HandleFunc("/{bucket}", ope.listObjectsHandler).Methods("GET")

    // Object operations
    router.HandleFunc("/{bucket}/{key:.*}", ope.putObjectHandler).Methods("PUT")
    router.HandleFunc("/{bucket}/{key:.*}", ope.getObjectHandler).Methods("GET")
    router.HandleFunc("/{bucket}/{key:.*}", ope.deleteObjectHandler).Methods("DELETE")
    router.HandleFunc("/{bucket}/{key:.*}", ope.headObjectHandler).Methods("HEAD")

    // Multipart upload
    router.HandleFunc("/{bucket}/{key:.*}", ope.initiateMultipartHandler).
        Methods("POST").Queries("uploads", "")
    router.HandleFunc("/{bucket}/{key:.*}", ope.uploadPartHandler).
        Methods("PUT").Queries("uploadId", "{uploadId}", "partNumber", "{partNumber}")
    router.HandleFunc("/{bucket}/{key:.*}", ope.completeMultipartHandler).
        Methods("POST").Queries("uploadId", "{uploadId}")

    router.ServeHTTP(w, r)
}

// HTTP handler methods
func (ope *ObjectProtocolEngine) listBucketsHandler(w http.ResponseWriter, r *http.Request) {
    ope.bucketsMu.RLock()
    defer ope.bucketsMu.RUnlock()

    w.Header().Set("Content-Type", "application/xml")
    w.WriteHeader(http.StatusOK)
    // Implement bucket list XML response
}

func (ope *ObjectProtocolEngine) createBucketHandler(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    bucket := vars["bucket"]

    config := BucketConfig{
        Owner:             "default",
        VersioningEnabled: false,
        DefaultTier:       StorageTierStandard,
    }

    if err := ope.CreateBucket(bucket, config); err != nil {
        http.Error(w, err.Error(), http.StatusConflict)
        return
    }

    w.WriteHeader(http.StatusOK)
}

func (ope *ObjectProtocolEngine) deleteBucketHandler(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    bucket := vars["bucket"]

    ope.bucketsMu.Lock()
    defer ope.bucketsMu.Unlock()

    if _, exists := ope.buckets[bucket]; !exists {
        http.Error(w, "bucket not found", http.StatusNotFound)
        return
    }

    delete(ope.buckets, bucket)
    w.WriteHeader(http.StatusNoContent)
}

func (ope *ObjectProtocolEngine) listObjectsHandler(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    bucket := vars["bucket"]

    ope.bucketsMu.RLock()
    _, exists := ope.buckets[bucket]
    ope.bucketsMu.RUnlock()

    if !exists {
        http.Error(w, "bucket not found", http.StatusNotFound)
        return
    }

    w.Header().Set("Content-Type", "application/xml")
    w.WriteHeader(http.StatusOK)
    // Implement object list XML response
}

func (ope *ObjectProtocolEngine) putObjectHandler(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    bucket := vars["bucket"]
    key := vars["key"]

    metadata := make(map[string]string)
    metadata["Content-Type"] = r.Header.Get("Content-Type")

    objMeta, err := ope.PutObject(r.Context(), bucket, key, r.Body, r.ContentLength, metadata)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    w.Header().Set("ETag", objMeta.ETag)
    w.WriteHeader(http.StatusOK)
}

func (ope *ObjectProtocolEngine) getObjectHandler(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    bucket := vars["bucket"]
    key := vars["key"]
    versionID := r.URL.Query().Get("versionId")

    reader, objMeta, err := ope.GetObject(r.Context(), bucket, key, versionID)
    if err != nil {
        http.Error(w, err.Error(), http.StatusNotFound)
        return
    }
    defer reader.Close()

    w.Header().Set("Content-Type", objMeta.ContentType)
    w.Header().Set("ETag", objMeta.ETag)
    w.Header().Set("Content-Length", fmt.Sprintf("%d", objMeta.Size))
    w.WriteHeader(http.StatusOK)

    io.Copy(w, reader)
}

func (ope *ObjectProtocolEngine) deleteObjectHandler(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    bucket := vars["bucket"]
    key := vars["key"]
    versionID := r.URL.Query().Get("versionId")

    if err := ope.DeleteObject(r.Context(), bucket, key, versionID); err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    w.WriteHeader(http.StatusNoContent)
}

func (ope *ObjectProtocolEngine) headObjectHandler(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    bucket := vars["bucket"]
    key := vars["key"]

    ope.objectsMu.RLock()
    objMeta, exists := ope.objects[bucket+"/"+key]
    ope.objectsMu.RUnlock()

    if !exists {
        http.Error(w, "object not found", http.StatusNotFound)
        return
    }

    w.Header().Set("Content-Type", objMeta.ContentType)
    w.Header().Set("ETag", objMeta.ETag)
    w.Header().Set("Content-Length", fmt.Sprintf("%d", objMeta.Size))
    w.WriteHeader(http.StatusOK)
}

func (ope *ObjectProtocolEngine) initiateMultipartHandler(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    bucket := vars["bucket"]
    key := vars["key"]

    uploadID, err := ope.InitiateMultipartUpload(r.Context(), bucket, key)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/xml")
    w.WriteHeader(http.StatusOK)
    fmt.Fprintf(w, "<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>", uploadID)
}

func (ope *ObjectProtocolEngine) uploadPartHandler(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    bucket := vars["bucket"]
    key := vars["key"]
    uploadID := r.URL.Query().Get("uploadId")
    partNumber := 0
    fmt.Sscanf(r.URL.Query().Get("partNumber"), "%d", &partNumber)

    partInfo, err := ope.UploadPart(r.Context(), bucket, key, uploadID, partNumber, r.Body, r.ContentLength)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    w.Header().Set("ETag", partInfo.ETag)
    w.WriteHeader(http.StatusOK)
}

func (ope *ObjectProtocolEngine) completeMultipartHandler(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    bucket := vars["bucket"]
    key := vars["key"]
    uploadID := r.URL.Query().Get("uploadId")

    // Parse parts from request body (simplified)
    parts := []PartInfo{}

    objMeta, err := ope.CompleteMultipartUpload(r.Context(), bucket, key, uploadID, parts)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    w.Header().Set("ETag", objMeta.ETag)
    w.WriteHeader(http.StatusOK)
}

func (ope *ObjectProtocolEngine) CreateBucket(name string, config BucketConfig) error {
    ope.bucketsMu.Lock()
    defer ope.bucketsMu.Unlock()
    
    if _, exists := ope.buckets[name]; exists {
        return fmt.Errorf("bucket already exists: %s", name)
    }
    
    bucket := &Bucket{
        Name:           name,
        CreationDate:   time.Now(),
        Owner:          config.Owner,
        VersioningEnabled: config.VersioningEnabled,
        DefaultTier:    config.DefaultTier,
        EncryptionEnabled: config.EncryptionEnabled,
        EncryptionType: config.EncryptionType,
    }
    
    ope.buckets[name] = bucket
    
    return nil
}

func (ope *ObjectProtocolEngine) PutObject(ctx context.Context, 
                                          bucket, key string,
                                          reader io.Reader, 
                                          size int64,
                                          metadata map[string]string) (*ObjectMetadata, error) {
    
    // Verify bucket exists
    ope.bucketsMu.RLock()
    b, exists := ope.buckets[bucket]
    ope.bucketsMu.RUnlock()
    
    if !exists {
        return nil, fmt.Errorf("bucket not found: %s", bucket)
    }
    
    // Generate storage ID
    storageID := fmt.Sprintf("%s/%s", bucket, key)
    
    // Read object data
    data := make([]byte, size)
    if _, err := io.ReadFull(reader, data); err != nil {
        return nil, err
    }
    
    // Calculate ETag (MD5)
    hash := md5.Sum(data)
    etag := hex.EncodeToString(hash[:])
    
    // Write to unified storage pool (goes through compression/dedup/encryption)
    storageOffset, err := ope.pool.AllocateSpace(storageID, uint64(size), true)
    if err != nil {
        return nil, err
    }
    
    if err := ope.pool.WriteWithServices(storageID, 0, data, uint64(size)); err != nil {
        return nil, err
    }
    
    // Create object metadata
    objMeta := &ObjectMetadata{
        Key:           key,
        Bucket:        bucket,
        Size:          size,
        ETag:          etag,
        ContentType:   getContentType(metadata),
        LastModified:  time.Now(),
        StorageClass:  "STANDARD",
        StorageID:     storageID,
        StorageOffset: storageOffset,
        Metadata:      metadata,
        Tags:          make(map[string]string),
    }
    
    // Handle versioning
    if b.VersioningEnabled {
        versionID := generateVersionID()
        objMeta.VersionID = versionID
        
        version := &ObjectVersion{
            VersionID:      versionID,
            Metadata:       objMeta,
            IsLatest:       true,
            IsDeleteMarker: false,
        }
        
        ope.versionsMu.Lock()
        versions := ope.versions[bucket+"/"+key]
        
        // Mark previous versions as not latest
        for _, v := range versions {
            v.IsLatest = false
        }
        
        versions = append(versions, version)
        ope.versions[bucket+"/"+key] = versions
        ope.versionsMu.Unlock()
    }
    
    // Store metadata
    ope.objectsMu.Lock()
    ope.objects[bucket+"/"+key] = objMeta
    ope.objectsMu.Unlock()
    
    // Apply lifecycle rules asynchronously
    go ope.applyLifecycleRules(bucket, key)
    
    return objMeta, nil
}

func (ope *ObjectProtocolEngine) GetObject(ctx context.Context,
                                          bucket, key string,
                                          versionID string) (io.ReadCloser, *ObjectMetadata, error) {
    
    // Lookup object metadata
    ope.objectsMu.RLock()
    objMeta, exists := ope.objects[bucket+"/"+key]
    ope.objectsMu.RUnlock()
    
    if !exists {
        return nil, nil, fmt.Errorf("object not found")
    }
    
    // Handle versioning
    if versionID != "" && objMeta.VersionID != versionID {
        ope.versionsMu.RLock()
        versions := ope.versions[bucket+"/"+key]
        ope.versionsMu.RUnlock()
        
        for _, v := range versions {
            if v.VersionID == versionID {
                objMeta = v.Metadata
                break
            }
        }
    }
    
    // Read from storage pool
    data := make([]byte, objMeta.Size)
    if err := ope.pool.ReadWithServices(objMeta.StorageID, 0, 
                                       data, uint64(objMeta.Size)); err != nil {
        return nil, nil, err
    }
    
    // Verify ETag
    hash := md5.Sum(data)
    etag := hex.EncodeToString(hash[:])
    if etag != objMeta.ETag {
        return nil, nil, fmt.Errorf("checksum mismatch")
    }
    
    return io.NopCloser(bytes.NewReader(data)), objMeta, nil
}

func (ope *ObjectProtocolEngine) DeleteObject(ctx context.Context,
                                             bucket, key string,
                                             versionID string) error {
    
    ope.bucketsMu.RLock()
    b, exists := ope.buckets[bucket]
    ope.bucketsMu.RUnlock()
    
    if !exists {
        return fmt.Errorf("bucket not found")
    }
    
    if b.VersioningEnabled {
        // Create delete marker
        deleteMarker := &ObjectVersion{
            VersionID:      generateVersionID(),
            IsLatest:       true,
            IsDeleteMarker: true,
        }
        
        ope.versionsMu.Lock()
        versions := ope.versions[bucket+"/"+key]
        
        for _, v := range versions {
            v.IsLatest = false
        }
        
        versions = append(versions, deleteMarker)
        ope.versions[bucket+"/"+key] = versions
        ope.versionsMu.Unlock()
    } else {
        // Permanent delete
        ope.objectsMu.Lock()
        objMeta := ope.objects[bucket+"/"+key]
        delete(ope.objects, bucket+"/"+key)
        ope.objectsMu.Unlock()
        
        // Reclaim space from pool
        if objMeta != nil {
            ope.pool.ReclaimSpace(objMeta.StorageID)
        }
    }
    
    return nil
}

// Lifecycle management
func (ope *ObjectProtocolEngine) applyLifecycleRules(bucket, key string) {
    ope.bucketsMu.RLock()
    b, exists := ope.buckets[bucket]
    ope.bucketsMu.RUnlock()
    
    if !exists || len(b.LifecycleRules) == 0 {
        return
    }
    
    ope.objectsMu.RLock()
    objMeta := ope.objects[bucket+"/"+key]
    ope.objectsMu.RUnlock()
    
    if objMeta == nil {
        return
    }
    
    // Check each lifecycle rule
    for _, rule := range b.LifecycleRules {
        if rule.Status != "Enabled" {
            continue
        }
        
        // Check if key matches prefix
        if !strings.HasPrefix(key, rule.Prefix) {
            continue
        }
        
        age := time.Since(objMeta.LastModified)
        
        // Check transitions
        for _, transition := range rule.Transitions {
            if age >= time.Duration(transition.Days)*24*time.Hour {
                // Transition to different storage tier
                ope.transitionObject(objMeta, transition.StorageClass)
            }
        }
        
        // Check expiration
        if rule.ExpirationDays > 0 &&
           age >= time.Duration(rule.ExpirationDays)*24*time.Hour {
            ope.DeleteObject(context.Background(), bucket, key, "")
        }
    }
}

func (ope *ObjectProtocolEngine) transitionObject(objMeta *ObjectMetadata,
                                                 storageClass string) error {
    // Move object to different tier in storage pool
    // This would involve:
    // 1. Reading object data
    // 2. Writing to new tier
    // 3. Updating metadata
    // 4. Reclaiming old space
    
    objMeta.StorageClass = storageClass
    return nil
}

// Multipart upload support
func (ope *ObjectProtocolEngine) InitiateMultipartUpload(ctx context.Context,
                                                        bucket, key string) (string, error) {
    uploadID := generateUploadID()
    
    upload := &MultipartUpload{
        UploadID:  uploadID,
        Bucket:    bucket,
        Key:       key,
        Parts:     make(map[int]*PartInfo),
        Initiated: time.Now(),
    }
    
    ope.uploadsMu.Lock()
    ope.uploads[uploadID] = upload
    ope.uploadsMu.Unlock()
    
    return uploadID, nil
}

func (ope *ObjectProtocolEngine) UploadPart(ctx context.Context,
                                           bucket, key, uploadID string,
                                           partNumber int,
                                           reader io.Reader,
                                           size int64) (*PartInfo, error) {
    
    ope.uploadsMu.RLock()
    upload, exists := ope.uploads[uploadID]
    ope.uploadsMu.RUnlock()
    
    if !exists {
        return nil, fmt.Errorf("upload not found")
    }
    
    // Read part data
    data := make([]byte, size)
    if _, err := io.ReadFull(reader, data); err != nil {
        return nil, err
    }
    
    // Calculate ETag
    hash := md5.Sum(data)
    etag := hex.EncodeToString(hash[:])
    
    // Store part
    partID := fmt.Sprintf("%s/part-%d", uploadID, partNumber)
    if _, err := ope.pool.AllocateSpace(partID, uint64(size), true); err != nil {
        return nil, err
    }
    
    if err := ope.pool.WriteWithServices(partID, 0, data, uint64(size)); err != nil {
        return nil, err
    }
    
    partInfo := &PartInfo{
        PartNumber: partNumber,
        ETag:       etag,
        Size:       size,
    }
    
    upload.mu.Lock()
    upload.Parts[partNumber] = partInfo
    upload.mu.Unlock()
    
    return partInfo, nil
}

func (ope *ObjectProtocolEngine) CompleteMultipartUpload(ctx context.Context,
                                                        bucket, key, uploadID string,
                                                        parts []PartInfo) (*ObjectMetadata, error) {

    ope.uploadsMu.RLock()
    _, exists := ope.uploads[uploadID]
    ope.uploadsMu.RUnlock()

    if !exists {
        return nil, fmt.Errorf("upload not found")
    }
    
    // Sort parts
    sort.Slice(parts, func(i, j int) bool {
        return parts[i].PartNumber < parts[j].PartNumber
    })
    
    // Concatenate parts
    var totalSize int64
    var buffer bytes.Buffer
    
    for _, part := range parts {
        partID := fmt.Sprintf("%s/part-%d", uploadID, part.PartNumber)
        
        data := make([]byte, part.Size)
        if err := ope.pool.ReadWithServices(partID, 0, data, uint64(part.Size)); err != nil {
            return nil, err
        }
        
        buffer.Write(data)
        totalSize += part.Size
    }
    
    // Put complete object
    objMeta, err := ope.PutObject(ctx, bucket, key, &buffer, totalSize, nil)
    if err != nil {
        return nil, err
    }
    
    // Cleanup
    ope.uploadsMu.Lock()
    delete(ope.uploads, uploadID)
    ope.uploadsMu.Unlock()
    
    // Delete part files
    for _, part := range parts {
        partID := fmt.Sprintf("%s/part-%d", uploadID, part.PartNumber)
        ope.pool.ReclaimSpace(partID)
    }
    
    return objMeta, nil
}

func getContentType(metadata map[string]string) string {
    if ct, ok := metadata["Content-Type"]; ok {
        return ct
    }
    return "application/octet-stream"
}

func generateVersionID() string {
    return fmt.Sprintf("%d", time.Now().UnixNano())
}

func generateUploadID() string {
    return hex.EncodeToString(randomBytes(16))
}

func randomBytes(n int) []byte {
    b := make([]byte, n)
    rand.Read(b)
    return b
}

// BucketConfig holds the configuration for creating a bucket
type BucketConfig struct {
    Owner             string
    VersioningEnabled bool
    DefaultTier       StorageTier
    EncryptionEnabled bool
    EncryptionType    string
}

// ReplicationConfig holds bucket replication settings
type ReplicationConfig struct {
    DestinationBucket string
    Role              string
    Rules             []ReplicationRule
}

// ReplicationRule defines a replication rule
type ReplicationRule struct {
    ID       string
    Priority int
    Prefix   string
    Status   string
}

// StorageTier represents different storage tiers
type StorageTier string

const (
    StorageTierStandard         StorageTier = "STANDARD"
    StorageTierInfrequentAccess StorageTier = "STANDARD_IA"
    StorageTierGlacier          StorageTier = "GLACIER"
)

// AccessControlList manages ACLs for objects
type AccessControlList struct {
    mu   sync.RWMutex
    acls map[string]*ObjectACL
}

// NewAccessControlList creates a new ACL manager
func NewAccessControlList() *AccessControlList {
    return &AccessControlList{
        acls: make(map[string]*ObjectACL),
    }
}

// ObjectACL represents object-level access control
type ObjectACL struct {
    Owner  string
    Grants []Grant
}

// Grant represents a single ACL grant
type Grant struct {
    Grantee    string
    Permission string
}

// MultipartUpload tracks the state of a multipart upload
type MultipartUpload struct {
    UploadID  string
    Bucket    string
    Key       string
    Parts     map[int]*PartInfo
    Initiated time.Time
    mu        sync.RWMutex
}

// PartInfo contains information about a multipart upload part
type PartInfo struct {
    PartNumber int
    ETag       string
    Size       int64
}

// UnifiedStoragePool is the underlying storage pool
type UnifiedStoragePool struct {
    mu      sync.RWMutex
    storage map[string][]byte
}

// NewUnifiedStoragePool creates a new storage pool
func NewUnifiedStoragePool() *UnifiedStoragePool {
    return &UnifiedStoragePool{
        storage: make(map[string][]byte),
    }
}

// AllocateSpace allocates space in the storage pool
func (p *UnifiedStoragePool) AllocateSpace(id string, size uint64, compressed bool) (uint64, error) {
    p.mu.Lock()
    defer p.mu.Unlock()

    p.storage[id] = make([]byte, 0, size)
    return 0, nil
}

// WriteWithServices writes data to the storage pool
func (p *UnifiedStoragePool) WriteWithServices(id string, offset uint64, data []byte, size uint64) error {
    p.mu.Lock()
    defer p.mu.Unlock()

    p.storage[id] = data
    return nil
}

// ReadWithServices reads data from the storage pool
func (p *UnifiedStoragePool) ReadWithServices(id string, offset uint64, data []byte, size uint64) error {
    p.mu.RLock()
    defer p.mu.RUnlock()

    stored, exists := p.storage[id]
    if !exists {
        return fmt.Errorf("storage ID not found: %s", id)
    }

    copy(data, stored)
    return nil
}

// ReclaimSpace reclaims space in the storage pool
func (p *UnifiedStoragePool) ReclaimSpace(id string) error {
    p.mu.Lock()
    defer p.mu.Unlock()

    delete(p.storage, id)
    return nil
}