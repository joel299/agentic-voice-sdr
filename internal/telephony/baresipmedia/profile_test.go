package baresipmedia

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIProfilePreservesWorkingFalePacoSIPIdentity(t *testing.T) {
	root := t.TempDir()
	source, systemModules := filepath.Join(root, "owner"), filepath.Join(root, "system-modules")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(systemModules, 0700); err != nil {
		t.Fatal(err)
	}
	ownerConfig := "module_path\t/usr/lib/baresip/modules\n" +
		"audio_player\talsa,default\n" +
		"audio_source\talsa,default\n" +
		"ctrl_tcp_listen\t127.0.0.1:4444\n"
	if err := os.WriteFile(filepath.Join(source, "config"), []byte(ownerConfig), 0600); err != nil {
		t.Fatal(err)
	}
	account, err := RenderFalePacoAccount("fixture-password")
	if err != nil {
		t.Fatal("render canonical account")
	}
	if err := os.WriteFile(filepath.Join(source, "accounts"), account, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(systemModules, "ctrl_tcp.so"), []byte("system module"), 0600); err != nil {
		t.Fatal(err)
	}
	mediaModule := filepath.Join(root, "gru151_media.so")
	if err := os.WriteFile(mediaModule, []byte("media module"), 0600); err != nil {
		t.Fatal(err)
	}
	profile, err := PrepareProfile(source, mediaModule, systemModules, "/tmp/api-rx.sock", "/tmp/api-tx.sock", "127.0.0.1:4444")
	if err != nil {
		t.Fatal("prepare API Baresip profile")
	}
	defer profile.Close()
	generatedAccount, err := os.ReadFile(filepath.Join(profile.Directory, "accounts"))
	if err != nil || !bytes.Equal(generatedAccount, account) {
		t.Fatal("normal API startup changed the working SIP account identity")
	}
	configured, domain, username, tcp, registration := InspectFalePacoAccount(generatedAccount)
	if !configured || !domain || !username || !tcp || !registration {
		t.Fatal("generated API profile differs from the canonical Fale Paco registration identity")
	}
	generatedConfig, err := os.ReadFile(filepath.Join(profile.Directory, "config"))
	if err != nil {
		t.Fatal("read generated profile config")
	}
	for _, want := range []string{"audio_player\tgru151_media,/tmp/api-rx.sock", "audio_source\tgru151_media,/tmp/api-tx.sock", "ctrl_tcp_listen\t127.0.0.1:4444"} {
		if !strings.Contains(string(generatedConfig), want) {
			t.Fatalf("generated API profile missing intentional media/control override %q", want)
		}
	}
}

func TestPrepareProfileUsesGeneratedSocketsWithoutMutatingSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	modules := filepath.Join(root, "system-modules")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(modules, 0700); err != nil {
		t.Fatal(err)
	}
	config := "audio_player\talsa,default\n" +
		"audio_source\talsa,default\n" +
		"module_path\t/usr/lib/baresip/modules\n" +
		"module\tstdio.so\n" +
		"ctrl_tcp_listen\t127.0.0.1:5555\n"
	account := "sip:user@example.invalid;auth_pass=never-print-this"
	if err := os.WriteFile(filepath.Join(source, "config"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	accountPath := filepath.Join(source, "accounts")
	if err := os.WriteFile(accountPath, []byte(account), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modules, "ctrl_tcp.so"), []byte("system-module"), 0600); err != nil {
		t.Fatal(err)
	}
	module := filepath.Join(root, "gru151_media.so")
	if err := os.WriteFile(module, []byte("compiled-module"), 0600); err != nil {
		t.Fatal(err)
	}
	profile, err := PrepareProfile(source, module, modules, "/private/rx.sock", "/private/tx.sock", "127.0.0.1:4444")
	if err != nil {
		t.Fatal(err)
	}
	defer profile.Close()
	generated, err := os.ReadFile(filepath.Join(profile.Directory, "config"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"audio_player\tgru151_media,/private/rx.sock",
		"audio_source\tgru151_media,/private/tx.sock",
		"auplay_srate\t16000",
		"ausrc_srate\t24000",
		"ctrl_tcp_listen\t127.0.0.1:4444",
		"module\tgru151_media.so",
		"module\tctrl_tcp.so",
	} {
		if !strings.Contains(string(generated), want) {
			t.Fatalf("generated config missing %q", want)
		}
	}
	if strings.Contains(string(generated), "127.0.0.1:5555") {
		t.Fatal("generated profile retained an alternate control listener")
	}
	generatedAccounts, err := os.ReadFile(filepath.Join(profile.Directory, "accounts"))
	if err != nil || string(generatedAccounts) != account {
		t.Fatal("source accounts were not copied intact")
	}
	info, err := os.Stat(filepath.Join(profile.Directory, "accounts"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("generated accounts permissions = %v, err=%v", info, err)
	}
	original, err := os.ReadFile(filepath.Join(source, "config"))
	if err != nil || string(original) != config {
		t.Fatal("source Baresip config was modified")
	}
	if _, err := os.Stat(filepath.Join(profile.Modules, "ctrl_tcp.so")); err != nil {
		t.Fatal("system control module is not in generated module path")
	}
}

func TestPrepareProfileRequiresLoopbackControlEndpoint(t *testing.T) {
	if _, err := PrepareProfile("/source", "/media.so", "/modules", "/rx", "/tx", "0.0.0.0:4444"); err != ErrInvalidBaresipProfile {
		t.Fatalf("error = %v, want loopback-only profile rejection", err)
	}
}
