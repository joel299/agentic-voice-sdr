package baresipmedia

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderFalePacoAccountUsesOnlyCanonicalProfileAndPassword(t *testing.T) {
	account, err := RenderFalePacoAccount("fixture-password!123")
	if err != nil {
		t.Fatal("render account failed")
	}
	want := "<sip:100@98034.falepaco.com.br:5060;transport=tcp>;auth_user=100;auth_pass=fixture-password!123;outbound=\"sip:98034.falepaco.com.br:5060;transport=tcp\";regint=600;inreq_allowed=no\n"
	if string(account) != want {
		t.Fatal("rendered account differs from the canonical Fale Paco account shape")
	}
	configured, domain, username, transport, registration := InspectFalePacoAccount(account)
	if !configured || !domain || !username || !transport || !registration {
		t.Fatalf("canonical account facts not recognized: %v %v %v %v %v", configured, domain, username, transport, registration)
	}
}

func TestFalePacoPasswordCannotInjectAccountParameters(t *testing.T) {
	for _, value := range []string{"", "bad;regint=0", "bad\nline", "bad\"quoted", "bad\\escape"} {
		if _, err := RenderFalePacoAccount(value); err == nil {
			t.Fatal("unsafe account parameter was accepted")
		}
	}
}

func TestWritePrivateAccountIsAtomicAndMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts")
	account, err := RenderFalePacoAccount("fixture-secret")
	if err != nil {
		t.Fatal("render account failed")
	}
	if err := WritePrivateAccount(path, account); err != nil {
		t.Fatal("private account write failed")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal("stat account file failed")
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("account file permission=%v", info.Mode().Perm())
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(account) {
		t.Fatal("account file contents were not written correctly")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "accounts" || strings.HasPrefix(entries[0].Name(), ".falepaco-accounts-") {
		t.Fatal("temporary account file was not cleaned up")
	}
}

func TestPrepareProfileCanUseOnlyTheSuppliedCanonicalAccount(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	modules := filepath.Join(root, "modules")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(modules, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"config": []byte("module\tctrl_tcp.so\n"), "accounts": []byte("wrong-account-must-not-be-copied\n")} {
		if err := os.WriteFile(filepath.Join(source, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(modules, "ctrl_tcp.so"), []byte("module"), 0600); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(root, "gru151_media.so")
	if err := os.WriteFile(media, []byte("module"), 0600); err != nil {
		t.Fatal(err)
	}
	account, _ := RenderFalePacoAccount("fixture-secret")
	profile, err := PrepareProfileWithAccount(source, media, modules, "/tmp/rx.sock", "/tmp/tx.sock", "127.0.0.1:4444", account)
	if err != nil {
		t.Fatal("prepare profile failed")
	}
	defer profile.Close()
	got, err := os.ReadFile(filepath.Join(profile.Directory, "accounts"))
	if err != nil || string(got) != string(account) {
		t.Fatal("private profile did not receive canonical account override")
	}
	info, err := os.Stat(filepath.Join(profile.Directory, "accounts"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("generated account file is not mode 0600")
	}
}
