package dpu

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/srilakshmi/storage/nvmeof"
)

// Client provides a local in-process DPU client implementation.
// This is a lightweight placeholder for a real gRPC or DOCA-backed client.
type Client struct {
	addr       string
	connected  bool
	mu         sync.RWMutex
	metrics    *Metrics
	targets    map[string]*NVMeoFTarget
	connectLag time.Duration
}

// Metrics tracks DPU activity.
type Metrics struct {
	Requests     uint64
	Offloads     uint64
	Failures     uint64
	LastActivity time.Time
	mu           sync.Mutex
}

// NVMeTargetConfig defines a DPU NVMe-oF target request.
type NVMeTargetConfig struct {
	SubsystemNQN string
	ListenAddr  string
	HostNQN     string
	NamespaceID uint32
	SizeBytes   uint64
	BlockSize   uint32
}

// NVMeoFTarget represents a DPU-managed NVMe-oF target.
type NVMeoFTarget struct {
	Config   NVMeTargetConfig
	target   *nvmeof.NVMeoFTarget
	running  bool
	started  time.Time
	lastTick time.Time
	mu       sync.RWMutex
}

// NewClient creates a new DPU client.
func NewClient(addr string) *Client {
	return &Client{
		addr:       addr,
		metrics:    &Metrics{},
		targets:    make(map[string]*NVMeoFTarget),
		connectLag: 20 * time.Millisecond,
	}
}

// Connect simulates establishing a DPU control-plane session.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected {
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(c.connectLag):
		c.connected = true
		c.metrics.touch()
		return nil
	}
}

// Close disconnects the DPU client.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected = false
	return nil
}

// OffloadNVMeTarget registers an NVMe-oF target on the DPU.
func (c *Client) OffloadNVMeTarget(ctx context.Context, cfg NVMeTargetConfig) (*NVMeoFTarget, error) {
	if err := c.ensureConnected(ctx); err != nil {
		return nil, err
	}
	if cfg.SubsystemNQN == "" || cfg.ListenAddr == "" {
		return nil, fmt.Errorf("invalid NVMe target config")
	}
	if cfg.HostNQN == "" {
		cfg.HostNQN = "nqn.host"
	}
	if cfg.BlockSize == 0 {
		cfg.BlockSize = 4096
	}
	if cfg.SizeBytes == 0 {
		cfg.SizeBytes = 1024 * 1024 * 1024
	}
	if cfg.NamespaceID == 0 {
		cfg.NamespaceID = 1
	}

	c.metrics.incrementOffload()

	target := nvmeof.NewNVMeoFTarget(cfg.SubsystemNQN, cfg.ListenAddr)
	subsys, err := target.CreateSubsystem(cfg.SubsystemNQN, "DPU-SN", "DPU-Model")
	if err != nil {
		c.metrics.incrementFailure()
		return nil, err
	}
	if _, err := subsys.AddNamespace(cfg.NamespaceID, cfg.SizeBytes, cfg.BlockSize); err != nil {
		c.metrics.incrementFailure()
		return nil, err
	}
	subsys.AllowHost(cfg.HostNQN)
	if err := target.Start(); err != nil {
		c.metrics.incrementFailure()
		return nil, err
	}

	offload := &NVMeoFTarget{
		Config:  cfg,
		target:  target,
		running: true,
		started: time.Now(),
	}

	c.mu.Lock()
	c.targets[cfg.SubsystemNQN] = offload
	c.mu.Unlock()

	return offload, nil
}

// StopNVMeTarget stops a DPU target by NQN.
func (c *Client) StopNVMeTarget(ctx context.Context, nqn string) error {
	if err := c.ensureConnected(ctx); err != nil {
		return err
	}

	c.mu.Lock()
	target, ok := c.targets[nqn]
	if ok {
		if target.target != nil {
			_ = target.target.Stop()
		}
		target.mu.Lock()
		target.running = false
		target.mu.Unlock()
		delete(c.targets, nqn)
	}
	c.mu.Unlock()

	c.metrics.touch()
	if !ok {
		return fmt.Errorf("target not found: %s", nqn)
	}
	return nil
}

// ListTargets returns current targets.
func (c *Client) ListTargets() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]string, 0, len(c.targets))
	for nqn := range c.targets {
		result = append(result, nqn)
	}
	return result
}

// Metrics returns a snapshot of DPU metrics.
func (c *Client) Metrics() Metrics {
	c.metrics.mu.Lock()
	defer c.metrics.mu.Unlock()
	return Metrics{
		Requests:     c.metrics.Requests,
		Offloads:     c.metrics.Offloads,
		Failures:     c.metrics.Failures,
		LastActivity: c.metrics.LastActivity,
	}
}

// SetConnectLatency controls simulated connect time.
func (c *Client) SetConnectLatency(latency time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connectLag = latency
}

// Start ensures the target is running.
func (t *NVMeoFTarget) Start() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running = true
	t.lastTick = time.Now()
	if t.started.IsZero() {
		t.started = time.Now()
	}
	if t.target != nil {
		_ = t.target.Start()
	}
}

// Stop marks the target as stopped.
func (t *NVMeoFTarget) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running = false
	t.lastTick = time.Now()
	if t.target != nil {
		_ = t.target.Stop()
	}
}

// Running reports whether the target is active.
func (t *NVMeoFTarget) Running() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.running
}

func (c *Client) ensureConnected(ctx context.Context) error {
	c.mu.RLock()
	connected := c.connected
	c.mu.RUnlock()

	if connected {
		c.metrics.touch()
		return nil
	}
	return c.Connect(ctx)
}

func (m *Metrics) incrementOffload() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Offloads++
	m.Requests++
	m.LastActivity = time.Now()
}

func (m *Metrics) incrementFailure() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Failures++
	m.Requests++
	m.LastActivity = time.Now()
}

func (m *Metrics) touch() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Requests++
	m.LastActivity = time.Now()
}
