package nvmeof

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// NVMeoFTarget represents an NVMe-oF target (server-side)
type NVMeoFTarget struct {
	// Target configuration
	targetNQN    string
	listenAddr   string

	// Subsystems
	subsystems   map[string]*Subsystem
	subsystemsMu sync.RWMutex

	// Active connections
	connections  map[string]*TargetConnection
	connMu       sync.RWMutex

	// Listener
	listener     net.Listener

	// Running state
	running      bool
	runningMu    sync.Mutex
}

// Subsystem represents an NVMe subsystem on the target
type Subsystem struct {
	SubsystemNQN string
	Serial       string
	Model        string

	// Namespaces
	Namespaces   map[uint32]*TargetNamespace
	namespacesMu sync.RWMutex

	// Controllers
	Controllers  map[uint16]*TargetController
	controllersMu sync.RWMutex
	nextCtrlID   uint16

	// Allowed hosts
	AllowedHosts map[string]bool
	hostsMu      sync.RWMutex
}

// TargetNamespace represents a namespace served by the target
type TargetNamespace struct {
	NSID         uint32
	Size         uint64  // in bytes
	BlockSize    uint32
	NGUID        string
	UUID         string

	// Backend storage
	Backend      StorageBackend

	// Statistics
	ReadOps      uint64
	WriteOps     uint64
	BytesRead    uint64
	BytesWritten uint64
}

// StorageBackend interface for actual data storage
type StorageBackend interface {
	Read(offset uint64, length uint32) ([]byte, error)
	Write(offset uint64, data []byte) error
	Flush() error
	Size() uint64
}

// MemoryBackend implements in-memory storage
type MemoryBackend struct {
	data []byte
	mu   sync.RWMutex
}

func NewMemoryBackend(size uint64) *MemoryBackend {
	return &MemoryBackend{
		data: make([]byte, size),
	}
}

func (mb *MemoryBackend) Read(offset uint64, length uint32) ([]byte, error) {
	mb.mu.RLock()
	defer mb.mu.RUnlock()

	if offset+uint64(length) > uint64(len(mb.data)) {
		return nil, fmt.Errorf("read beyond end of device")
	}

	result := make([]byte, length)
	copy(result, mb.data[offset:offset+uint64(length)])
	return result, nil
}

func (mb *MemoryBackend) Write(offset uint64, data []byte) error {
	mb.mu.Lock()
	defer mb.mu.Unlock()

	if offset+uint64(len(data)) > uint64(len(mb.data)) {
		return fmt.Errorf("write beyond end of device")
	}

	copy(mb.data[offset:], data)
	return nil
}

func (mb *MemoryBackend) Flush() error {
	return nil
}

func (mb *MemoryBackend) Size() uint64 {
	return uint64(len(mb.data))
}

// TargetController represents a controller connection
type TargetController struct {
	ControllerID uint16
	HostNQN      string
	ConnectedAt  time.Time

	// Queue pairs
	AdminQueue   *TargetQueue
	IOQueues     map[uint16]*TargetQueue
	ioQueuesMu   sync.RWMutex
}

// TargetQueue represents a queue on the target side
type TargetQueue struct {
	QueueID      uint16
	QueueSize    uint16

	// Command processing
	processingMu sync.Mutex
}

// TargetConnection represents a client connection
type TargetConnection struct {
	ConnID       string
	Conn         net.Conn
	HostNQN      string
	SubsystemNQN string

	// Assigned controller
	Controller   *TargetController

	// Connection state
	Connected    bool
	ConnectedAt  time.Time

	// Statistics
	CommandsProcessed uint64
}

// NewNVMeoFTarget creates a new NVMe-oF target
func NewNVMeoFTarget(targetNQN string, listenAddr string) *NVMeoFTarget {
	return &NVMeoFTarget{
		targetNQN:   targetNQN,
		listenAddr:  listenAddr,
		subsystems:  make(map[string]*Subsystem),
		connections: make(map[string]*TargetConnection),
		running:     false,
	}
}

// CreateSubsystem creates a new subsystem
func (nt *NVMeoFTarget) CreateSubsystem(nqn string, serial string, model string) (*Subsystem, error) {
	nt.subsystemsMu.Lock()
	defer nt.subsystemsMu.Unlock()

	if _, exists := nt.subsystems[nqn]; exists {
		return nil, fmt.Errorf("subsystem already exists: %s", nqn)
	}

	subsystem := &Subsystem{
		SubsystemNQN: nqn,
		Serial:       serial,
		Model:        model,
		Namespaces:   make(map[uint32]*TargetNamespace),
		Controllers:  make(map[uint16]*TargetController),
		AllowedHosts: make(map[string]bool),
		nextCtrlID:   1,
	}

	nt.subsystems[nqn] = subsystem
	return subsystem, nil
}

// AddNamespace adds a namespace to a subsystem
func (s *Subsystem) AddNamespace(nsid uint32, size uint64, blockSize uint32) (*TargetNamespace, error) {
	s.namespacesMu.Lock()
	defer s.namespacesMu.Unlock()

	if _, exists := s.Namespaces[nsid]; exists {
		return nil, fmt.Errorf("namespace %d already exists", nsid)
	}

	// Create backend storage
	backend := NewMemoryBackend(size)

	namespace := &TargetNamespace{
		NSID:      nsid,
		Size:      size,
		BlockSize: blockSize,
		NGUID:     fmt.Sprintf("%032x", time.Now().UnixNano()),
		UUID:      generateNamespaceUUID(),
		Backend:   backend,
	}

	s.Namespaces[nsid] = namespace
	return namespace, nil
}

// AllowHost allows a host to connect to the subsystem
func (s *Subsystem) AllowHost(hostNQN string) {
	s.hostsMu.Lock()
	defer s.hostsMu.Unlock()
	s.AllowedHosts[hostNQN] = true
}

// Start starts the NVMe-oF target
func (nt *NVMeoFTarget) Start() error {
	nt.runningMu.Lock()
	defer nt.runningMu.Unlock()

	if nt.running {
		return fmt.Errorf("target already running")
	}

	// Create TCP listener
	listener, err := net.Listen("tcp", nt.listenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	nt.listener = listener
	nt.running = true

	// Start accepting connections
	go nt.acceptConnections()

	return nil
}

// acceptConnections accepts incoming connections
func (nt *NVMeoFTarget) acceptConnections() {
	for {
		nt.runningMu.Lock()
		running := nt.running
		nt.runningMu.Unlock()

		if !running {
			return
		}

		conn, err := nt.listener.Accept()
		if err != nil {
			continue
		}

		// Handle connection in goroutine
		go nt.handleConnection(conn)
	}
}

// handleConnection handles a client connection
func (nt *NVMeoFTarget) handleConnection(conn net.Conn) {
	defer conn.Close()

	targetConn := &TargetConnection{
		ConnID:      generateConnectionID(),
		Conn:        conn,
		Connected:   true,
		ConnectedAt: time.Now(),
	}

	// Process commands
	for {
		// Read command (64 bytes)
		cmdBuf := make([]byte, 64)
		n, err := conn.Read(cmdBuf)
		if err != nil || n < 64 {
			break
		}

		// Parse command
		cmd := parseCommand(cmdBuf)

		// Process command
		completion := nt.processCommand(targetConn, cmd)

		// Send completion
		compBuf := serializeCompletion(completion)
		conn.Write(compBuf)
	}

	// Cleanup
	nt.connMu.Lock()
	delete(nt.connections, targetConn.ConnID)
	nt.connMu.Unlock()
}

// processCommand processes an NVMe command
func (nt *NVMeoFTarget) processCommand(conn *TargetConnection, cmd *NVMeCommand) *NVMeCompletion {
	completion := &NVMeCompletion{
		CommandID: cmd.CommandID,
		Status:    NVMeStatusSuccess,
	}

	switch cmd.Opcode {
	case NVMeOpcodeFabrics:
		// Handle fabrics command
		return nt.processFabricsCommand(conn, cmd)

	case NVMeAdminIdentify:
		// Handle identify command
		return nt.processIdentifyCommand(conn, cmd)

	case NVMeCmdRead:
		// Handle read command
		return nt.processReadCommand(conn, cmd)

	case NVMeCmdWrite:
		// Handle write command
		return nt.processWriteCommand(conn, cmd)

	case NVMeCmdFlush:
		// Handle flush command
		return nt.processFlushCommand(conn, cmd)

	default:
		completion.Status = NVMeStatusInvalidOpcode
	}

	return completion
}

// processFabricsCommand processes fabrics-specific commands
func (nt *NVMeoFTarget) processFabricsCommand(conn *TargetConnection, cmd *NVMeCommand) *NVMeCompletion {
	completion := &NVMeCompletion{
		CommandID: cmd.CommandID,
		Status:    NVMeStatusSuccess,
	}

	if cmd.Fabrics.FCType == NVMeFabricsTypeConnect {
		// Handle connect command
		// Read connect data from connection
		// For now, simplified

		// Find subsystem
		nt.subsystemsMu.RLock()
		subsystem, exists := nt.subsystems[conn.SubsystemNQN]
		nt.subsystemsMu.RUnlock()

		if !exists {
			completion.Status = NVMeStatusInvalidField
			return completion
		}

		// Check if host is allowed
		subsystem.hostsMu.RLock()
		allowed := subsystem.AllowedHosts[conn.HostNQN]
		subsystem.hostsMu.RUnlock()

		if !allowed {
			completion.Status = NVMeStatusInvalidField
			return completion
		}

		// Create controller
		subsystem.controllersMu.Lock()
		ctrlID := subsystem.nextCtrlID
		subsystem.nextCtrlID++

		controller := &TargetController{
			ControllerID: ctrlID,
			HostNQN:      conn.HostNQN,
			ConnectedAt:  time.Now(),
			IOQueues:     make(map[uint16]*TargetQueue),
		}

		// Create admin queue if QID == 0
		if cmd.Fabrics.QID == 0 {
			controller.AdminQueue = &TargetQueue{
				QueueID:   0,
				QueueSize: 32,
			}
		} else {
			// Create I/O queue
			ioQueue := &TargetQueue{
				QueueID:   cmd.Fabrics.QID,
				QueueSize: cmd.Fabrics.SQSize,
			}
			controller.IOQueues[cmd.Fabrics.QID] = ioQueue
		}

		subsystem.Controllers[ctrlID] = controller
		subsystem.controllersMu.Unlock()

		conn.Controller = controller

		// Return controller ID in completion
		completion.DW0 = uint32(ctrlID)
	}

	return completion
}

// processIdentifyCommand processes identify commands
func (nt *NVMeoFTarget) processIdentifyCommand(conn *TargetConnection, cmd *NVMeCommand) *NVMeCompletion {
	completion := &NVMeCompletion{
		CommandID: cmd.CommandID,
		Status:    NVMeStatusSuccess,
	}

	// Get subsystem
	nt.subsystemsMu.RLock()
	subsystem, exists := nt.subsystems[conn.SubsystemNQN]
	nt.subsystemsMu.RUnlock()

	if !exists {
		completion.Status = NVMeStatusInternalError
		return completion
	}

	if cmd.Identify.CNS == NVMeIDCNSCtrl {
		// Identify controller - send controller data
		identifyData := make([]byte, 4096)

		// Number of namespaces
		subsystem.namespacesMu.RLock()
		nn := uint32(len(subsystem.Namespaces))
		subsystem.namespacesMu.RUnlock()

		binary.LittleEndian.PutUint32(identifyData[516:520], nn)

		// Send identify data
		conn.Conn.Write(identifyData)

	} else if cmd.Identify.CNS == NVMeIDCNSNS {
		// Identify namespace
		subsystem.namespacesMu.RLock()
		ns, exists := subsystem.Namespaces[cmd.NSID]
		subsystem.namespacesMu.RUnlock()

		if !exists {
			completion.Status = NVMeStatusInvalidField
			return completion
		}

		identifyData := make([]byte, 4096)

		// Namespace size
		nsze := ns.Size / uint64(ns.BlockSize)
		binary.LittleEndian.PutUint64(identifyData[0:8], nsze)

		// LBA format (assuming 4K blocks = 2^12)
		lbads := uint8(12) // log2(4096)
		identifyData[128] = lbads

		conn.Conn.Write(identifyData)
	}

	return completion
}

// processReadCommand processes read commands
func (nt *NVMeoFTarget) processReadCommand(conn *TargetConnection, cmd *NVMeCommand) *NVMeCompletion {
	completion := &NVMeCompletion{
		CommandID: cmd.CommandID,
		Status:    NVMeStatusSuccess,
	}

	// Get subsystem
	nt.subsystemsMu.RLock()
	subsystem, exists := nt.subsystems[conn.SubsystemNQN]
	nt.subsystemsMu.RUnlock()

	if !exists {
		completion.Status = NVMeStatusInternalError
		return completion
	}

	// Get namespace
	subsystem.namespacesMu.RLock()
	ns, exists := subsystem.Namespaces[cmd.NSID]
	subsystem.namespacesMu.RUnlock()

	if !exists {
		completion.Status = NVMeStatusInvalidField
		return completion
	}

	// Calculate offset and length
	lba := cmd.RW.SLBA
	numBlocks := uint32(cmd.RW.Length) + 1
	offset := lba * uint64(ns.BlockSize)
	length := numBlocks * ns.BlockSize

	// Read from backend
	data, err := ns.Backend.Read(offset, length)
	if err != nil {
		completion.Status = NVMeStatusDataXferError
		return completion
	}

	// Send data to initiator
	conn.Conn.Write(data)

	// Update statistics
	atomic.AddUint64(&ns.ReadOps, 1)
	atomic.AddUint64(&ns.BytesRead, uint64(length))

	return completion
}

// processWriteCommand processes write commands
func (nt *NVMeoFTarget) processWriteCommand(conn *TargetConnection, cmd *NVMeCommand) *NVMeCompletion {
	completion := &NVMeCompletion{
		CommandID: cmd.CommandID,
		Status:    NVMeStatusSuccess,
	}

	// Get subsystem and namespace
	nt.subsystemsMu.RLock()
	subsystem, exists := nt.subsystems[conn.SubsystemNQN]
	nt.subsystemsMu.RUnlock()

	if !exists {
		completion.Status = NVMeStatusInternalError
		return completion
	}

	subsystem.namespacesMu.RLock()
	ns, exists := subsystem.Namespaces[cmd.NSID]
	subsystem.namespacesMu.RUnlock()

	if !exists {
		completion.Status = NVMeStatusInvalidField
		return completion
	}

	// Calculate offset and length
	lba := cmd.RW.SLBA
	numBlocks := uint32(cmd.RW.Length) + 1
	offset := lba * uint64(ns.BlockSize)
	length := numBlocks * ns.BlockSize

	// Read data from connection
	data := make([]byte, length)
	n, err := conn.Conn.Read(data)
	if err != nil || uint32(n) < length {
		completion.Status = NVMeStatusDataXferError
		return completion
	}

	// Write to backend
	if err := ns.Backend.Write(offset, data); err != nil {
		completion.Status = NVMeStatusDataXferError
		return completion
	}

	// Update statistics
	atomic.AddUint64(&ns.WriteOps, 1)
	atomic.AddUint64(&ns.BytesWritten, uint64(length))

	return completion
}

// processFlushCommand processes flush commands
func (nt *NVMeoFTarget) processFlushCommand(conn *TargetConnection, cmd *NVMeCommand) *NVMeCompletion {
	completion := &NVMeCompletion{
		CommandID: cmd.CommandID,
		Status:    NVMeStatusSuccess,
	}

	// Get subsystem and namespace
	nt.subsystemsMu.RLock()
	subsystem, exists := nt.subsystems[conn.SubsystemNQN]
	nt.subsystemsMu.RUnlock()

	if !exists {
		completion.Status = NVMeStatusInternalError
		return completion
	}

	subsystem.namespacesMu.RLock()
	ns, exists := subsystem.Namespaces[cmd.NSID]
	subsystem.namespacesMu.RUnlock()

	if !exists {
		completion.Status = NVMeStatusInvalidField
		return completion
	}

	// Flush backend
	if err := ns.Backend.Flush(); err != nil {
		completion.Status = NVMeStatusInternalError
	}

	return completion
}

// Stop stops the NVMe-oF target
func (nt *NVMeoFTarget) Stop() error {
	nt.runningMu.Lock()
	defer nt.runningMu.Unlock()

	if !nt.running {
		return nil
	}

	nt.running = false

	if nt.listener != nil {
		return nt.listener.Close()
	}

	return nil
}

// Helper functions
func parseCommand(buf []byte) *NVMeCommand {
	cmd := &NVMeCommand{}

	cmd.Opcode = buf[0]
	cmd.Flags = buf[1]
	cmd.CommandID = binary.LittleEndian.Uint16(buf[2:4])
	cmd.NSID = binary.LittleEndian.Uint32(buf[4:8])

	cmd.MPTR = binary.LittleEndian.Uint64(buf[16:24])
	cmd.PRP1 = binary.LittleEndian.Uint64(buf[24:32])
	cmd.PRP2 = binary.LittleEndian.Uint64(buf[32:40])

	cmd.CDW10 = binary.LittleEndian.Uint32(buf[40:44])
	cmd.CDW11 = binary.LittleEndian.Uint32(buf[44:48])
	cmd.CDW12 = binary.LittleEndian.Uint32(buf[48:52])
	cmd.CDW13 = binary.LittleEndian.Uint32(buf[52:56])
	cmd.CDW14 = binary.LittleEndian.Uint32(buf[56:60])
	cmd.CDW15 = binary.LittleEndian.Uint32(buf[60:64])

	// Parse union fields based on opcode
	if cmd.Opcode == NVMeCmdRead || cmd.Opcode == NVMeCmdWrite {
		cmd.RW.SLBA = binary.LittleEndian.Uint64(buf[40:48])
		cmd.RW.Length = binary.LittleEndian.Uint16(buf[48:50])
	} else if cmd.Opcode == NVMeAdminIdentify {
		cmd.Identify.CNS = buf[40]
	} else if cmd.Opcode == NVMeOpcodeFabrics {
		cmd.Fabrics.FCType = buf[40]
		cmd.Fabrics.QID = binary.LittleEndian.Uint16(buf[86:88])
		cmd.Fabrics.SQSize = binary.LittleEndian.Uint16(buf[84:86])
	}

	return cmd
}

func serializeCompletion(comp *NVMeCompletion) []byte {
	buf := make([]byte, 16)

	binary.LittleEndian.PutUint32(buf[0:4], comp.DW0)
	binary.LittleEndian.PutUint16(buf[8:10], comp.SQHD)
	binary.LittleEndian.PutUint16(buf[10:12], comp.SQID)
	binary.LittleEndian.PutUint16(buf[12:14], comp.CommandID)
	binary.LittleEndian.PutUint16(buf[14:16], comp.Status)

	return buf
}

// Helper functions (also in initiator.go)
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
