package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var ErrNoStoredMetadata = errors.New("whatsapp metadata not found")

type ConfigMetadata struct {
	Provider            string    `json:"provider"`
	BaseURL             string    `json:"base_url"`
	ActiveInstanceID    string    `json:"active_instance_id,omitempty"`
	ActiveInstancePhone string    `json:"active_instance_phone,omitempty"`
	ProviderStatus      string    `json:"provider_status,omitempty"`
	LastVerifiedAt      time.Time `json:"last_verified_at,omitempty"`
}

type ConfigStore interface {
	Save(context.Context, ConfigMetadata) error
	Load(context.Context) (ConfigMetadata, error)
}

type MemoryConfigStore struct {
	mu       sync.RWMutex
	metadata ConfigMetadata
	set      bool
}

func NewMemoryConfigStore() *MemoryConfigStore { return &MemoryConfigStore{} }
func (s *MemoryConfigStore) Save(_ context.Context, metadata ConfigMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metadata = metadata
	s.set = true
	return nil
}
func (s *MemoryConfigStore) Load(_ context.Context) (ConfigMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.set {
		return ConfigMetadata{}, ErrNoStoredMetadata
	}
	return s.metadata, nil
}

type FileConfigStore struct {
	Path string
	mu   sync.Mutex
}

func (s *FileConfigStore) Save(_ context.Context, metadata ConfigMetadata) error {
	if s == nil || s.Path == "" {
		return errors.New("config store path is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".whatsapp-config-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, s.Path)
}
func (s *FileConfigStore) Load(_ context.Context) (ConfigMetadata, error) {
	if s == nil || s.Path == "" {
		return ConfigMetadata{}, errors.New("config store path is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return ConfigMetadata{}, ErrNoStoredMetadata
	}
	if err != nil {
		return ConfigMetadata{}, err
	}
	var metadata ConfigMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return ConfigMetadata{}, errors.New("stored whatsapp metadata is invalid")
	}
	return metadata, nil
}
