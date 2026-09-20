package sip

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

// PJSIPRegistrationObjectName returns the canonical Asterisk PJSIP registration object name for a trunk.
func PJSIPRegistrationObjectName(trunkName string) string {
	return fmt.Sprintf("trunk-%s-reg", trunkName)
}

// PJSIPEndpointObjectName returns the canonical Asterisk PJSIP endpoint object name for a trunk.
func PJSIPEndpointObjectName(trunkName string) string {
	return fmt.Sprintf("trunk-%s", trunkName)
}

// PJSIPTransportObjectName returns the canonical Asterisk PJSIP transport object name for a transport type.
func PJSIPTransportObjectName(transport TransportType) string {
	return fmt.Sprintf("transport-%s", transport)
}

const pjsipTmplText = `; ==============================================================================
; PJSIP Trunk Configuration: {{ .Name }} (Provider: {{ .Provider }})
; Generated automatically by Agentic Voice SDR SIP Reconciler
; ==============================================================================

{{ if eq .AuthType "userpass" }}[trunk-{{ .Name }}-auth]
type=auth
auth_type=userpass
username={{ .AuthUsername }}
password={{ .Secret }}
{{ if .Realm }}realm={{ .Realm }}{{ end }}
{{ end }}
[trunk-{{ .Name }}-aor]
type=aor
contact=sip:{{ .HostNetworkAddressOrHost }}:{{ .Port }}

[trunk-{{ .Name }}]
type=endpoint
transport=transport-{{ .Transport }}
context=from-external
disallow=all
allow={{ .CodecsString }}
{{ if eq .AuthType "userpass" }}outbound_auth=trunk-{{ .Name }}-auth{{ end }}
aors=trunk-{{ .Name }}-aor
{{ if .FromUser }}from_user={{ .FromUser }}{{ end }}
{{ if .FromDomain }}from_domain={{ .FromDomain }}{{ end }}
{{ if .OutboundProxy }}outbound_proxy=sip:{{ .OutboundProxyNetworkAddressOrProxy }}{{ end }}
{{ if .CallerID }}callerid={{ .CallerID }}{{ end }}

{{ if .RegistrationRequired }}[trunk-{{ .Name }}-reg]
type=registration
transport=transport-{{ .Transport }}
{{ if eq .AuthType "userpass" }}outbound_auth=trunk-{{ .Name }}-auth{{ end }}
server_uri=sip:{{ .RegistrarNetworkAddressOrHost }}:{{ .Port }}
client_uri=sip:{{ .RegistrationIdentity }}@{{ .RegistrarOrHost }}:{{ .Port }}
retry_interval=60
{{ end }}
`

var pjsipTemplate = template.Must(template.New("pjsip").Parse(pjsipTmplText))

type pjsipTemplateData struct {
	TrunkConfig
	CodecsString                       string
	RegistrarOrHost                    string
	RegistrarNetworkAddressOrHost      string
	HostNetworkAddressOrHost           string
	OutboundProxyNetworkAddressOrProxy string
	RegistrationIdentity               string
}

// GeneratePJSIPConfig renders an Asterisk pjsip.conf snippet for the trunk.
func GeneratePJSIPConfig(cfg TrunkConfig) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", fmt.Errorf("cannot generate PJSIP config for invalid trunk: %w", err)
	}

	codecsStr := strings.Join(cfg.Codecs, ",")
	if codecsStr == "" {
		codecsStr = "ulaw,alaw"
	}

	regHost := cfg.Registrar
	if regHost == "" {
		regHost = cfg.Host
	}

	regIdentity := cfg.AuthUsername
	if regIdentity == "" {
		regIdentity = cfg.FromUser
	}

	hostNetwork := cfg.HostNetworkAddress
	if hostNetwork == "" {
		hostNetwork = cfg.Host
	}
	regNetwork := cfg.RegistrarNetworkAddress
	if regNetwork == "" {
		regNetwork = regHost
	}
	proxyNetwork := cfg.OutboundProxyNetworkAddress
	if proxyNetwork == "" {
		proxyNetwork = cfg.OutboundProxy
	}

	data := pjsipTemplateData{
		TrunkConfig:                        cfg,
		CodecsString:                       codecsStr,
		RegistrarOrHost:                    regHost,
		RegistrarNetworkAddressOrHost:      regNetwork,
		HostNetworkAddressOrHost:           hostNetwork,
		OutboundProxyNetworkAddressOrProxy: proxyNetwork,
		RegistrationIdentity:               regIdentity,
	}

	var buf bytes.Buffer
	if err := pjsipTemplate.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to render PJSIP template: %w", err)
	}

	return buf.String(), nil
}

// Regex matches password=<anything up to newline>
var passwordRegexp = regexp.MustCompile(`(?m)^(password=)(.+)$`)

// MaskPJSIPSecrets replaces password fields in PJSIP config strings with ***** for safe logging.
func MaskPJSIPSecrets(pjsipConfig string) string {
	return passwordRegexp.ReplaceAllString(pjsipConfig, "${1}*****")
}
