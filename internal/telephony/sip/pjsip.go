package sip

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

const pjsipTmplText = `; ==============================================================================
; PJSIP Trunk Configuration: {{ .Name }} (Provider: {{ .Provider }})
; Generated automatically by Agentic Voice SDR SIP Reconciler
; ==============================================================================

[transport-{{ .Transport }}]
type=transport
protocol={{ .Transport }}
bind=0.0.0.0:{{ if eq .Transport "tls" }}5061{{ else }}5060{{ end }}

{{ if eq .AuthType "userpass" }}[trunk-{{ .Name }}-auth]
type=auth
auth_type=userpass
username={{ .AuthUsername }}
password={{ .Secret }}
{{ if .Realm }}realm={{ .Realm }}{{ end }}
{{ end }}
[trunk-{{ .Name }}-aor]
type=aor
contact=sip:{{ .Host }}:{{ .Port }}

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
{{ if .OutboundProxy }}outbound_proxy=sip:{{ .OutboundProxy }}{{ end }}
{{ if .CallerID }}callerid={{ .CallerID }}{{ end }}

{{ if .RegistrationRequired }}[trunk-{{ .Name }}-reg]
type=registration
transport=transport-{{ .Transport }}
{{ if eq .AuthType "userpass" }}outbound_auth=trunk-{{ .Name }}-auth{{ end }}
server_uri=sip:{{ .RegistrarOrHost }}:{{ .Port }}
client_uri=sip:{{ .AuthUsername }}@{{ .RegistrarOrHost }}:{{ .Port }}
retry_interval=60
{{ end }}
`

var pjsipTemplate = template.Must(template.New("pjsip").Parse(pjsipTmplText))

type pjsipTemplateData struct {
	TrunkConfig
	CodecsString    string
	RegistrarOrHost string
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

	data := pjsipTemplateData{
		TrunkConfig:     cfg,
		CodecsString:    codecsStr,
		RegistrarOrHost: regHost,
	}

	var buf bytes.Buffer
	if err := pjsipTemplate.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to render PJSIP template: %w", err)
	}

	return buf.String(), nil
}

var passwordRegexp = regexp.MustCompile(`(?i)(password=)([^\s
]+)`)

// MaskPJSIPSecrets replaces password fields in PJSIP config strings with ***** for safe logging.
func MaskPJSIPSecrets(pjsipConfig string) string {
	return passwordRegexp.ReplaceAllString(pjsipConfig, "${1}*****")
}
