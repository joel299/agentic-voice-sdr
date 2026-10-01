package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/joel299/agentic-voice-sdr/internal/ownerauth"
	"golang.org/x/term"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("run this command in a local interactive terminal")
	}
	first, err := readSecret("Password: ")
	if err != nil {
		return fmt.Errorf("read password")
	}
	defer clear(first)
	second, err := readSecret("Confirm password: ")
	if err != nil {
		return fmt.Errorf("read confirmation")
	}
	defer clear(second)
	if len(first) == 0 || !bytes.Equal(first, second) {
		return fmt.Errorf("passwords must match and must not be empty")
	}
	if len(first) > 72 { return fmt.Errorf("bcrypt passwords must be at most 72 bytes") }
	hash, err := ownerauth.HashPassword(string(first))
	if err != nil {
		return fmt.Errorf("hash password")
	}
	if err := os.MkdirAll(".runtime-secrets", 0700); err != nil {
		return fmt.Errorf("prepare protected local config")
	}
	dirInfo, err := os.Lstat(".runtime-secrets")
	if err != nil || dirInfo.Mode().Perm()&0077 != 0 {
		return fmt.Errorf(".runtime-secrets must be accessible only to the current user")
	}
	path := filepath.Join(".runtime-secrets", "owner-login.env")
	tmp, err := os.CreateTemp(filepath.Dir(path), ".owner-login-*")
	if err != nil {
		return fmt.Errorf("write protected local config")
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0600); err == nil {
		_, err = fmt.Fprintf(tmp, "OWNER_LOGIN_PASSWORD_HASH=%s\n", hash)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write protected local config")
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace protected local config")
	}
	fmt.Fprintln(os.Stdout, "Owner login password hash saved in protected local configuration.")
	return nil
}

func readSecret(prompt string) ([]byte, error) {
	fmt.Fprint(os.Stderr, prompt)
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return value, err
}
