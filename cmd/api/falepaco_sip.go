package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/joel299/agentic-voice-sdr/internal/httpapi"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/baresipmedia"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/control"
)

type falePacoSIPService struct {
	accountPath string
	controller  interface {
		RegistrationStatus(context.Context) (control.RegistrationStatus, error)
	}
}

func newFalePacoSIPService(profileDir string, controller interface {
	RegistrationStatus(context.Context) (control.RegistrationStatus, error)
}) *falePacoSIPService {
	return &falePacoSIPService{accountPath: filepath.Join(profileDir, "accounts"), controller: controller}
}

func (s *falePacoSIPService) Get(context.Context) (httpapi.FalePacoSIPConfig, error) {
	configured, err := s.passwordConfigured()
	if err != nil {
		return httpapi.FalePacoSIPConfig{}, err
	}
	return httpapi.CanonicalFalePacoSIPForRuntime(configured), nil
}

func (s *falePacoSIPService) ValidateRegistration(ctx context.Context) (httpapi.FalePacoSIPValidation, error) {
	configured, err := s.passwordConfigured()
	if err != nil {
		return httpapi.FalePacoSIPValidation{}, err
	}
	_, domain, username, transport, registrationRequired := s.accountFacts()
	status, err := s.controller.RegistrationStatus(ctx)
	if err != nil {
		return httpapi.FalePacoSIPValidation{}, err
	}
	registered := status.State == control.RegistrationRegistered
	return httpapi.FalePacoSIPValidation{
		OK:                 configured && domain && username && transport && registrationRequired && registered,
		PasswordConfigured: configured, CredentialPresent: configured, DomainMatch: domain,
		UsernameMatch: username, RealmMatch: registered, Transport: "tcp",
		RegistrationRequired: registrationRequired, RegistrationState: string(status.State),
	}, nil
}

func (s *falePacoSIPService) passwordConfigured() (bool, error) {
	info, err := os.Lstat(s.accountPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0600 {
		return false, errors.New("Baresip account must be a regular 0600 file")
	}
	contents, err := os.ReadFile(s.accountPath)
	if err != nil {
		return false, errors.New("read protected Baresip account failed")
	}
	configured, _, _, _, _ := baresipmedia.InspectFalePacoAccount(contents)
	return configured, nil
}

func (s *falePacoSIPService) accountFacts() (configured, domain, username, transport, registrationRequired bool) {
	info, err := os.Lstat(s.accountPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0600 {
		return false, false, false, false, false
	}
	contents, err := os.ReadFile(s.accountPath)
	if err != nil {
		return false, false, false, false, false
	}
	return baresipmedia.InspectFalePacoAccount(contents)
}
