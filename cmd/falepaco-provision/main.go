package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/joel299/agentic-voice-sdr/internal/httpapi"
	"github.com/joel299/agentic-voice-sdr/internal/telephony/sip"
)

const runtimeEnvPath = "/root/agentic-voice-sdr/.runtime-secrets/falepaco.env"
const canonicalDomain = "98034.falepaco.com.br"

type runtimeCredentials struct {
	domain, username, extension, password string
}

func loadRuntimeCredentials(ctx context.Context, path string) (runtimeCredentials, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return runtimeCredentials{}, fmt.Errorf("runtime credential file is missing or not restrictive")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return runtimeCredentials{}, fmt.Errorf("runtime credential file owner does not match process")
	}
	// Source the owner-managed shell env file without ever logging its output.
	// Values are transferred to this process only through a private pipe.
	const script = `set -a; . "$1" >/dev/null 2>&1 || exit 10; printf '%s\0' "$FALEPACO_SIP_DOMAIN" "$FALEPACO_SIP_USERNAME" "$FALEPACO_SIP_EXTENSION" "$FALEPACO_SIP_PASSWORD"`
	cmd := exec.CommandContext(ctx, "bash", "--noprofile", "--norc", "-c", script, "falepaco-runtime", path)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		return runtimeCredentials{}, fmt.Errorf("failed to load runtime credential file")
	}
	parts := strings.Split(stdout.String(), "\x00")
	if len(parts) != 5 || parts[0] == "" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return runtimeCredentials{}, fmt.Errorf("runtime credential file is missing required fields")
	}
	return runtimeCredentials{domain: parts[0], username: parts[1], extension: parts[2], password: parts[3]}, nil
}

func provisionRequest(c runtimeCredentials) httpapi.SIPConfigRequest {
	return httpapi.SIPConfigRequest{
		Provider: "falepaco", Name: "falepaco", Host: c.domain, Port: 5060, Transport: "udp",
		Registrar: c.domain,
		Auth:      httpapi.SIPAuthRequest{Type: "userpass", Username: c.username, Secret: c.password},
		FromUser:  c.extension, RegistrationRequired: true, Enabled: true,
	}
}

func configValue(config, section, key string) string {
	active := false
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			active = line == "["+section+"]"
			continue
		}
		if active {
			name, value, ok := strings.Cut(line, "=")
			if ok && name == key {
				return value
			}
		}
	}
	return ""
}

func yes(ok bool) string {
	if ok {
		return "yes"
	}
	return "no"
}

func run() int {
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()
	credentials, err := loadRuntimeCredentials(ctx, runtimeEnvPath)
	if err != nil {
		fmt.Println("credential_file_loaded=no")
		return 2
	}
	fmt.Println("credential_file_exists=yes")
	fmt.Println("credential_file_loaded=yes")
	fmt.Println("credential_source=runtime_env_file")
	request := provisionRequest(credentials)
	canonical, err := request.ToCanonical()
	if err != nil {
		fmt.Println("request_valid=no")
		return 2
	}
	generated, err := sip.GeneratePJSIPConfig(canonical)
	if err != nil {
		fmt.Println("pjsip_generation=failed")
		return 2
	}
	domainMatch := credentials.domain == canonicalDomain && canonical.Host == credentials.domain && canonical.Registrar == credentials.domain
	usernameMatch := configValue(generated, "trunk-falepaco-auth", "username") == credentials.username
	extensionMatch := configValue(generated, "trunk-falepaco", "from_user") == credentials.extension
	password := configValue(generated, "trunk-falepaco-auth", "password")
	passwordMatch := password != "" && password == credentials.password
	fmt.Println("domain_match=" + yes(domainMatch))
	fmt.Println("username_match=" + yes(usernameMatch))
	fmt.Println("extension_match=" + yes(extensionMatch))
	fmt.Println("password_match=" + yes(passwordMatch))
	fmt.Println("secrets_redacted=yes")
	if !domainMatch || !usernameMatch || !extensionMatch || !passwordMatch {
		fmt.Println("provisioning_aborted=credential_mismatch")
		return 3
	}
	if canonical.HostNetworkAddress != "" || canonical.RegistrarNetworkAddress != "" || canonical.OutboundProxyNetworkAddress != "" {
		fmt.Println("hardcoded_ip=no")
		return 3
	}
	fmt.Println("configured_host=" + canonical.Host)
	fmt.Println("transport=" + string(canonical.Transport))
	fmt.Printf("port=%d\n", canonical.Port)
	fmt.Println("server_uri=sip:" + canonical.Registrar + ":5060")
	fmt.Println("client_uri=sip:" + canonical.AuthUsername + "@" + canonical.Registrar + ":5060")
	fmt.Println("pjsip_auth_present=" + yes(strings.Contains(generated, "[trunk-falepaco-auth]")))
	fmt.Println("hardcoded_ip=no")
	manager, err := sip.NewManager(sip.DefaultNetworkDialer{}, sip.NewRealAsteriskReloader("/etc/asterisk/pjsip.d", nil))
	if err != nil {
		fmt.Println("manager_init=failed")
		return 2
	}
	status, err := manager.ApplyTrunk(ctx, canonical)
	fmt.Println("registration_state=" + status.RegistrationState)
	fmt.Println("manager_status=" + string(status.Status))
	fmt.Println("endpoint_active=" + yes(status.EndpointActive))
	if err != nil {
		fmt.Println("manager_apply=failed")
		return 1
	}
	fmt.Println("manager_apply=passed")
	return 0
}

func main() { os.Exit(run()) }
