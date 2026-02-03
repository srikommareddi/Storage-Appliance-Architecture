package powerscale

import (
	"fmt"
	"time"

	"github.com/klauspost/reedsolomon"
	"github.com/srilakshmi/storage/dpu"
)

// DPUEncoder abstracts erasure coding offload implementations.
type DPUEncoder interface {
	Name() string
	Encode(data []byte, protection ProtectionLevel) ([][]byte, error)
}

// DPUOffloadConfig controls the local offload emulator.
type DPUOffloadConfig struct {
	SimulatedLatency time.Duration
	MaxConcurrency   int
}

// LocalDPUEncoder simulates DPU offload using a bounded worker pool.
type LocalDPUEncoder struct {
	latency time.Duration
	sem     chan struct{}
}

// NewLocalDPUEncoder creates a local DPU offload emulator.
func NewLocalDPUEncoder(cfg DPUOffloadConfig) *LocalDPUEncoder {
	maxConc := cfg.MaxConcurrency
	if maxConc <= 0 {
		maxConc = 4
	}
	return &LocalDPUEncoder{
		latency: cfg.SimulatedLatency,
		sem:     make(chan struct{}, maxConc),
	}
}

func (e *LocalDPUEncoder) Name() string {
	return "local-dpu-emulator"
}

func (e *LocalDPUEncoder) Encode(data []byte, protection ProtectionLevel) ([][]byte, error) {
	if protection.DataStripes <= 0 || protection.ParityStripes <= 0 {
		return nil, fmt.Errorf("invalid protection level")
	}

	e.sem <- struct{}{}
	defer func() { <-e.sem }()

	if e.latency > 0 {
		time.Sleep(e.latency)
	}

	totalShards := protection.DataStripes + protection.ParityStripes
	enc, err := reedsolomon.New(protection.DataStripes, protection.ParityStripes)
	if err != nil {
		return nil, err
	}

	shardSize := (len(data) + protection.DataStripes - 1) / protection.DataStripes
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
	for i := protection.DataStripes; i < totalShards; i++ {
		shards[i] = make([]byte, shardSize)
	}

	if err := enc.Encode(shards); err != nil {
		return nil, err
	}
	return shards, nil
}

// EnableDPUOffload attaches the local DPU offload emulator.
func (ofs *OneFS) EnableDPUOffload(cfg DPUOffloadConfig) {
	ofs.dpuMu.Lock()
	defer ofs.dpuMu.Unlock()
	ofs.dpuEncoder = NewLocalDPUEncoder(cfg)
}

// SetDPUEncoder installs a custom DPU encoder.
func (ofs *OneFS) SetDPUEncoder(encoder DPUEncoder) {
	ofs.dpuMu.Lock()
	defer ofs.dpuMu.Unlock()
	ofs.dpuEncoder = encoder
}

// AttachDPUClient sets a DPU client for offload orchestration.
func (ofs *OneFS) AttachDPUClient(client *dpu.Client) {
	ofs.dpuMu.Lock()
	defer ofs.dpuMu.Unlock()
	ofs.dpuClient = client
}

// DPUClient returns the attached DPU client, if any.
func (ofs *OneFS) DPUClient() *dpu.Client {
	ofs.dpuMu.RLock()
	defer ofs.dpuMu.RUnlock()
	return ofs.dpuClient
}

// DPUEncoderName reports the active encoder name, if any.
func (ofs *OneFS) DPUEncoderName() string {
	ofs.dpuMu.RLock()
	defer ofs.dpuMu.RUnlock()
	if ofs.dpuEncoder == nil {
		return ""
	}
	return ofs.dpuEncoder.Name()
}
