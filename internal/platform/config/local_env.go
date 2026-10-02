package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/joel299/agentic-voice-sdr/internal/telephony/callservice"
)

var RequiredLocalRuntimeVariables = []string{
	"GEMINI_API_KEY", "GEMINI_LIVE_MODEL", "OPENROUTER_API_KEY", "OPENROUTER_JEV_MODEL",
	"BARESIP_CTRL_TCP_ADDR", "BARESIP_PROFILE_DIR", "BARESIP_MEDIA_MODULE_PATH",
	"OUTBOUND_CALL_DESTINATION_ALLOWLIST", "PGHOST", "PGPORT", "PGUSER", "PGDATABASE",
}

// LoadLocalEnv exports a protected literal KEY=value file. It never evaluates
// shell code, substitutes variables, or includes values in errors. Required
// runtime keys absent from the file cannot be inherited from a parent shell.
func LoadLocalEnv(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("local environment file must be a protected regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("cannot open local environment file")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Mode().Perm()&0077 != 0 {
		return errors.New("local environment file changed while opening")
	}
	scanner := bufio.NewScanner(io.LimitReader(f, 1<<20+1))
	values := make(map[string]string)
	size := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		size += len(scanner.Bytes()) + 1
		if size > 1<<20 {
			return errors.New("local environment file is too large")
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || !validLocalEnvKey(key) || strings.ContainsRune(value, 0) {
			return errors.New("invalid local environment assignment")
		}
		if len(value) > 0 && (value[0] == '\'' || value[0] == '"') {
			if len(value) < 2 || value[len(value)-1] != value[0] {
				return errors.New("invalid local environment quoting")
			}
			value = value[1 : len(value)-1]
		}
		if _, exists := values[key]; exists {
			return errors.New("duplicate local environment assignment")
		}
		values[key] = value
	}
	if scanner.Err() != nil {
		return errors.New("cannot parse local environment file")
	}
	for _, key := range RequiredLocalRuntimeVariables {
		_ = os.Unsetenv(key)
	}
	for key, value := range values {
		if os.Setenv(key, value) != nil {
			return errors.New("cannot export local environment assignment")
		}
	}
	return nil
}
func validLocalEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

// ValidateLocalRuntime emits names/presence only; policy normalization uses the
// same implementation as CallService. This gate never creates a provider.
func ValidateLocalRuntime(out io.Writer) error {
	missing := false
	for _, key := range RequiredLocalRuntimeVariables {
		state := "present"
		if strings.TrimSpace(os.Getenv(key)) == "" {
			state = "missing"
			missing = true
		}
		if _, err := fmt.Fprintf(out, "%s=%s\n", key, state); err != nil {
			return errors.New("cannot report local configuration gate")
		}
	}
	if missing {
		return errors.New("required local runtime configuration is missing")
	}
	policy, err := callservice.NewAllowlist(splitCSV(os.Getenv("OUTBOUND_CALL_DESTINATION_ALLOWLIST")))
	if err != nil {
		return errors.New("invalid outbound destination policy")
	}
	if len(policy.Destinations()) != 1 {
		return errors.New("local owner runtime requires exactly one allowed destination")
	}
	_, err = fmt.Fprintln(out, "allowlist_count=1")
	return err
}
