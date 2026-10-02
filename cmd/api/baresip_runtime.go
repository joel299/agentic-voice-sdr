package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/platform/config"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/baresipmedia"
)

type baresipRuntime struct {
	mu      sync.Mutex
	ctx     context.Context
	cfg     config.Config
	rxPath  string
	txPath  string
	profile *baresipmedia.Profile
	process *baresipmedia.Process
}

func startBaresipRuntime(ctx context.Context, cfg config.Config, rxPath, txPath string) (*baresipRuntime, error) {
	profile, err := baresipmedia.PrepareProfile(cfg.BaresipProfileDir, cfg.BaresipMediaModulePath, cfg.BaresipSystemModuleDir, rxPath, txPath, cfg.BaresipCtrlTCPAddress)
	if err != nil {
		return nil, errors.New("prepare private Baresip profile")
	}
	process, err := baresipmedia.StartProcess(ctx, cfg.BaresipBinaryPath, profile.Directory)
	if err == nil {
		err = waitForBaresipControl(ctx, cfg.BaresipCtrlTCPAddress, 10*time.Second)
	}
	if err != nil {
		if process != nil {
			_ = process.Close()
		}
		_ = profile.Close()
		return nil, err
	}
	return &baresipRuntime{ctx: ctx, cfg: cfg, rxPath: rxPath, txPath: txPath, profile: profile, process: process}, nil
}

func (b *baresipRuntime) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var err error
	if b.process != nil {
		err = b.process.Close()
		b.process = nil
	}
	if b.profile != nil {
		if closeErr := b.profile.Close(); err == nil {
			err = closeErr
		}
		b.profile = nil
	}
	return err
}
