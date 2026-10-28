package topology

import (
	"bytes"
	"strconv"
	"strings"
	"text/template"
)

// Role names one process of a topology.
type Role string

const (
	Control    Role = "control"
	NodeA      Role = "nodeA"
	NodeB      Role = "nodeB"
	Global     Role = "global"
	Standalone Role = "standalone"
)

// Coalition is a coalition name and its plaintext password.
type Coalition struct{ Name, Password string }

// DefaultCoalitions is the pair every topology serves unless overridden.
var DefaultCoalitions = []Coalition{{"Blue", "blue123"}, {"Red", "red123"}}

// ProcConfig is everything RenderConfig needs for one process. Defaults
// (frequencies, coalitions per role) are applied by the caller.
type ProcConfig struct {
	Role                                               Role
	HTTPPort, ControlPort, VoicePort, VoiceControlPort int
	Coalitions                                         []Coalition
	GlobalFrequencies, TestFrequencies                 []float32
	APIKey                                             string
	TokenPrivateKey, TokenPublicKey, VCCert, VCKey     string
	RemoteHost                                         string
	PublicAddr                                         string
	// ServerName is the TLS server name for the VoiceControl channel;
	// empty means "localhost" (the pre-generated certificate's SAN).
	ServerName string
}

// yamlSingleQuote renders s as a single-quoted YAML scalar. Single quotes
// carry a Windows path's backslashes literally; only ' is doubled.
func yamlSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func yamlFloats(fs []float32) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = strconv.FormatFloat(float64(f), 'f', -1, 32)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

var configTmpl = template.Must(template.New("config").Funcs(template.FuncMap{
	"q":      yamlSingleQuote,
	"floats": yamlFloats,
}).Parse(`servers:
  http:
    host: 127.0.0.1
    port: {{.HTTPPort}}
  control:
    host: 127.0.0.1
    port: {{.ControlPort}}
  voice:
    host: 127.0.0.1
    port: {{.VoicePort}}

{{if .Coalitions}}coalitions:
{{range .Coalitions}}  - name: {{q .Name}}
    description: ""
    color: ""
    password: {{q .Password}}
{{end}}{{else}}coalitions: []
{{end}}
frequencies:
  testFrequencies: {{floats .TestFrequencies}}
  globalFrequencies: {{floats .GlobalFrequencies}}

general:
  maxRadiosPerUser: 20

security:
  enablePluginAuth: false
  plugins: []
  enableGuestAuth: true
  token:
    expiration: 28800
    privateKeyFile: {{q .TokenPrivateKey}}
    publicKeyFile: {{q .TokenPublicKey}}
    issuer: "https://vcs.itest.local"
    subject: "vcs.itest.local"

voiceControl:
  port: {{.VoiceControlPort}}
  remoteHost: {{q .RemoteHost}}
  listenHost: 127.0.0.1
  certificateFile: {{q .VCCert}}
  privateKeyFile: {{q .VCKey}}
  serverName: {{q .ServerName}}
  publicAddr: {{q .PublicAddr}}
  region: ""

api:
  key: {{q .APIKey}}
`))

// RenderConfig renders one process's config.yaml. Every host is
// 127.0.0.1 and no clientTLS block is emitted (plaintext loopback).
func RenderConfig(c ProcConfig) ([]byte, error) {
	if c.ServerName == "" {
		c.ServerName = "localhost"
	}
	var b bytes.Buffer
	if err := configTmpl.Execute(&b, c); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
