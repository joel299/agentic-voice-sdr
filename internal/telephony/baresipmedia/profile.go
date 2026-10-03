package baresipmedia

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrInvalidBaresipProfile = errors.New("baresip media profile: invalid configuration")

// Profile is a private, generated Baresip profile. Its accounts file is a
// permission-restricted copy of the owner's source profile and is deleted by
// Close; the source profile is never edited.
type Profile struct {
	Directory string
	Modules   string
}

func (p *Profile) Close() error {
	if p == nil || p.Directory == "" {
		return nil
	}
	err := os.RemoveAll(p.Directory)
	p.Directory = ""
	p.Modules = ""
	return err
}

// PrepareProfile copies only Baresip's config and accounts, then injects the
// adapter's generated sockets into a temporary profile before Baresip starts.
func PrepareProfile(sourceDir, mediaModule, systemModuleDir, rxPath, txPath, ctrlAddress string) (*Profile, error) {
	return PrepareProfileWithAccount(sourceDir, mediaModule, systemModuleDir, rxPath, txPath, ctrlAddress, nil)
}

// PrepareProfileWithAccount creates the private runtime profile and optionally
// replaces its copied account with an explicit protected account record.
func PrepareProfileWithAccount(sourceDir, mediaModule, systemModuleDir, rxPath, txPath, ctrlAddress string, account []byte) (*Profile, error) {
	if strings.TrimSpace(sourceDir) == "" || strings.TrimSpace(mediaModule) == "" || strings.TrimSpace(systemModuleDir) == "" || strings.TrimSpace(rxPath) == "" || strings.TrimSpace(txPath) == "" || ctrlAddress != "127.0.0.1:4444" {
		return nil, ErrInvalidBaresipProfile
	}
	config, err := os.ReadFile(filepath.Join(sourceDir, "config"))
	if err != nil {
		return nil, fmt.Errorf("read source Baresip config: %w", err)
	}
	if info, err := os.Stat(mediaModule); err != nil || !info.Mode().IsRegular() {
		return nil, ErrInvalidBaresipProfile
	}
	modules, err := os.ReadDir(systemModuleDir)
	if err != nil {
		return nil, fmt.Errorf("read Baresip module directory: %w", err)
	}
	dir, err := os.MkdirTemp("", "gru152-baresip-")
	if err != nil {
		return nil, err
	}
	profile := &Profile{Directory: dir, Modules: filepath.Join(dir, "modules")}
	cleanup := func(cause error) (*Profile, error) {
		_ = profile.Close()
		return nil, cause
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return cleanup(err)
	}
	if err := os.Mkdir(profile.Modules, 0700); err != nil {
		return cleanup(err)
	}
	for _, entry := range modules {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".so") {
			continue
		}
		source := filepath.Join(systemModuleDir, entry.Name())
		if err := os.Symlink(source, filepath.Join(profile.Modules, entry.Name())); err != nil {
			return cleanup(err)
		}
	}
	if err := copyPrivateFile(mediaModule, filepath.Join(profile.Modules, "gru151_media.so")); err != nil {
		return cleanup(err)
	}
	generated := configureAudioProfile(string(config), profile.Modules, rxPath, txPath, ctrlAddress)
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(generated), 0600); err != nil {
		return cleanup(err)
	}
	accountsPath := filepath.Join(sourceDir, "accounts")
	if len(account) > 0 {
		if err := copyPrivateFileFromBytes(account, filepath.Join(dir, "accounts")); err != nil {
			return cleanup(err)
		}
	} else if _, err := os.Stat(accountsPath); err == nil {
		if err := copyPrivateFile(accountsPath, filepath.Join(dir, "accounts")); err != nil {
			return cleanup(err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return cleanup(err)
	}
	return profile, nil
}

func copyPrivateFileFromBytes(data []byte, destination string) error {
	if len(data) == 0 {
		return ErrInvalidBaresipProfile
	}
	if err := os.WriteFile(destination, data, 0600); err != nil {
		return err
	}
	return os.Chmod(destination, 0600)
}

func copyPrivateFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.WriteFile(destination, data, 0600); err != nil {
		return err
	}
	return os.Chmod(destination, 0600)
}

func configureAudioProfile(source, moduleDir, rxPath, txPath, ctrlAddress string) string {
	replacements := map[string]string{
		"module_path":     moduleDir,
		"audio_player":    "gru151_media," + rxPath,
		"audio_source":    "gru151_media," + txPath,
		"auplay_srate":    "16000",
		"ausrc_srate":     "24000",
		"auplay_channels": "1",
		"ausrc_channels":  "1",
		"auplay_format":   "s16",
		"ausrc_format":    "s16",
		"ctrl_tcp_listen": ctrlAddress,
	}
	seen := make(map[string]bool, len(replacements))
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			lines = append(lines, line)
			continue
		}
		fields := strings.Fields(trimmed)
		key := fields[0]
		if replacement, ok := replacements[key]; ok {
			if !seen[key] {
				lines = append(lines, key+"\t"+replacement)
				seen[key] = true
			}
			continue
		}
		lines = append(lines, line)
	}
	for key, value := range replacements {
		if !seen[key] {
			lines = append(lines, key+"\t"+value)
		}
	}
	for _, module := range []string{"gru151_media.so", "ctrl_tcp.so"} {
		if !hasModuleLine(lines, module) {
			lines = append(lines, "module\t"+module)
		}
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

func hasModuleLine(lines []string, name string) bool {
	for _, line := range lines {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && fields[0] == "module" && fields[1] == name {
			return true
		}
	}
	return false
}
