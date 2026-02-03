package nvmeof

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// NVMe-oF initiator (host-side) implementation
type NVMeoFInitiator struct {
	// Connection to target
	conn         net.Conn

	// Host NQN
	hostNQN      string

	// Connected subsystems
	subsystems   map[string]*ConnectedSubsystem
	subsystemsMu sync.RWMutex

	// I/O queues
	ioQueues     []*IOQueue
	ioQueuesMu   sync.RWMutex

	// Admin queue
	adminQueue   *AdminQueue

	// Queue configuration
	ioQueueCount  int
	ioQueueSize   uint16
	adminQueueSize uint16
	queueCursor   uint32

	// Running state
	running      bool
	runningMu    sync.Mutex
}

// InitiatorConfig controls queue sizing and counts.
type InitiatorConfig struct {
	IOQueueCount  int
	IOQueueSize   uint16
	AdminQueueSize uint16
}

type ConnectedSubsystem struct {
	NQN          string
	ControllerID uint16

	// Namespaces
	Namespaces   map[uint32]*Namespace

	// Connection state
	Connected    bool
	ConnectedAt  time.Time
}

type Namespace struct {
	NSID         uint32
	CapacityBlocks uint64
	BlockSize    uint32

	// Features
	SupportsTRIM      bool
	SupportsFlush     bool
	SupportsWriteZeros bool
}

type IOQueue struct {
	QID          uint16
	Size         uint16

	// Command submission
	commandsMu   sync.Mutex
	nextCmdID    uint16

	// Pending commands
	pending      map[uint16]*PendingCommand
	pendingMu    sync.RWMutex

	// Performance stats
	commandsSubmitted  uint64
	commandsCompleted  uint64
	totalLatencyNs     uint64
}

type AdminQueue struct {
	QID          uint16
	Size         uint16

	// Command submission
	commandsMu   sync.Mutex
	nextCmdID    uint16

	// Pending commands
	pending      map[uint16]*PendingCommand
	pendingMu    sync.RWMutex
}

type PendingCommand struct {
	CommandID    uint16
	SubmitTime   time.Time
	Callback     func(status uint16, result uint64)
	DataBuffer   []byte
}

// NVMe Command structure
type NVMeCommand struct {
	Opcode       uint8
	Flags        uint8
	CommandID    uint16
	NSID         uint32

	Reserved     uint64

	MPTR         uint64  // Metadata pointer
	PRP1         uint64  // Physical Region Page 1
	PRP2         uint64  // Physical Region Page 2

	CDW10        uint32
	CDW11        uint32
	CDW12        uint32
	CDW13        uint32
	CDW14        uint32
	CDW15        uint32

	// Union fields for different command types
	RW           NVMeRWCommand
	Identify     NVMeIdentifyCommand
	Fabrics      NVMeFabricsCommand
}

// Read/Write command fields
type NVMeRWCommand struct {
	SLBA         uint64  // Starting LBA
	Length       uint16  // Number of logical blocks
	Control      uint16
	DSMGMT       uint32
	REFTAG       uint32
	APPTAG       uint16
	APPMASK      uint16
}

// Identify command fields
type NVMeIdentifyCommand struct {
	CNS          uint8   // Controller or Namespace Structure
	Reserved     uint8
	ControllerID uint16
	UUID         [16]byte
}

// Fabrics command fields
type NVMeFabricsCommand struct {
	FCType       uint8   // Fabrics Command Type
	Reserved1    [35]byte
	Attributes   uint8
	Reserved2    [2]byte
	SQSize       uint16  // Submission Queue Size
	QID          uint16  // Queue ID
	CQID         uint16  // Completion Queue ID
}

// Connect data for fabrics
type ConnectData struct {
	HostNQN      string
	SubsysNQN    string
	RecordFormat uint16
	Reserved     [2]byte
}

// NVMe Completion Queue Entry
type NVMeCompletion struct {
	DW0          uint32  // Command specific
	Reserved     uint32
	SQHD         uint16  // Submission Queue Head
	SQID         uint16  // Submission Queue Identifier
	CommandID    uint16
	Status       uint16
}

// NVMe Command Opcodes
const (
	// Admin Commands
	NVMeAdminDeleteSQ     = 0x00
	NVMeAdminCreateSQ     = 0x01
	NVMeAdminGetLogPage   = 0x02
	NVMeAdminDeleteCQ     = 0x04
	NVMeAdminCreateCQ     = 0x05
	NVMeAdminIdentify     = 0x06
	NVMeAdminAbort        = 0x08
	NVMeAdminSetFeatures  = 0x09
	NVMeAdminGetFeatures  = 0x0A
	NVMeAdminAsyncEvent   = 0x0C
	NVMeAdminNSManage     = 0x0D
	NVMeAdminActivateFW   = 0x10
	NVMeAdminDownloadFW   = 0x11
	NVMeAdminDeviceSelfTest = 0x14
	NVMeAdminNSAttach     = 0x15

	// I/O Commands
	NVMeCmdFlush          = 0x00
	NVMeCmdWrite          = 0x01
	NVMeCmdRead           = 0x02
	NVMeCmdWriteUncor     = 0x04
	NVMeCmdCompare        = 0x05
	NVMeCmdWriteZeroes    = 0x08
	NVMeCmdDSM            = 0x09
	NVMeCmdVerify         = 0x0C
	NVMeCmdResv           = 0x0D
	NVMeCmdResvReg        = 0x0D
	NVMeCmdResvReport     = 0x0E
	NVMeCmdResvAcquire    = 0x11
	NVMeCmdResvRelease    = 0x15

	// Fabrics Commands
	NVMeOpcodeFabrics     = 0x7F
)

// Fabrics Command Types
const (
	NVMeFabricsTypePropertySet   = 0x00
	NVMeFabricsTypeConnect        = 0x01
	NVMeFabricsTypePropertyGet    = 0x04
	NVMeFabricsTypeAuthSend       = 0x05
	NVMeFabricsTypeAuthReceive    = 0x06
	NVMeFabricsTypeDisconnect     = 0x08
)

// Identify CNS values
const (
	NVMeIDCNSNS           = 0x00  // Identify Namespace
	NVMeIDCNSCtrl         = 0x01  // Identify Controller
	NVMeIDCNSActiveNS     = 0x02  // Active Namespace List
	NVMeIDCNSNSDescList   = 0x03  // Namespace Identification Descriptor
)

// Status codes
const (
	NVMeStatusSuccess     = 0x0000
	NVMeStatusInvalidOpcode = 0x0001
	NVMeStatusInvalidField  = 0x0002
	NVMeStatusCmdIDConflict = 0x0003
	NVMeStatusDataXferError = 0x0004
	NVMeStatusAbortedPowerLoss = 0x0005
	NVMeStatusInternalError = 0x0006
)

func NewNVMeoFInitiator(targetAddr string, hostNQN string) (*NVMeoFInitiator, error) {
	return NewNVMeoFInitiatorWithConfig(targetAddr, hostNQN, InitiatorConfig{
		IOQueueCount:  4,
		IOQueueSize:   128,
		AdminQueueSize: 32,
	})
}

func NewNVMeoFInitiatorWithConfig(targetAddr string, hostNQN string, cfg InitiatorConfig) (*NVMeoFInitiator, error) {
	if cfg.IOQueueCount <= 0 {
		cfg.IOQueueCount = 1
	}
	if cfg.IOQueueSize == 0 {
		cfg.IOQueueSize = 128
	}
	if cfg.AdminQueueSize == 0 {
		cfg.AdminQueueSize = 32
	}

	// Connect to target via TCP
	conn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	initiator := &NVMeoFInitiator{
		conn:       conn,
		hostNQN:    hostNQN,
		subsystems: make(map[string]*ConnectedSubsystem),
		ioQueueCount:  cfg.IOQueueCount,
		ioQueueSize:   cfg.IOQueueSize,
		adminQueueSize: cfg.AdminQueueSize,
		running:    true,
	}

	// Create admin queue
	initiator.adminQueue = &AdminQueue{
		QID:     0,
		Size:    cfg.AdminQueueSize,
		pending: make(map[uint16]*PendingCommand),
	}

	// Start completion handler
	go initiator.handleCompletions()

	return initiator, nil
}

// Connect to subsystem
func (ni *NVMeoFInitiator) ConnectSubsystem(ctx context.Context, nqn string) error {
	// Build connect command
	cmd := NVMeCommand{
		Opcode: NVMeOpcodeFabrics,
		NSID:   0,
	}

	// Set fabrics-specific fields
	cmd.Fabrics.FCType = NVMeFabricsTypeConnect
	cmd.Fabrics.QID = 0  // Admin queue

	// Send connect data
	connectData := ConnectData{
		HostNQN:    ni.hostNQN,
		SubsysNQN:  nqn,
		RecordFormat: 0,
	}

	// Submit connect command
	result, status, err := ni.adminQueue.SubmitCommandSync(ctx, ni.conn, &cmd, &connectData)
	if err != nil {
		return err
	}

	if status != NVMeStatusSuccess {
		return fmt.Errorf("connect failed with status: 0x%x", status)
	}

	// Extract controller ID from result
	controllerID := uint16(result)

	// Create subsystem record
	subsys := &ConnectedSubsystem{
		NQN:          nqn,
		ControllerID: controllerID,
		Namespaces:   make(map[uint32]*Namespace),
		Connected:    true,
		ConnectedAt:  time.Now(),
	}

	ni.subsystemsMu.Lock()
	ni.subsystems[nqn] = subsys
	ni.subsystemsMu.Unlock()

	// Identify controller
	if err := ni.identifyController(ctx, subsys); err != nil {
		return err
	}

	// Create I/O queues
	if err := ni.createIOQueues(ctx, subsys); err != nil {
		return err
	}

	return nil
}

func (ni *NVMeoFInitiator) identifyController(ctx context.Context,
	subsys *ConnectedSubsystem) error {

	cmd := NVMeCommand{
		Opcode: NVMeAdminIdentify,
		NSID:   0,
	}

	cmd.Identify.CNS = NVMeIDCNSCtrl

	identifyBuf := make([]byte, 4096)

	_, status, err := ni.adminQueue.SubmitCommandSync(ctx, ni.conn, &cmd, identifyBuf)
	if err != nil {
		return err
	}

	if status != NVMeStatusSuccess {
		return fmt.Errorf("identify failed: 0x%x", status)
	}

	// Parse identify data
	nn := binary.LittleEndian.Uint32(identifyBuf[516:520])  // Number of namespaces

	// Identify each namespace
	for nsid := uint32(1); nsid <= nn; nsid++ {
		if err := ni.identifyNamespace(ctx, subsys, nsid); err != nil {
			continue  // Skip failed namespaces
		}
	}

	return nil
}

func (ni *NVMeoFInitiator) identifyNamespace(ctx context.Context,
	subsys *ConnectedSubsystem,
	nsid uint32) error {

	cmd := NVMeCommand{
		Opcode: NVMeAdminIdentify,
		NSID:   nsid,
	}

	cmd.Identify.CNS = NVMeIDCNSNS

	identifyBuf := make([]byte, 4096)

	_, status, err := ni.adminQueue.SubmitCommandSync(ctx, ni.conn, &cmd, identifyBuf)
	if err != nil {
		return err
	}

	if status != NVMeStatusSuccess {
		return fmt.Errorf("identify namespace failed: 0x%x", status)
	}

	// Parse namespace data
	nsze := binary.LittleEndian.Uint64(identifyBuf[0:8])      // Size
	flbas := identifyBuf[26]                                  // Formatted LBA size
	lbaFormat := flbas & 0xF

	// LBA format structure at offset 128
	lbafOffset := 128 + (int(lbaFormat) * 4)
	lbads := identifyBuf[lbafOffset]  // LBA data size (power of 2)
	blockSize := uint32(1 << lbads)

	ns := &Namespace{
		NSID:           nsid,
		CapacityBlocks: nsze,
		BlockSize:      blockSize,
		SupportsTRIM:   true,
		SupportsFlush:  true,
		SupportsWriteZeros: true,
	}

	subsys.Namespaces[nsid] = ns

	return nil
}

func (ni *NVMeoFInitiator) createIOQueues(ctx context.Context,
	subsys *ConnectedSubsystem) error {

	numQueues := ni.ioQueueCount

	for i := 0; i < numQueues; i++ {
		qid := uint16(i + 1)

		// Create I/O queue via fabric connect
		cmd := NVMeCommand{
			Opcode: NVMeOpcodeFabrics,
		}

		cmd.Fabrics.FCType = NVMeFabricsTypeConnect
		cmd.Fabrics.QID = qid
		cmd.Fabrics.SQSize = ni.ioQueueSize  // Queue size

		connectData := ConnectData{
			HostNQN:   ni.hostNQN,
			SubsysNQN: subsys.NQN,
		}

		_, status, err := ni.adminQueue.SubmitCommandSync(ctx, ni.conn, &cmd, &connectData)
		if err != nil {
			return err
		}

		if status != NVMeStatusSuccess {
			return fmt.Errorf("I/O queue connect failed: 0x%x", status)
		}

		// Create queue object
		queue := &IOQueue{
			QID:     qid,
			Size:    ni.ioQueueSize,
			pending: make(map[uint16]*PendingCommand),
		}

		ni.ioQueuesMu.Lock()
		ni.ioQueues = append(ni.ioQueues, queue)
		ni.ioQueuesMu.Unlock()
	}

	return nil
}

// QueueCount returns the number of I/O queues.
func (ni *NVMeoFInitiator) QueueCount() int {
	ni.ioQueuesMu.RLock()
	defer ni.ioQueuesMu.RUnlock()
	return len(ni.ioQueues)
}

func (ni *NVMeoFInitiator) pickQueue() (*IOQueue, error) {
	ni.ioQueuesMu.RLock()
	defer ni.ioQueuesMu.RUnlock()

	if len(ni.ioQueues) == 0 {
		return nil, fmt.Errorf("no I/O queues available")
	}
	index := int(atomic.AddUint32(&ni.queueCursor, 1)-1) % len(ni.ioQueues)
	return ni.ioQueues[index], nil
}

// Read from namespace
func (ni *NVMeoFInitiator) Read(ctx context.Context, nqn string, nsid uint32,
	lba uint64, numBlocks uint32,
	buffer []byte) error {

	ni.subsystemsMu.RLock()
	subsys, exists := ni.subsystems[nqn]
	ni.subsystemsMu.RUnlock()

	if !exists {
		return fmt.Errorf("subsystem not connected")
	}

	ns, exists := subsys.Namespaces[nsid]
	if !exists {
		return fmt.Errorf("namespace not found")
	}

	// Validate request
	if uint64(len(buffer)) < uint64(numBlocks)*uint64(ns.BlockSize) {
		return fmt.Errorf("buffer too small")
	}

	queue, err := ni.pickQueue()
	if err != nil {
		return err
	}

	// Build read command
	cmd := NVMeCommand{
		Opcode: NVMeCmdRead,
		NSID:   nsid,
	}

	cmd.RW.SLBA = lba
	cmd.RW.Length = uint16(numBlocks - 1)

	// Submit command
	done := make(chan error, 1)

	queue.SubmitCommand(ni.conn, &cmd, buffer, func(status uint16, result uint64) {
		if status != NVMeStatusSuccess {
			done <- fmt.Errorf("read failed: 0x%x", status)
		} else {
			done <- nil
		}
	})

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReadWithQueue reads using a specific I/O queue index.
func (ni *NVMeoFInitiator) ReadWithQueue(ctx context.Context, nqn string, nsid uint32,
	lba uint64, numBlocks uint32, buffer []byte, queueIndex int) error {

	ni.subsystemsMu.RLock()
	subsys, exists := ni.subsystems[nqn]
	ni.subsystemsMu.RUnlock()

	if !exists {
		return fmt.Errorf("subsystem not connected")
	}

	ns, exists := subsys.Namespaces[nsid]
	if !exists {
		return fmt.Errorf("namespace not found")
	}

	if uint64(len(buffer)) < uint64(numBlocks)*uint64(ns.BlockSize) {
		return fmt.Errorf("buffer too small")
	}

	ni.ioQueuesMu.RLock()
	defer ni.ioQueuesMu.RUnlock()

	if queueIndex < 0 || queueIndex >= len(ni.ioQueues) {
		return fmt.Errorf("invalid queue index")
	}
	queue := ni.ioQueues[queueIndex]

	cmd := NVMeCommand{
		Opcode: NVMeCmdRead,
		NSID:   nsid,
	}

	cmd.RW.SLBA = lba
	cmd.RW.Length = uint16(numBlocks - 1)

	done := make(chan error, 1)
	queue.SubmitCommand(ni.conn, &cmd, buffer, func(status uint16, result uint64) {
		if status != NVMeStatusSuccess {
			done <- fmt.Errorf("read failed: 0x%x", status)
		} else {
			done <- nil
		}
	})

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Write to namespace
func (ni *NVMeoFInitiator) Write(ctx context.Context, nqn string, nsid uint32,
	lba uint64, numBlocks uint32,
	buffer []byte) error {

	ni.subsystemsMu.RLock()
	subsys, exists := ni.subsystems[nqn]
	ni.subsystemsMu.RUnlock()

	if !exists {
		return fmt.Errorf("subsystem not connected")
	}

	ns, exists := subsys.Namespaces[nsid]
	if !exists {
		return fmt.Errorf("namespace not found")
	}

	// Validate request
	if uint64(len(buffer)) < uint64(numBlocks)*uint64(ns.BlockSize) {
		return fmt.Errorf("buffer too small")
	}

	queue, err := ni.pickQueue()
	if err != nil {
		return err
	}

	cmd := NVMeCommand{
		Opcode: NVMeCmdWrite,
		NSID:   nsid,
	}

	cmd.RW.SLBA = lba
	cmd.RW.Length = uint16(numBlocks - 1)

	done := make(chan error, 1)

	queue.SubmitCommand(ni.conn, &cmd, buffer, func(status uint16, result uint64) {
		if status != NVMeStatusSuccess {
			done <- fmt.Errorf("write failed: 0x%x", status)
		} else {
			done <- nil
		}
	})

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WriteWithQueue writes using a specific I/O queue index.
func (ni *NVMeoFInitiator) WriteWithQueue(ctx context.Context, nqn string, nsid uint32,
	lba uint64, numBlocks uint32, buffer []byte, queueIndex int) error {

	ni.subsystemsMu.RLock()
	subsys, exists := ni.subsystems[nqn]
	ni.subsystemsMu.RUnlock()

	if !exists {
		return fmt.Errorf("subsystem not connected")
	}

	ns, exists := subsys.Namespaces[nsid]
	if !exists {
		return fmt.Errorf("namespace not found")
	}

	if uint64(len(buffer)) < uint64(numBlocks)*uint64(ns.BlockSize) {
		return fmt.Errorf("buffer too small")
	}

	ni.ioQueuesMu.RLock()
	defer ni.ioQueuesMu.RUnlock()

	if queueIndex < 0 || queueIndex >= len(ni.ioQueues) {
		return fmt.Errorf("invalid queue index")
	}
	queue := ni.ioQueues[queueIndex]

	cmd := NVMeCommand{
		Opcode: NVMeCmdWrite,
		NSID:   nsid,
	}

	cmd.RW.SLBA = lba
	cmd.RW.Length = uint16(numBlocks - 1)

	done := make(chan error, 1)
	queue.SubmitCommand(ni.conn, &cmd, buffer, func(status uint16, result uint64) {
		if status != NVMeStatusSuccess {
			done <- fmt.Errorf("write failed: 0x%x", status)
		} else {
			done <- nil
		}
	})

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Flush namespace
func (ni *NVMeoFInitiator) Flush(ctx context.Context, nqn string, nsid uint32) error {
	queue, err := ni.pickQueue()
	if err != nil {
		return err
	}

	cmd := NVMeCommand{
		Opcode: NVMeCmdFlush,
		NSID:   nsid,
	}

	done := make(chan error, 1)

	queue.SubmitCommand(ni.conn, &cmd, nil, func(status uint16, result uint64) {
		if status != NVMeStatusSuccess {
			done <- fmt.Errorf("flush failed: 0x%x", status)
		} else {
			done <- nil
		}
	})

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// FlushWithQueue flushes using a specific I/O queue index.
func (ni *NVMeoFInitiator) FlushWithQueue(ctx context.Context, nqn string, nsid uint32, queueIndex int) error {
	ni.ioQueuesMu.RLock()
	defer ni.ioQueuesMu.RUnlock()

	if queueIndex < 0 || queueIndex >= len(ni.ioQueues) {
		return fmt.Errorf("invalid queue index")
	}
	queue := ni.ioQueues[queueIndex]

	cmd := NVMeCommand{
		Opcode: NVMeCmdFlush,
		NSID:   nsid,
	}

	done := make(chan error, 1)

	queue.SubmitCommand(ni.conn, &cmd, nil, func(status uint16, result uint64) {
		if status != NVMeStatusSuccess {
			done <- fmt.Errorf("flush failed: 0x%x", status)
		} else {
			done <- nil
		}
	})

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close initiator connection
func (ni *NVMeoFInitiator) Close() error {
	ni.runningMu.Lock()
	ni.running = false
	ni.runningMu.Unlock()

	if ni.conn != nil {
		return ni.conn.Close()
	}
	return nil
}

func (ni *NVMeoFInitiator) handleCompletions() {
	for {
		ni.runningMu.Lock()
		running := ni.running
		ni.runningMu.Unlock()

		if !running {
			return
		}

		// Read completion from connection
		completion := &NVMeCompletion{}

		// Set read deadline
		ni.conn.SetReadDeadline(time.Now().Add(1 * time.Second))

		// Read completion entry (16 bytes)
		buf := make([]byte, 16)
		n, err := ni.conn.Read(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}

		if n < 16 {
			continue
		}

		// Parse completion
		completion.DW0 = binary.LittleEndian.Uint32(buf[0:4])
		completion.SQHD = binary.LittleEndian.Uint16(buf[8:10])
		completion.SQID = binary.LittleEndian.Uint16(buf[10:12])
		completion.CommandID = binary.LittleEndian.Uint16(buf[12:14])
		completion.Status = binary.LittleEndian.Uint16(buf[14:16])

		// Dispatch to appropriate queue
		ni.dispatchCompletion(completion)
	}
}

func (ni *NVMeoFInitiator) dispatchCompletion(completion *NVMeCompletion) {
	// Check if admin queue or I/O queue
	if completion.SQID == 0 {
		// Admin queue completion
		ni.adminQueue.HandleCompletion(completion)
	} else {
		// I/O queue completion
		ni.ioQueuesMu.RLock()
		defer ni.ioQueuesMu.RUnlock()
		for _, queue := range ni.ioQueues {
			if queue.QID == completion.SQID {
				queue.HandleCompletion(completion)
				break
			}
		}
	}
}

func (q *IOQueue) SubmitCommand(conn net.Conn, cmd *NVMeCommand, data []byte,
	callback func(uint16, uint64)) {

	q.commandsMu.Lock()
	cid := q.nextCmdID
	q.nextCmdID++
	q.commandsMu.Unlock()

	cmd.CommandID = cid

	// Store pending command
	pending := &PendingCommand{
		CommandID:  cid,
		SubmitTime: time.Now(),
		Callback:   callback,
		DataBuffer: data,
	}

	q.pendingMu.Lock()
	q.pending[cid] = pending
	q.pendingMu.Unlock()

	// Serialize and send command over fabric
	cmdBytes := serializeCommand(cmd)
	conn.Write(cmdBytes)

	// Send data if present
	if data != nil && len(data) > 0 {
		conn.Write(data)
	}

	atomic.AddUint64(&q.commandsSubmitted, 1)
}

func (q *IOQueue) HandleCompletion(completion *NVMeCompletion) {
	q.pendingMu.Lock()
	pending, exists := q.pending[completion.CommandID]
	if exists {
		delete(q.pending, completion.CommandID)
	}
	q.pendingMu.Unlock()

	if !exists {
		return
	}

	// Calculate latency
	latency := time.Since(pending.SubmitTime)
	atomic.AddUint64(&q.totalLatencyNs, uint64(latency.Nanoseconds()))
	atomic.AddUint64(&q.commandsCompleted, 1)

	// Call callback
	if pending.Callback != nil {
		pending.Callback(completion.Status, uint64(completion.DW0))
	}
}

func (aq *AdminQueue) SubmitCommandSync(ctx context.Context, conn net.Conn,
	cmd *NVMeCommand, data interface{}) (uint64, uint16, error) {

	resultChan := make(chan struct {
		result uint64
		status uint16
	}, 1)

	aq.commandsMu.Lock()
	cid := aq.nextCmdID
	aq.nextCmdID++
	aq.commandsMu.Unlock()

	cmd.CommandID = cid

	// Store pending command
	pending := &PendingCommand{
		CommandID:  cid,
		SubmitTime: time.Now(),
		Callback: func(status uint16, result uint64) {
			resultChan <- struct {
				result uint64
				status uint16
			}{result, status}
		},
	}

	aq.pendingMu.Lock()
	aq.pending[cid] = pending
	aq.pendingMu.Unlock()

	// Serialize and send
	cmdBytes := serializeCommand(cmd)
	conn.Write(cmdBytes)

	// Send data if needed
	if data != nil {
		// Serialize data based on type
		// For now, simplified
	}

	// Wait for completion
	select {
	case result := <-resultChan:
		return result.result, result.status, nil
	case <-ctx.Done():
		return 0, 0, ctx.Err()
	case <-time.After(30 * time.Second):
		return 0, 0, fmt.Errorf("command timeout")
	}
}

func (aq *AdminQueue) HandleCompletion(completion *NVMeCompletion) {
	aq.pendingMu.Lock()
	pending, exists := aq.pending[completion.CommandID]
	if exists {
		delete(aq.pending, completion.CommandID)
	}
	aq.pendingMu.Unlock()

	if !exists {
		return
	}

	// Call callback
	if pending.Callback != nil {
		pending.Callback(completion.Status, uint64(completion.DW0))
	}
}

func serializeCommand(cmd *NVMeCommand) []byte {
	// NVMe command is 64 bytes
	buf := make([]byte, 64)

	buf[0] = cmd.Opcode
	buf[1] = cmd.Flags
	binary.LittleEndian.PutUint16(buf[2:4], cmd.CommandID)
	binary.LittleEndian.PutUint32(buf[4:8], cmd.NSID)

	binary.LittleEndian.PutUint64(buf[16:24], cmd.MPTR)
	binary.LittleEndian.PutUint64(buf[24:32], cmd.PRP1)
	binary.LittleEndian.PutUint64(buf[32:40], cmd.PRP2)

	binary.LittleEndian.PutUint32(buf[40:44], cmd.CDW10)
	binary.LittleEndian.PutUint32(buf[44:48], cmd.CDW11)
	binary.LittleEndian.PutUint32(buf[48:52], cmd.CDW12)
	binary.LittleEndian.PutUint32(buf[52:56], cmd.CDW13)
	binary.LittleEndian.PutUint32(buf[56:60], cmd.CDW14)
	binary.LittleEndian.PutUint32(buf[60:64], cmd.CDW15)

	return buf
}

// GetStatistics returns queue statistics
func (q *IOQueue) GetStatistics() map[string]uint64 {
	return map[string]uint64{
		"commands_submitted": atomic.LoadUint64(&q.commandsSubmitted),
		"commands_completed": atomic.LoadUint64(&q.commandsCompleted),
		"total_latency_ns":   atomic.LoadUint64(&q.totalLatencyNs),
	}
}
