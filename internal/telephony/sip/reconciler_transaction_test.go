package sip

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

type resolverTxnRunner struct {
	pjsipFailures    int
	resolverFailures int
	healthFailure    bool
	pjsipReloads     int
	resolverReloads  int
}

func (r *resolverTxnRunner) RunCommand(_ context.Context, _ string, args ...string) (string, error) {
	cmd := strings.Join(args, " ")
	if strings.Contains(cmd, "res_resolver_unbound") {
		r.resolverReloads++
		if r.resolverFailures > 0 {
			r.resolverFailures--
			return "", errors.New("resolver reload failed")
		}
		return "OK", nil
	}
	if strings.Contains(cmd, "res_pjsip") {
		r.pjsipReloads++
		if r.pjsipFailures > 0 {
			r.pjsipFailures--
			return "", errors.New("pjsip reload failed")
		}
		return "OK", nil
	}
	if strings.Contains(cmd, "core show uptime") {
		if r.healthFailure {
			return "", errors.New("health failed")
		}
		return "System uptime: 1 second", nil
	}
	return "", nil
}

func newResolverTxnTestReloader(t *testing.T, dir string, runner *resolverTxnRunner) *RealAsteriskReloader {
	t.Helper()
	return &RealAsteriskReloader{configDir: dir, runner: runner,
		groupLookupFn: func(string) (*user.Group, error) { return &user.Group{Gid: "1"}, nil },
		removeFn:      os.Remove}
}

func writeResolverFixture(t *testing.T, dir, trunk, pjsip, hosts, resolver string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, trunk+".conf"), []byte(pjsip), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gru83-pinned.hosts"), []byte(hosts), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resolver_unbound.conf"), []byte(resolver), 0640); err != nil {
		t.Fatal(err)
	}
}

func TestResolverTransactionRestoresFilesOnStageReloadFailure(t *testing.T) {
	dir := t.TempDir()
	trunk := "transactional"
	oldPJSIP := "; gru83-pin host=old.provider.test address=192.0.2.10\n"
	oldHosts := resolverMarker + "\n192.0.2.10 old.provider.test\n"
	oldResolver := resolverMarker + "\n[general]\nresolv = system\ndebug = yes\nhosts = " + filepath.Join(dir, ".gru83-pinned.hosts") + "\n"
	writeResolverFixture(t, dir, trunk, oldPJSIP, oldHosts, oldResolver)
	runner := &resolverTxnRunner{pjsipFailures: 1}
	r := newResolverTxnTestReloader(t, dir, runner)
	err := r.StagePJSIPConfig(context.Background(), trunk, "; gru83-pin host=new.provider.test address=192.0.2.20\n")
	if err == nil || !strings.Contains(err.Error(), "PRIMARY FAILURE") {
		t.Fatalf("expected primary failure, got %v", err)
	}
	assertFile(t, filepath.Join(dir, trunk+".conf"), oldPJSIP)
	assertFile(t, filepath.Join(dir, ".gru83-pinned.hosts"), oldHosts)
	assertFile(t, filepath.Join(dir, "resolver_unbound.conf"), oldResolver)
	assertNoTmp(t, dir)
}

func TestResolverTransactionRejectsUnmanagedResolverWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	trunk := "unmanaged"
	old := "[general]\nresolv = system\nnameserver = 192.0.2.53\n"
	if err := os.WriteFile(filepath.Join(dir, trunk+".conf"), []byte("old"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resolver_unbound.conf"), []byte(old), 0640); err != nil {
		t.Fatal(err)
	}
	runner := &resolverTxnRunner{}
	r := newResolverTxnTestReloader(t, dir, runner)
	if err := r.StagePJSIPConfig(context.Background(), trunk, "; gru83-pin host=x.test address=192.0.2.1\n"); err == nil {
		t.Fatal("expected unmanaged resolver rejection")
	}
	assertFile(t, filepath.Join(dir, "resolver_unbound.conf"), old)
	if _, err := os.Stat(filepath.Join(dir, ".gru83-pinned.hosts")); !os.IsNotExist(err) {
		t.Fatalf("managed hosts residual: %v", err)
	}
	if runner.pjsipReloads != 0 {
		t.Fatalf("PJSIP reload executed: %d", runner.pjsipReloads)
	}
}

func TestResolverTransactionPreservesManagedDirectives(t *testing.T) {
	dir := t.TempDir()
	trunk := "managed"
	hosts := resolverMarker + "\n192.0.2.1 old.provider.test\n"
	resolver := resolverMarker + "\n[general]\nresolv = system\nnameserver = 192.0.2.53\nta_file = /etc/ssl/cert.pem\ndebug = yes\nhosts = " + filepath.Join(dir, ".gru83-pinned.hosts") + "\n"
	writeResolverFixture(t, dir, trunk, "; gru83-pin host=old.provider.test address=192.0.2.1\n", hosts, resolver)
	r := newResolverTxnTestReloader(t, dir, &resolverTxnRunner{})
	if _, err := r.syncPinnedResolver(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "resolver_unbound.conf"))
	text := string(got)
	for _, needle := range []string{"resolv = system", "nameserver = 192.0.2.53", "ta_file = /etc/ssl/cert.pem", "debug = yes"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("directive lost: %s", needle)
		}
	}
}

func TestResolverTransactionRestoresBothFilesAfterSecondRenameFailure(t *testing.T) {
	dir := t.TempDir()
	trunk := "renamefail"
	oldHosts := resolverMarker + "\n192.0.2.1 old.test\n"
	oldResolver := resolverMarker + "\n[general]\nresolv = system\n"
	writeResolverFixture(t, dir, trunk, "; gru83-pin host=old.test address=192.0.2.1\n", oldHosts, oldResolver)
	calls := 0
	r := newResolverTxnTestReloader(t, dir, &resolverTxnRunner{})
	r.renameFn = func(old, new string) error {
		calls++
		if calls == 2 {
			return errors.New("injected second rename failure")
		}
		return os.Rename(old, new)
	}
	if _, err := r.syncPinnedResolver(); err == nil {
		t.Fatal("expected rename failure")
	}
	assertFile(t, filepath.Join(dir, ".gru83-pinned.hosts"), oldHosts)
	assertFile(t, filepath.Join(dir, "resolver_unbound.conf"), oldResolver)
	assertNoTmp(t, dir)
}

func TestResolverTransactionRestoresBothFilesAfterFirstRenameFailure(t *testing.T) {
	dir := t.TempDir()
	trunk := "renamefailfirst"
	oldHosts := resolverMarker + "\n192.0.2.1 old.test\n"
	oldResolver := resolverMarker + "\n[general]\nresolv = system\n"
	writeResolverFixture(t, dir, trunk, "; gru83-pin host=old.test address=192.0.2.1\n", oldHosts, oldResolver)
	r := newResolverTxnTestReloader(t, dir, &resolverTxnRunner{})
	r.renameFn = func(_, _ string) error { return errors.New("injected first rename failure") }
	if _, err := r.syncPinnedResolver(); err == nil {
		t.Fatal("expected first rename failure")
	}
	assertFile(t, filepath.Join(dir, ".gru83-pinned.hosts"), oldHosts)
	assertFile(t, filepath.Join(dir, "resolver_unbound.conf"), oldResolver)
	assertNoTmp(t, dir)
}

func TestResolverTransactionWriteFailureLeavesNoManagedFiles(t *testing.T) {
	dir := t.TempDir()
	trunk := "writefail"
	if err := os.WriteFile(filepath.Join(dir, trunk+".conf"), []byte("; gru83-pin host=x.test address=192.0.2.1\n"), 0640); err != nil {
		t.Fatal(err)
	}
	r := newResolverTxnTestReloader(t, dir, &resolverTxnRunner{})
	r.writeFileFn = func(path string, _ []byte, _ os.FileMode) error {
		if strings.HasSuffix(path, "resolver_unbound.conf.tmp") {
			return errors.New("injected write failure")
		}
		return os.WriteFile(path, []byte("unused"), 0640)
	}
	if _, err := r.syncPinnedResolver(); err == nil {
		t.Fatal("expected resolver write failure")
	}
	if _, err := os.Stat(filepath.Join(dir, ".gru83-pinned.hosts")); !os.IsNotExist(err) {
		t.Fatalf("hosts residual after write failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "resolver_unbound.conf")); !os.IsNotExist(err) {
		t.Fatalf("resolver residual after write failure: %v", err)
	}
}

func TestResolverRollbackRestoresOldMappingAfterSuccessfulStage(t *testing.T) {
	dir := t.TempDir()
	trunk := "rollbackstate"
	oldPJSIP := "; gru83-pin host=old.test address=192.0.2.1\n"
	oldHosts := resolverMarker + "\n192.0.2.1 old.test\n"
	oldResolver := resolverMarker + "\n[general]\nresolv = system\n"
	writeResolverFixture(t, dir, trunk, oldPJSIP, oldHosts, oldResolver)
	r := newResolverTxnTestReloader(t, dir, &resolverTxnRunner{})
	if err := r.StagePJSIPConfig(context.Background(), trunk, "; gru83-pin host=new.test address=192.0.2.2\n"); err != nil {
		t.Fatal(err)
	}
	if err := r.RollbackPJSIPConfig(context.Background(), trunk); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(dir, trunk+".conf"), oldPJSIP)
	assertFile(t, filepath.Join(dir, ".gru83-pinned.hosts"), oldHosts)
}

func TestResolverTransaction(t *testing.T) {
	dir := t.TempDir()
	trunk := "resolverrollback"
	writeResolverFixture(t, dir, trunk, "; gru83-pin host=old.test address=192.0.2.1\n", resolverMarker+"\n192.0.2.1 old.test\n", resolverMarker+"\n[general]\nresolv = system\n")
	r := newResolverTxnTestReloader(t, dir, &resolverTxnRunner{pjsipFailures: 1, resolverFailures: 1})
	err := r.StagePJSIPConfig(context.Background(), trunk, "; gru83-pin host=new.test address=192.0.2.2\n")
	if err == nil || !strings.Contains(err.Error(), "ROLLBACK FAILURE") {
		t.Fatalf("rollback error was not propagated: %v", err)
	}
}

func TestGeneratePJSIPConfigIPv6UsesBrackets(t *testing.T) {
	cfg := TrunkConfig{Name: "ipv6", Host: "sip.provider.test", HostNetworkAddress: "2001:db8::10", Registrar: "reg.provider.test", RegistrarNetworkAddress: "[2001:db8::11]:5070", Port: 5061, Transport: TransportTLS, AuthType: AuthIP, Enabled: true, RegistrationRequired: true, OutboundProxy: "proxy.provider.test", OutboundProxyNetworkAddress: "[2001:db8::12]:5090", FromUser: "test"}
	rendered, err := GeneratePJSIPConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"contact=sip:[2001:db8::10]:5061", "server_uri=sip:reg.provider.test:5061", "client_uri=sip:test@reg.provider.test:5061", "outbound_proxy=sip:[2001:db8::12]:5090"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("IPv6 rendering missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "sip:2001:db8::") {
		t.Fatalf("unbracketed IPv6 URI rendered: %s", rendered)
	}
}
func TestOutboundProxyHostPortMetadataUsesHostname(t *testing.T) {
	cfg := TrunkConfig{Name: "proxy", Host: "provider.test", HostNetworkAddress: "192.0.2.10", Port: 5061, Transport: TransportTLS, AuthType: AuthIP, Enabled: true, OutboundProxy: "proxy.provider.test:5061", OutboundProxyNetworkAddress: "192.0.2.20:5061"}
	rendered, err := GeneratePJSIPConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "; gru83-pin host=proxy.provider.test address=192.0.2.20:5061") {
		t.Fatalf("proxy metadata contains invalid host: %s", rendered)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s changed:\nwant %q\ngot %q", path, want, string(got))
	}
}
func assertNoTmp(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temporary file remains: %s", e.Name())
		}
	}
}
