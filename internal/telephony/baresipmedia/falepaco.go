package baresipmedia

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	FalePacoRegistrar = "98034.falepaco.com.br"
	FalePacoUser      = "100"
)

var ErrInvalidFalePacoPassword = errors.New("invalid Fale Paco SIP password")

var falePacoAccountPrefix = "<sip:100@98034.falepaco.com.br:5060;transport=tcp>;auth_user=100;auth_pass="
var falePacoAccountSuffix = ";outbound=\"sip:98034.falepaco.com.br:5060;transport=tcp\";regint=600;inreq_allowed=no"

// RenderFalePacoAccount builds the fixed, owner-independent Baresip account
// record. Only the password is caller supplied.
func RenderFalePacoAccount(password string) ([]byte, error) {
	if err := ValidateFalePacoPassword(password); err != nil {
		return nil, err
	}
	return []byte(falePacoAccountPrefix + password + falePacoAccountSuffix + "\n"), nil
}

// ValidateFalePacoPassword rejects characters that would change Baresip's
// semicolon-delimited account-parameter structure or create another record.
func ValidateFalePacoPassword(password string) error {
	if password == "" || len(password) > 512 || !utf8.ValidString(password) {
		return ErrInvalidFalePacoPassword
	}
	for _, r := range password {
		if unicode.IsControl(r) || r == ';' || r == '"' || r == '\\' {
			return ErrInvalidFalePacoPassword
		}
	}
	return nil
}

// InspectFalePacoAccount reports only safe profile facts; it never returns the
// credential. It accepts only the exact server-generated account shape.
func InspectFalePacoAccount(contents []byte) (configured, domainMatch, usernameMatch, transportMatch, registrationRequired bool) {
	line := strings.TrimSpace(string(contents))
	if !strings.HasPrefix(line, falePacoAccountPrefix) || !strings.HasSuffix(line, falePacoAccountSuffix) {
		return false, false, false, false, false
	}
	password := strings.TrimSuffix(strings.TrimPrefix(line, falePacoAccountPrefix), falePacoAccountSuffix)
	if ValidateFalePacoPassword(password) != nil {
		return false, false, false, false, false
	}
	return true, true, true, true, true
}

// WritePrivateAccount atomically replaces a local Baresip accounts file with
// owner-only permissions. Error messages never include file contents.
func WritePrivateAccount(path string, contents []byte) error {
	if strings.TrimSpace(path) == "" || len(contents) == 0 {
		return ErrInvalidBaresipProfile
	}
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidBaresipProfile
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidBaresipProfile
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrInvalidBaresipProfile
	}
	tmp, err := os.CreateTemp(dir, ".falepaco-accounts-*")
	if err != nil {
		return ErrInvalidBaresipProfile
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(contents)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return ErrInvalidBaresipProfile
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return ErrInvalidBaresipProfile
	}
	return nil
}
