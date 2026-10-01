package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/platform/config"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/baresipmedia"
)

var (
	errBaresipRestartFailed = errors.New("Baresip account update could not be applied")
	errBaresipCallActive    = errors.New("Baresip account update rejected while a call is active")
)

type baresipRuntime struct {
	mu      sync.Mutex
	ctx     context.Context
	cfg     config.Config
	rxPath  string
	txPath  string
	profile *baresipmedia.Profile
	process *baresipmedia.Process
	active  func() bool
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

func (b *baresipRuntime) SetActiveCallCheck(check func() bool) {
	b.mu.Lock()
	b.active = check
	b.mu.Unlock()
}

func (b *baresipRuntime) ApplyFalePacoPassword(ctx context.Context, password string) error {
	account, err := baresipmedia.RenderFalePacoAccount(password)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active != nil && b.active() {
		return errBaresipCallActive
	}
	if ctx == nil {
		ctx = context.Background()
	}
	accountPath := filepath.Join(b.cfg.BaresipProfileDir, "accounts")
	previousAccount, previousReadErr := os.ReadFile(accountPath)
	previousExists := previousReadErr == nil
	if previousReadErr != nil && !errors.Is(previousReadErr, os.ErrNotExist) {
		return errBaresipRestartFailed
	}
	if err := baresipmedia.WritePrivateAccount(accountPath, account); err != nil {
		return errBaresipRestartFailed
	}
	nextProfile, err := baresipmedia.PrepareProfileWithAccount(b.cfg.BaresipProfileDir, b.cfg.BaresipMediaModulePath, b.cfg.BaresipSystemModuleDir, b.rxPath, b.txPath, b.cfg.BaresipCtrlTCPAddress, account)
	if err != nil {
		b.restoreAccount(accountPath, previousAccount, previousExists)
		return errBaresipRestartFailed
	}
	oldProfile, oldProcess := b.profile, b.process
	if oldProcess != nil {
		_ = oldProcess.Close()
	}
	nextProcess, startErr := baresipmedia.StartProcess(b.ctx, b.cfg.BaresipBinaryPath, nextProfile.Directory)
	if startErr == nil {
		startErr = waitForBaresipControl(ctx, b.cfg.BaresipCtrlTCPAddress, 10*time.Second)
	}
	if startErr != nil {
		if nextProcess != nil {
			_ = nextProcess.Close()
		}
		_ = nextProfile.Close()
		b.restoreAccount(accountPath, previousAccount, previousExists)
		restored, restoreErr := baresipmedia.StartProcess(b.ctx, b.cfg.BaresipBinaryPath, oldProfile.Directory)
		if restoreErr == nil {
			restoreErr = waitForBaresipControl(b.ctx, b.cfg.BaresipCtrlTCPAddress, 10*time.Second)
		}
		if restoreErr == nil {
			b.process = restored
			b.profile = oldProfile
		} else {
			if restored != nil {
				_ = restored.Close()
			}
			b.process = nil
			// Keep the old profile reachable so Close can remove its private
			// temporary directory even when the rollback process cannot start.
			b.profile = oldProfile
		}
		return errBaresipRestartFailed
	}
	b.process = nextProcess
	b.profile = nextProfile
	if oldProfile != nil {
		_ = oldProfile.Close()
	}
	return nil
}

func (b *baresipRuntime) restoreAccount(path string, account []byte, existed bool) {
	if existed {
		_ = baresipmedia.WritePrivateAccount(path, account)
		return
	}
	_ = os.Remove(path)
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
