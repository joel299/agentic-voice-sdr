package sip

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// NetworkDialer abstracts network dialing for deterministic testing.
type NetworkDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	DialTLSContext(ctx context.Context, network, address string) (net.Conn, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// TLSIdentityDialer optionally separates the pinned network address from the
// logical TLS service name used for certificate/SNI verification.
type TLSIdentityDialer interface {
	DialTLSContextWithServerName(ctx context.Context, network, address, serverName string) (net.Conn, error)
}

// DefaultNetworkDialer implements NetworkDialer using standard net and crypto/tls packages.
type DefaultNetworkDialer struct{}

func (d DefaultNetworkDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	return dialer.DialContext(ctx, network, address)
}

func (d DefaultNetworkDialer) DialTLSContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	tlsConfig := &tls.Config{InsecureSkipVerify: false}
	return tls.DialWithDialer(&dialer, network, address, tlsConfig)
}

func (d DefaultNetworkDialer) LookupHost(ctx context.Context, host string) ([]string, error) {
	var r net.Resolver
	return r.LookupHost(ctx, host)
}

// CommandRunner abstracts shell command execution for Asterisk CLI interaction.
type CommandRunner interface {
	RunCommand(ctx context.Context, name string, args ...string) (string, error)
}

// OSCommandRunner executes commands via os/exec.
type OSCommandRunner struct{}

func (r OSCommandRunner) RunCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// AsteriskReloader abstracts staging, verification, commit, and rollback for Asterisk PJSIP integration.
type AsteriskReloader interface {
	StagePJSIPConfig(ctx context.Context, trunkName string, pjsipConf string) error
	CommitPJSIPConfig(ctx context.Context, trunkName string) error
	RollbackPJSIPConfig(ctx context.Context, trunkName string) error
	RemovePJSIPConfig(ctx context.Context, trunkName string) error
	CheckAsteriskHealth(ctx context.Context) (bool, error)
	CheckTransport(ctx context.Context, transport TransportType) (bool, error)
	CheckEndpoint(ctx context.Context, trunkName string) (bool, error)
	CheckRegistration(ctx context.Context, trunkName string) (string, bool, error)
}

// ChownFunc abstracts file ownership setting for testability.
type ChownFunc func(name string, uid, gid int) error

// GroupLookupFunc abstracts system group lookup for testability.
type GroupLookupFunc func(name string) (*user.Group, error)

// DirStatFunc abstracts directory stat querying for testability.
type DirStatFunc func(path string) (os.FileInfo, error)

// RemoveFunc abstracts file removal for deterministic cleanup failure tests.
type RemoveFunc func(name string) error

type WriteFileFunc func(name string, data []byte, perm os.FileMode) error
type RenameFunc func(oldPath, newPath string) error

// RealAsteriskReloader is the operational concrete implementation for Asterisk PJSIP integration.
type RealAsteriskReloader struct {
	configDir     string
	runner        CommandRunner
	chownFn       ChownFunc
	groupLookupFn GroupLookupFunc
	dirStatFn     DirStatFunc
	removeFn      RemoveFunc
	writeFileFn   WriteFileFunc
	renameFn      RenameFunc
}

// NewRealAsteriskReloader creates an operational RealAsteriskReloader instance.
func NewRealAsteriskReloader(configDir string, runner CommandRunner) *RealAsteriskReloader {
	if configDir == "" {
		configDir = "/etc/asterisk/pjsip.d"
	}
	if runner == nil {
		runner = OSCommandRunner{}
	}
	return &RealAsteriskReloader{
		configDir:     configDir,
		runner:        runner,
		chownFn:       os.Chown,
		groupLookupFn: user.LookupGroup,
		dirStatFn:     os.Stat,
		removeFn:      os.Remove,
	}
}

// SetGroupLookupFunc overrides the default user.LookupGroup function for testing group availability.
func (r *RealAsteriskReloader) SetGroupLookupFunc(fn GroupLookupFunc) {
	if fn == nil {
		fn = user.LookupGroup
	}
	r.groupLookupFn = fn
}

// SetDirStatFunc overrides the default os.Stat function for testing directory stat resolution.
func (r *RealAsteriskReloader) SetDirStatFunc(fn DirStatFunc) {
	if fn == nil {
		fn = os.Stat
	}
	r.dirStatFn = fn
}

// SetRemoveFunc overrides file removal for deterministic cleanup tests.
func (r *RealAsteriskReloader) SetRemoveFunc(fn RemoveFunc) {
	if fn == nil {
		fn = os.Remove
	}
	r.removeFn = fn
}

// SetChownFunc overrides the default os.Chown function for testing or custom security policy verification.
func (r *RealAsteriskReloader) SetChownFunc(fn ChownFunc) {
	if fn == nil {
		fn = os.Chown
	}
	r.chownFn = fn
}

func (r *RealAsteriskReloader) getConfigPath(trunkName string) (string, error) {
	if trunkName == "" || strings.Contains(trunkName, "/") || strings.Contains(trunkName, "\\") || strings.Contains(trunkName, "..") {
		return "", fmt.Errorf("invalid or malicious trunk name %q", trunkName)
	}
	cleanDir := filepath.Clean(r.configDir)
	targetPath := filepath.Clean(filepath.Join(cleanDir, fmt.Sprintf("%s.conf", trunkName)))
	rel, err := filepath.Rel(cleanDir, targetPath)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return "", fmt.Errorf("security violation: path traversal detected for trunk %q", trunkName)
	}
	return targetPath, nil
}

const resolverMarker = "; managed by agentic-voice-sdr GRU-83; do not edit"

type resolverSnapshot struct {
	hostsPath, resolverPath     string
	hosts, resolver             []byte
	hostsExists, resolverExists bool
}

func (r *RealAsteriskReloader) resolverDir() string {
	dir := filepath.Clean(r.configDir)
	if filepath.Base(dir) == "pjsip.d" {
		return filepath.Dir(dir)
	}
	return dir
}

func (r *RealAsteriskReloader) captureResolver() (resolverSnapshot, error) {
	dir := r.resolverDir()
	s := resolverSnapshot{hostsPath: filepath.Join(dir, ".gru83-pinned.hosts"), resolverPath: filepath.Join(dir, "resolver_unbound.conf")}
	for path, dst := range map[string]*[]byte{s.hostsPath: &s.hosts, s.resolverPath: &s.resolver} {
		data, err := os.ReadFile(path)
		if err == nil {
			*dst = data
			if path == s.hostsPath {
				s.hostsExists = true
			} else {
				s.resolverExists = true
			}
		} else if !os.IsNotExist(err) {
			return s, fmt.Errorf("failed to read resolver state %s: %w", path, err)
		}
	}
	if s.resolverExists && !strings.Contains(string(s.resolver), resolverMarker) {
		return s, fmt.Errorf("refusing to overwrite unmanaged resolver configuration %s", s.resolverPath)
	}
	if s.hostsExists && !strings.Contains(string(s.hosts), resolverMarker) {
		return s, fmt.Errorf("refusing to overwrite unmanaged pinned hosts file %s", s.hostsPath)
	}
	return s, nil
}

func (r *RealAsteriskReloader) writeResolverFile(path string, data []byte, perm os.FileMode) error {
	if r.writeFileFn != nil {
		return r.writeFileFn(path, data, perm)
	}
	return os.WriteFile(path, data, perm)
}

func (r *RealAsteriskReloader) renameResolverFile(oldPath, newPath string) error {
	if r.renameFn != nil {
		return r.renameFn(oldPath, newPath)
	}
	return os.Rename(oldPath, newPath)
}

func (r *RealAsteriskReloader) syncPinnedResolver() (resolverSnapshot, error) {
	s, err := r.captureResolver()
	if err != nil {
		return s, err
	}
	dir := r.resolverDir()
	entries, err := filepath.Glob(filepath.Join(r.configDir, "*.conf"))
	if err != nil {
		return s, fmt.Errorf("failed to enumerate PJSIP configs for resolver sync: %w", err)
	}
	byHost := make(map[string]string)
	for _, path := range entries {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return s, fmt.Errorf("failed to read PJSIP config %s for resolver sync: %w", path, readErr)
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 4 || fields[0] != ";" || fields[1] != "gru83-pin" {
				continue
			}
			var host, address string
			for _, field := range fields[2:] {
				key, value, ok := strings.Cut(field, "=")
				if !ok || value == "" {
					continue
				}
				switch key {
				case "host":
					host = value
				case "address":
					address = value
				}
			}
			if host == "" || address == "" {
				continue
			}
			if hostPart, _, splitErr := net.SplitHostPort(address); splitErr == nil {
				address = hostPart
			} else if strings.Count(address, ":") == 1 {
				address = strings.SplitN(address, ":", 2)[0]
			}
			byHost[host] = address
		}
	}
	hosts := make([]string, 0, len(byHost))
	for host, address := range byHost {
		hosts = append(hosts, address+" "+host)
	}
	sort.Strings(hosts)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return s, fmt.Errorf("failed to create resolver directory %s: %w", dir, err)
	}
	hostsContent := resolverMarker + "\n" + strings.Join(hosts, "\n") + "\n"
	resolverContent := string(s.resolver)
	if resolverContent == "" {
		resolverContent = resolverMarker + "\n[general]\nresolv = system\n"
	}
	lines := strings.Split(strings.TrimRight(resolverContent, "\n"), "\n")
	foundHosts := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "hosts =") {
			lines[i] = "hosts = " + s.hostsPath
			foundHosts = true
		}
	}
	if !foundHosts {
		lines = append(lines, "hosts = "+s.hostsPath)
	}
	resolverContent = strings.Join(lines, "\n") + "\n"
	newState := []struct{ path, content string }{{s.hostsPath, hostsContent}, {s.resolverPath, resolverContent}}
	// Stage both files before touching either live path, then install with rollback.
	for _, item := range newState {
		path, content := item.path, item.content
		tmp := path + ".tmp"
		if err := r.writeResolverFile(tmp, []byte(content), 0640); err != nil {
			return s, fmt.Errorf("failed to write managed resolver file %s: %w", path, err)
		}
		if err := r.applySecureFilePermissions(tmp); err != nil {
			_ = os.Remove(tmp)
			return s, fmt.Errorf("failed to secure managed resolver file %s: %w", path, err)
		}
	}
	for _, item := range newState {
		if err := r.renameResolverFile(item.path+".tmp", item.path); err != nil {
			for _, pending := range newState {
				_ = os.Remove(pending.path + ".tmp")
			}
			restoreErr := r.restoreResolver(s)
			return s, compoundRollback(fmt.Errorf("failed to install managed resolver file %s: %w", item.path, err), restoreErr)
		}
	}
	return s, nil
}

func (r *RealAsteriskReloader) restoreResolver(s resolverSnapshot) error {
	for path, data := range map[string][]byte{s.hostsPath: s.hosts, s.resolverPath: s.resolver} {
		exists := (path == s.hostsPath && s.hostsExists) || (path == s.resolverPath && s.resolverExists)
		if exists {
			if err := r.writeResolverFile(path, data, 0640); err != nil {
				return err
			}
			if err := r.applySecureFilePermissions(path); err != nil {
				return err
			}
		} else if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (r *RealAsteriskReloader) reloadResolver(ctx context.Context) error {
	if _, err := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_resolver_unbound.so"); err != nil {
		return err
	}
	return nil
}

func compoundRollback(primary error, failures ...error) error {
	all := []error{primary}
	for _, failure := range failures {
		if failure != nil {
			all = append(all, failure)
		}
	}
	return errors.Join(all...)
}

// applySecureFilePermissions enforces 0640 (-rw-r-----) permissions, resolves valid Asterisk owner/group, and fails closed.
func (r *RealAsteriskReloader) applySecureFilePermissions(filePath string) error {
	// 1. Enforce 0640 permissions (-rw-r-----)
	if err := os.Chmod(filePath, 0640); err != nil {
		return fmt.Errorf("failed to set secure 0640 permissions on %s: %w", filePath, err)
	}

	targetUid := -1
	targetGid := -1

	// 2. Try deriving UID/GID from configDir or parent directory
	dirStatFunc := r.dirStatFn
	if dirStatFunc == nil {
		dirStatFunc = os.Stat
	}

	if r.configDir != "" {
		if info, err := dirStatFunc(r.configDir); err == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				if stat.Uid != 0 {
					targetUid = int(stat.Uid)
				}
				if stat.Gid != 0 {
					targetGid = int(stat.Gid)
				}
			}
		}
		// If configDir gave GID 0 (root), check parent directory (e.g. /etc/asterisk)
		if targetGid <= 0 {
			parent := filepath.Dir(r.configDir)
			if parent != "" && parent != "/" {
				if info, err := dirStatFunc(parent); err == nil {
					if stat, ok := info.Sys().(*syscall.Stat_t); ok {
						if stat.Gid != 0 {
							targetGid = int(stat.Gid)
						}
					}
				}
			}
		}
	}

	// 3. If GID is still root or unassigned, lookup "asterisk" group explicitly
	if targetGid <= 0 {
		groupLookup := r.groupLookupFn
		if groupLookup == nil {
			groupLookup = user.LookupGroup
		}
		if g, err := groupLookup("asterisk"); err == nil {
			if gid, err := strconv.Atoi(g.Gid); err == nil {
				targetGid = gid
			}
		}
	}

	// 4. Fail closed if no valid target GID ownership policy could be determined (targetGid == -1 forbidden)
	if targetGid <= 0 {
		return fmt.Errorf("security policy failure: unable to resolve valid Asterisk GID ownership policy for %s (no non-root directory group ownership and 'asterisk' group unavailable)", filePath)
	}

	// 5. Apply chown with resolved policy. Fail closed on any error (no silent EPERM bypass)
	chownFunc := r.chownFn
	if chownFunc == nil {
		chownFunc = os.Chown
	}
	if err := chownFunc(filePath, targetUid, targetGid); err != nil {
		return fmt.Errorf("failed to apply chown (%d:%d) on %s: %w", targetUid, targetGid, filePath, err)
	}

	// 5. Stat verification: confirm file permissions and target ownership strictly match policy
	statInfo, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("failed to verify permissions on %s: %w", filePath, err)
	}

	mode := statInfo.Mode().Perm()
	if mode&0007 != 0 {
		return fmt.Errorf("security violation: file %s is world-accessible (%04o)", filePath, mode)
	}
	if mode&0020 != 0 {
		return fmt.Errorf("security violation: file %s is group-writable (%04o)", filePath, mode)
	}

	if stat, ok := statInfo.Sys().(*syscall.Stat_t); ok {
		if targetUid > 0 && int(stat.Uid) != targetUid {
			return fmt.Errorf("security policy failure: file %s UID %d does not match required target UID %d", filePath, stat.Uid, targetUid)
		}
		if targetGid > 0 && int(stat.Gid) != targetGid {
			return fmt.Errorf("security policy failure: file %s GID %d does not match required target GID %d", filePath, stat.Gid, targetGid)
		}
	}

	return nil
}

// copyFile performs a secure file copy with 0640 permissions and secure ownership.
func (r *RealAsteriskReloader) copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}

	return r.applySecureFilePermissions(dst)
}

// StagePJSIPConfig stages a new PJSIP config file, preserves any existing config in .bak, and reloads Asterisk.
func (r *RealAsteriskReloader) StagePJSIPConfig(ctx context.Context, trunkName string, pjsipConf string) error {
	targetPath, err := r.getConfigPath(trunkName)
	if err != nil {
		return err
	}

	tmpPath := targetPath + ".tmp"
	bakPath := targetPath + ".bak"

	// Step 1: Write temp file with 0640 permissions and enforce secure filesystem security policy
	if err := os.WriteFile(tmpPath, []byte(pjsipConf), 0640); err != nil {
		return fmt.Errorf("failed to write temp PJSIP config file %s: %w", tmpPath, err)
	}
	if err := r.applySecureFilePermissions(tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("security policy failure on temp file %s: %w", tmpPath, err)
	}

	// Step 2: Preserve existing config if present
	hasPrev := false
	if _, err := os.Stat(targetPath); err == nil {
		if err := r.copyFile(targetPath, bakPath); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("failed to backup existing PJSIP config file: %w", err)
		}
		hasPrev = true
	}

	// Step 3: Atomic rename and enforce secure filesystem policy on target file BEFORE reload
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		if hasPrev {
			_ = os.Remove(bakPath)
		}
		return fmt.Errorf("failed to atomically apply PJSIP config file: %w", err)
	}
	if err := r.applySecureFilePermissions(targetPath); err != nil {
		if hasPrev {
			rbErr := os.Rename(bakPath, targetPath)
			secErr := r.applySecureFilePermissions(targetPath)
			if rbErr != nil || secErr != nil {
				return fmt.Errorf("security policy failure on target PJSIP config file %s (%w); rollback failed (restoreErr: %v, secErr: %v)", targetPath, err, rbErr, secErr)
			}
		} else {
			_ = os.Remove(targetPath)
		}
		return fmt.Errorf("security policy failure on target PJSIP config file %s: %w", targetPath, err)
	}

	resolverPrev, err := r.syncPinnedResolver()
	if err != nil {
		if hasPrev {
			_ = os.Rename(bakPath, targetPath)
		} else {
			_ = os.Remove(targetPath)
		}
		return fmt.Errorf("failed to synchronize pinned resolver before Asterisk reload: %w", err)
	}
	if err := r.reloadResolver(ctx); err != nil {
		resolverRestoreErr := r.restoreResolver(resolverPrev)
		var pjsipRestoreErr error
		if hasPrev {
			pjsipRestoreErr = os.Rename(bakPath, targetPath)
		} else {
			pjsipRestoreErr = os.Remove(targetPath)
		}
		return compoundRollback(fmt.Errorf("PRIMARY FAILURE: resolver reload failed: %w", err), fmt.Errorf("ROLLBACK FAILURE: resolver restore=%v pjsip restore=%v", resolverRestoreErr, pjsipRestoreErr))
	}

	// Step 5: Reload Asterisk PJSIP module
	out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
	if err != nil {
		resolverRestoreErr := r.restoreResolver(resolverPrev)
		resolverReloadErr := r.reloadResolver(ctx)
		if hasPrev {
			rbErr := os.Rename(bakPath, targetPath)
			var secErr error
			if rbErr == nil {
				secErr = r.applySecureFilePermissions(targetPath)
			}
			_, reloadErr := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
			if rbErr != nil || secErr != nil || resolverRestoreErr != nil || resolverReloadErr != nil || reloadErr != nil {
				return compoundRollback(fmt.Errorf("PRIMARY FAILURE: asterisk reload failed during trunk stage: %w (output: %s)", err, out), fmt.Errorf("ROLLBACK FAILURE: pjsip restore=%v resolver restore=%v resolver reload=%v pjsip reload=%v", rbErr, resolverRestoreErr, resolverReloadErr, reloadErr), secErr)
			}
		} else {
			removeErr := os.Remove(targetPath)
			_, rollbackReloadErr := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
			if removeErr != nil || resolverRestoreErr != nil || resolverReloadErr != nil || rollbackReloadErr != nil {
				return compoundRollback(fmt.Errorf("PRIMARY FAILURE: asterisk reload failed during trunk stage: %w (output: %s)", err, out), fmt.Errorf("ROLLBACK FAILURE: rollback failed: pjsip remove=%v resolver restore=%v resolver reload=%v pjsip reload=%v", removeErr, resolverRestoreErr, resolverReloadErr, rollbackReloadErr))
			}
		}
		return fmt.Errorf("PRIMARY FAILURE: asterisk reload failed during trunk stage: %w, output: %s", err, out)
	}

	return nil
}

// CommitPJSIPConfig finalizes a staged transaction by removing the backup file.
func (r *RealAsteriskReloader) CommitPJSIPConfig(ctx context.Context, trunkName string) error {
	targetPath, err := r.getConfigPath(trunkName)
	if err != nil {
		return err
	}
	bakPath := targetPath + ".bak"
	if _, err := os.Stat(bakPath); err == nil {
		if err := os.Remove(bakPath); err != nil {
			return fmt.Errorf("failed to remove backup config file during commit: %w", err)
		}
	}
	return nil
}

// RollbackPJSIPConfig restores previous config file from backup or removes staged file on transaction failure.
func (r *RealAsteriskReloader) RollbackPJSIPConfig(ctx context.Context, trunkName string) error {
	targetPath, err := r.getConfigPath(trunkName)
	if err != nil {
		return err
	}
	bakPath := targetPath + ".bak"

	var restoreErr error
	var secErr error
	if _, err := os.Stat(bakPath); err == nil {
		// Restore previous config
		restoreErr = os.Rename(bakPath, targetPath)
		if restoreErr == nil {
			secErr = r.applySecureFilePermissions(targetPath)
		}
	} else {
		// No previous config: remove targetPath
		if _, err := os.Stat(targetPath); err == nil {
			restoreErr = os.Remove(targetPath)
		}
	}

	resolverErr := error(nil)
	if _, syncErr := r.syncPinnedResolver(); syncErr != nil {
		resolverErr = syncErr
	} else {
		resolverErr = r.reloadResolver(ctx)
	}
	// Reload Asterisk to apply restored or removed state
	out, reloadErr := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")

	if restoreErr != nil || secErr != nil || resolverErr != nil || reloadErr != nil {
		return fmt.Errorf("rollback failed for trunk %s (restoreErr: %v, secErr: %v, resolverErr: %v, reloadErr: %v, output: %s)", trunkName, restoreErr, secErr, resolverErr, reloadErr, out)
	}

	return nil
}

// RemovePJSIPConfig stages removal of a PJSIP config file, preserves backup, and reloads Asterisk.
func (r *RealAsteriskReloader) RemovePJSIPConfig(ctx context.Context, trunkName string) error {
	targetPath, err := r.getConfigPath(trunkName)
	if err != nil {
		return err
	}

	bakPath := targetPath + ".bak"
	hasPrev := false

	if _, err := os.Stat(targetPath); err == nil {
		if err := r.copyFile(targetPath, bakPath); err != nil {
			return fmt.Errorf("failed to backup config prior to removal: %w", err)
		}
		hasPrev = true
		if err := os.Remove(targetPath); err != nil {
			_ = os.Remove(bakPath)
			return fmt.Errorf("failed to remove PJSIP config file %s: %w", targetPath, err)
		}
	}

	resolverPrev, err := r.syncPinnedResolver()
	if err != nil {
		if hasPrev {
			if restoreErr := os.Rename(bakPath, targetPath); restoreErr != nil {
				return fmt.Errorf("failed to synchronize pinned resolver before trunk removal and rollback failed: %w (restore: %v)", err, restoreErr)
			}
		}
		return fmt.Errorf("failed to synchronize pinned resolver before trunk removal: %w", err)
	}
	if err := r.reloadResolver(ctx); err != nil {
		resolverRestoreErr := r.restoreResolver(resolverPrev)
		var pjsipRestoreErr error
		if hasPrev {
			pjsipRestoreErr = os.Rename(bakPath, targetPath)
		}
		return compoundRollback(fmt.Errorf("PRIMARY FAILURE: resolver reload failed during trunk removal: %w", err), fmt.Errorf("ROLLBACK FAILURE: resolver restore=%v pjsip restore=%v", resolverRestoreErr, pjsipRestoreErr))
	}

	out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
	if err != nil {
		resolverRestoreErr := r.restoreResolver(resolverPrev)
		resolverReloadErr := r.reloadResolver(ctx)
		if hasPrev {
			rbErr := os.Rename(bakPath, targetPath)
			var secErr error
			if rbErr == nil {
				secErr = r.applySecureFilePermissions(targetPath)
			}
			_, reloadErr := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
			if rbErr != nil || secErr != nil || resolverRestoreErr != nil || resolverReloadErr != nil || reloadErr != nil {
				return compoundRollback(fmt.Errorf("PRIMARY FAILURE: asterisk reload failed during trunk removal: %w (output: %s)", err, out), fmt.Errorf("ROLLBACK FAILURE: pjsip restore=%v resolver restore=%v resolver reload=%v pjsip reload=%v", rbErr, resolverRestoreErr, resolverReloadErr, reloadErr), secErr)
			}
		}
		return fmt.Errorf("asterisk reload failed during trunk removal: %w, output: %s", err, out)
	}

	// Verify health post-removal
	healthy, healthErr := r.CheckAsteriskHealth(ctx)
	if healthErr != nil || !healthy {
		resolverRestoreErr := r.restoreResolver(resolverPrev)
		resolverReloadErr := r.reloadResolver(ctx)
		if hasPrev {
			rbErr := os.Rename(bakPath, targetPath)
			var secErr error
			if rbErr == nil {
				secErr = r.applySecureFilePermissions(targetPath)
			}
			_, reloadErr := r.runner.RunCommand(ctx, "asterisk", "-rx", "module reload res_pjsip.so")
			if rbErr != nil || secErr != nil || resolverRestoreErr != nil || resolverReloadErr != nil || reloadErr != nil {
				return compoundRollback(fmt.Errorf("PRIMARY FAILURE: Asterisk unhealthy after trunk removal: %v", healthErr), fmt.Errorf("ROLLBACK FAILURE: pjsip restore=%v resolver restore=%v resolver reload=%v pjsip reload=%v", rbErr, resolverRestoreErr, resolverReloadErr, reloadErr), secErr)
			}
		}
		return fmt.Errorf("asterisk unhealthy after trunk removal: %v", healthErr)
	}

	if hasPrev {
		if err := r.removeFn(bakPath); err != nil {
			return fmt.Errorf("failed to finalize trunk removal cleanup: %w", err)
		}
	}
	return nil
}

// CheckAsteriskHealth queries Asterisk uptime/ping via CLI.
func (r *RealAsteriskReloader) CheckAsteriskHealth(ctx context.Context) (bool, error) {
	out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", "core show uptime")
	if err != nil {
		return false, fmt.Errorf("failed to query Asterisk CLI: %w (output: %s)", err, out)
	}
	if strings.Contains(out, "System uptime") || strings.Contains(out, "Uptime:") || strings.Contains(out, "Asterisk") {
		return true, nil
	}
	return false, fmt.Errorf("unexpected Asterisk uptime output: %s", out)
}

// CheckTransport queries Asterisk CLI to verify presence of required shared transport.
func (r *RealAsteriskReloader) CheckTransport(ctx context.Context, transport TransportType) (bool, error) {
	out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", fmt.Sprintf("pjsip show transport %s", PJSIPTransportObjectName(transport)))
	if err != nil {
		return false, fmt.Errorf("failed to query Asterisk transport via CLI: %w (output: %s)", err, out)
	}
	if strings.Contains(out, "Unable to find object") || strings.Contains(out, "No objects found") || strings.Contains(out, "not found") {
		return false, nil
	}
	if strings.Contains(out, "Transport:") || strings.Contains(out, string(transport)) {
		return true, nil
	}
	return false, nil
}

// CheckEndpoint queries Asterisk CLI to verify endpoint presence.
func (r *RealAsteriskReloader) CheckEndpoint(ctx context.Context, trunkName string) (bool, error) {
	out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", fmt.Sprintf("pjsip show endpoint %s", PJSIPEndpointObjectName(trunkName)))
	if err != nil {
		return false, fmt.Errorf("failed to query Asterisk endpoint via CLI: %w (output: %s)", err, out)
	}
	if strings.Contains(out, "Unable to find object") || strings.Contains(out, "No objects found") || strings.Contains(out, "not found") {
		return false, nil
	}
	if strings.Contains(out, "Endpoint:") || strings.Contains(out, PJSIPEndpointObjectName(trunkName)) {
		return true, nil
	}
	return false, nil
}

// CheckRegistration queries Asterisk CLI to verify registration status for outbound registered trunks.
func (r *RealAsteriskReloader) CheckRegistration(ctx context.Context, trunkName string) (string, bool, error) {
	out, err := r.runner.RunCommand(ctx, "asterisk", "-rx", fmt.Sprintf("pjsip show registration %s", PJSIPRegistrationObjectName(trunkName)))
	if err != nil {
		return "Unregistered", false, fmt.Errorf("failed to query Asterisk registration via CLI: %w (output: %s)", err, out)
	}
	if strings.Contains(out, "Unable to find object") || strings.Contains(out, "No objects found") || strings.Contains(out, "not found") {
		return "Unregistered", false, nil
	}
	if strings.Contains(out, "Registered") || strings.Contains(out, "REGISTERED") {
		return "Registered", true, nil
	}
	if strings.Contains(out, "Rejected") || strings.Contains(out, "REJECTED") {
		return "Rejected", false, nil
	}
	return "Unregistered", false, nil
}

// Manager orchestrates lifecycle state transitions and verification for SIP trunks.
type Manager struct {
	mu             sync.RWMutex
	globalReloadMu sync.Mutex
	trunkLocks     map[string]*sync.Mutex
	trunks         map[string]TrunkConfig
	statuses       map[string]StatusReport
	pjsipConfigs   map[string]string
	dialer         NetworkDialer
	reloader       AsteriskReloader
}

// NewManager constructs a Manager instance with mandatory dialer and reloader.
func NewManager(dialer NetworkDialer, reloader AsteriskReloader) (*Manager, error) {
	if dialer == nil {
		return nil, fmt.Errorf("dialer is required and cannot be nil")
	}
	if reloader == nil {
		return nil, fmt.Errorf("reloader is required and cannot be nil; mock reloader is for test use only")
	}
	return &Manager{
		trunkLocks:   make(map[string]*sync.Mutex),
		trunks:       make(map[string]TrunkConfig),
		statuses:     make(map[string]StatusReport),
		pjsipConfigs: make(map[string]string),
		dialer:       dialer,
		reloader:     reloader,
	}, nil
}

// getTrunkLock returns a dedicated mutex for the specified trunk name, allocating one if needed.
func (m *Manager) getTrunkLock(trunkName string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, ok := m.trunkLocks[trunkName]
	if !ok {
		lock = &sync.Mutex{}
		m.trunkLocks[trunkName] = lock
	}
	return lock
}

// ApplyTrunk validates, reconciles, and applies a SIP trunk configuration safely inside an atomic transaction.
func (m *Manager) ApplyTrunk(ctx context.Context, cfg TrunkConfig) (StatusReport, error) {
	// Deep clone input config to prevent caller slice mutation
	cfg = cfg.Clone()

	// Acquire per-trunk mutex to serialize all transactional operations for this trunk name
	trunkLock := m.getTrunkLock(cfg.Name)
	trunkLock.Lock()
	defer trunkLock.Unlock()

	// Acquire global reload mutex to serialize Asterisk runtime reload/verification transactions
	m.globalReloadMu.Lock()
	defer m.globalReloadMu.Unlock()

	if err := cfg.Validate(); err != nil {
		report := StatusReport{
			TrunkName:       cfg.Name,
			Provider:        cfg.Provider,
			Status:          StatusAuthError,
			LastError:       fmt.Sprintf("validation failed: %v", err),
			LastValidatedAt: time.Now(),
		}
		return report, fmt.Errorf("invalid trunk configuration: %w", err)
	}

	// Handling Enabled = false (Disable transaction)
	if !cfg.Enabled {
		// Remove the named PJSIP config independently of in-memory history. This
		// is required after a process restart, when m.trunks is empty but the
		// operational file may still be active in Asterisk.
		if err := m.reloader.RemovePJSIPConfig(ctx, cfg.Name); err != nil {
			return StatusReport{}, fmt.Errorf("failed to disable trunk in Asterisk: %w", err)
		}

		report := StatusReport{
			TrunkName:         cfg.Name,
			Provider:          cfg.Provider,
			Status:            StatusDisabled,
			EndpointActive:    false,
			RegistrationState: "Disabled",
			LastValidatedAt:   time.Now(),
		}

		m.mu.Lock()
		m.trunks[cfg.Name] = cfg
		m.statuses[cfg.Name] = report
		delete(m.pjsipConfigs, cfg.Name)
		m.mu.Unlock()

		return report, nil
	}

	report := StatusReport{
		TrunkName:       cfg.Name,
		Provider:        cfg.Provider,
		Status:          StatusValidating,
		LastValidatedAt: time.Now(),
	}

	// Step 1: DNS Resolution Check
	dialHost := cfg.HostNetworkAddress
	if dialHost == "" {
		dialHost = cfg.Host
	}
	_, err := m.dialer.LookupHost(ctx, dialHost)
	if err != nil {
		report.Status = StatusDNSError
		report.LastError = fmt.Sprintf("DNS lookup failed for host %s: %v", cfg.Host, err)
		return report, fmt.Errorf("DNS failure: %w", err)
	}

	// Step 2: Connection / Reachability & TLS Check
	address := fmt.Sprintf("%s:%d", dialHost, cfg.Port)
	network := string(cfg.Transport)

	if cfg.Transport == TransportTLS {
		serverName := cfg.TLSServiceName
		if serverName == "" {
			serverName = cfg.Host
		}
		var conn net.Conn
		if identityDialer, ok := m.dialer.(TLSIdentityDialer); ok {
			conn, err = identityDialer.DialTLSContextWithServerName(ctx, "tcp", address, serverName)
		} else {
			conn, err = m.dialer.DialTLSContext(ctx, "tcp", address)
		}
		if err != nil {
			report.Status = StatusConnectionError
			report.LastError = fmt.Sprintf("TLS connection/handshake failed to %s: %v", address, err)
			return report, fmt.Errorf("TLS connection failure: %w", err)
		}
		_ = conn.Close()
	} else {
		conn, err := m.dialer.DialContext(ctx, network, address)
		if err != nil {
			report.Status = StatusConnectionError
			report.LastError = fmt.Sprintf("Connection failed to %s via %s: %v", address, network, err)
			return report, fmt.Errorf("connection failure: %w", err)
		}
		_ = conn.Close()
	}

	// Step 3: PJSIP Config Generation
	pjsipConf, err := GeneratePJSIPConfig(cfg)
	if err != nil {
		report.Status = StatusConfigured
		report.LastError = fmt.Sprintf("PJSIP generation error: %v", err)
		return report, fmt.Errorf("PJSIP generation failed: %w", err)
	}

	// Step 4: Stage PJSIP config with Asterisk
	if err := m.reloader.StagePJSIPConfig(ctx, cfg.Name, pjsipConf); err != nil {
		report.Status = StatusConfigured
		report.LastError = fmt.Sprintf("Asterisk reload error: %v", err)
		return report, fmt.Errorf("asterisk reload failed: %w", err)
	}

	// Helper function for transaction rollback on verification failure
	rollbackTransaction := func(origErr error, status LifecycleStatus) (StatusReport, error) {
		rbErr := m.reloader.RollbackPJSIPConfig(ctx, cfg.Name)
		report.Status = status
		report.EndpointActive = false
		if rbErr != nil {
			report.LastError = fmt.Sprintf("%v; rollback error: %v", origErr, rbErr)
			return report, fmt.Errorf("operation failed (%w) and rollback failed: %v", origErr, rbErr)
		}
		report.LastError = origErr.Error()
		return report, origErr
	}

	// Step 5: Check Asterisk Health
	healthy, healthErr := m.reloader.CheckAsteriskHealth(ctx)
	report.AsteriskHealthy = healthy
	if healthErr != nil || !healthy {
		if healthErr == nil {
			healthErr = fmt.Errorf("asterisk health status is unhealthy")
		}
		return rollbackTransaction(fmt.Errorf("asterisk health check failed: %w", healthErr), StatusConnectionError)
	}

	// Step 5b: Check Shared Transport Provisioned in Asterisk
	tpActive, tpErr := m.reloader.CheckTransport(ctx, cfg.Transport)
	if tpErr != nil || !tpActive {
		if tpErr == nil {
			tpErr = fmt.Errorf("shared transport transport-%s not provisioned in Asterisk", cfg.Transport)
		}
		return rollbackTransaction(fmt.Errorf("asterisk transport check failed: %w", tpErr), StatusConfigured)
	}

	// Step 6: Check Endpoint Existence in Asterisk
	epActive, epErr := m.reloader.CheckEndpoint(ctx, cfg.Name)
	report.EndpointActive = epActive
	if epErr != nil || !epActive {
		if epErr == nil {
			epErr = fmt.Errorf("endpoint trunk-%s not active in Asterisk", cfg.Name)
		}
		return rollbackTransaction(fmt.Errorf("asterisk endpoint check failed: %w", epErr), StatusConfigured)
	}

	// Step 7: Verify Registration if required
	if cfg.RegistrationRequired {
		regState, regHealthy, regErr := m.reloader.CheckRegistration(ctx, cfg.Name)
		report.RegistrationState = regState
		if regErr != nil || !regHealthy || (regState != "Registered" && regState != "REGISTERED") {
			if regErr == nil {
				regErr = fmt.Errorf("registration state %q is not Registered", regState)
			}
			return rollbackTransaction(fmt.Errorf("registration failed: %w", regErr), StatusRegistrationFailed)
		}
		report.Status = StatusReady
		report.EndpointActive = true
	} else {
		report.RegistrationState = "N/A"
		report.Status = StatusReady
		report.EndpointActive = true
	}

	// Step 8: Commit PJSIP config transaction on full success
	if err := m.reloader.CommitPJSIPConfig(ctx, cfg.Name); err != nil {
		return rollbackTransaction(fmt.Errorf("failed to commit PJSIP config: %w", err), StatusConfigured)
	}

	// Commit state safely in Manager
	m.mu.Lock()
	m.trunks[cfg.Name] = cfg
	m.statuses[cfg.Name] = report
	m.pjsipConfigs[cfg.Name] = pjsipConf
	m.mu.Unlock()

	return report, nil
}

// GetTrunk retrieves a cloned applied trunk configuration by name.
func (m *Manager) GetTrunk(name string) (TrunkConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg, ok := m.trunks[name]
	if !ok {
		return TrunkConfig{}, false
	}
	return cfg.Clone(), true
}

// GetStatus retrieves the live status report of a trunk.
func (m *Manager) GetStatus(name string) (StatusReport, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	status, ok := m.statuses[name]
	return status, ok
}

// GetPJSIPConfig retrieves the generated PJSIP configuration for a trunk.
func (m *Manager) GetPJSIPConfig(name string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	conf, ok := m.pjsipConfigs[name]
	return conf, ok
}

// ListTrunks returns all configured trunks with defensive copies.
func (m *Manager) ListTrunks() []TrunkConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]TrunkConfig, 0, len(m.trunks))
	for _, cfg := range m.trunks {
		list = append(list, cfg.Clone())
	}
	return list
}
