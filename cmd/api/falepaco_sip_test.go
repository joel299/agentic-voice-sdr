package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/baresipmedia"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/control"
)

type fakeFalePacoRegistration struct {
	status control.RegistrationStatus
	err    error
}

func (f fakeFalePacoRegistration) RegistrationStatus(context.Context) (control.RegistrationStatus, error) {
	return f.status, f.err
}

func TestFalePacoSIPServiceValidationUsesProtectedCanonicalAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts")
	account, err := baresipmedia.RenderFalePacoAccount("fixture-password-must-not-escape")
	if err != nil {
		t.Fatal("render canonical account failed")
	}
	if err := baresipmedia.WritePrivateAccount(path, account); err != nil {
		t.Fatal("write protected account failed")
	}
	service := newFalePacoSIPService(filepath.Dir(path), nil, fakeFalePacoRegistration{status: control.RegistrationStatus{State: control.RegistrationRegistered}}, nil)
	got, err := service.ValidateRegistration(context.Background())
	if err != nil {
		t.Fatal("validate registration failed")
	}
	want := true
	if got.OK != want || !got.PasswordConfigured || !got.CredentialPresent || !got.DomainMatch || !got.UsernameMatch || !got.RealmMatch || got.Transport != "tcp" || !got.RegistrationRequired || got.RegistrationState != string(control.RegistrationRegistered) {
		t.Fatalf("unexpected safe validation facts: %#v", got)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("runtime credential file is not protected with mode 0600")
	}
}

func TestFalePacoSIPServiceDoesNotClaimRegistrationOrAcceptNoncanonicalAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts")
	if err := os.WriteFile(path, []byte("<sip:100@wrong.example>;auth_pass=not-returned\n"), 0600); err != nil {
		t.Fatal(err)
	}
	service := newFalePacoSIPService(filepath.Dir(path), nil, fakeFalePacoRegistration{status: control.RegistrationStatus{State: control.RegistrationFailed, Detail: "private auth details"}}, nil)
	got, err := service.ValidateRegistration(context.Background())
	if err != nil {
		t.Fatal("validate registration failed")
	}
	if got.OK || got.PasswordConfigured || got.CredentialPresent || got.DomainMatch || got.UsernameMatch || got.RealmMatch || got.RegistrationState != string(control.RegistrationFailed) {
		t.Fatalf("noncanonical or rejected account reported ready: %#v", got)
	}
}
