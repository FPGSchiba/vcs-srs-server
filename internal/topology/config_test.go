package topology

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/state"
	"gopkg.in/yaml.v3"
)

func rows() map[string]ProcConfig {
	base := ProcConfig{
		HTTPPort: 18080, ControlPort: 15002, VoicePort: 15003, VoiceControlPort: 15004,
		APIKey: "k-123", TokenPrivateKey: "/t/key.pem", TokenPublicKey: "/t/pub.pem",
		VCCert: "/t/vc-cert.pem", VCKey: "/t/vc-key.pem",
	}
	control := base
	control.Role = Control
	control.Coalitions = DefaultCoalitions
	control.GlobalFrequencies = []float32{249.0}
	control.TestFrequencies = []float32{247.2}
	control.RemoteHost = "127.0.0.1"

	dev := base
	dev.Role = NodeA
	dev.RemoteHost = "127.0.0.1"
	dev.PublicAddr = "127.0.0.1:" + strconv.Itoa(base.VoicePort)

	glob := dev
	glob.Role = Global
	glob.GlobalFrequencies = []float32{249.0}

	win := dev
	win.VCCert = `C:\Users\a b\Temp\x'y\vc-cert.pem`

	return map[string]ProcConfig{"control": control, "node-dev-like": dev, "node-with-globals": glob, "windows-path": win}
}

func TestRenderConfigRoundTrip(t *testing.T) {
	for name, pc := range rows() {
		t.Run(name, func(t *testing.T) {
			b, err := RenderConfig(pc)
			if err != nil {
				t.Fatal(err)
			}
			var st state.SettingsState
			if err := yaml.Unmarshal(b, &st); err != nil {
				t.Fatalf("unmarshal: %v\n%s", err, b)
			}
			if st.Servers.HTTP != (state.ServerSetting{Host: "127.0.0.1", Port: pc.HTTPPort}) ||
				st.Servers.Control != (state.ServerSetting{Host: "127.0.0.1", Port: pc.ControlPort}) ||
				st.Servers.Voice != (state.ServerSetting{Host: "127.0.0.1", Port: pc.VoicePort}) {
				t.Errorf("servers = %+v", st.Servers)
			}
			vc := st.VoiceControl
			if vc.Port != pc.VoiceControlPort || vc.RemoteHost != pc.RemoteHost || vc.ListenHost != "127.0.0.1" ||
				vc.CertificateFile != pc.VCCert || vc.PrivateKeyFile != pc.VCKey || vc.ServerName != "localhost" ||
				vc.PublicAddr != pc.PublicAddr || vc.Region != "" {
				t.Errorf("voiceControl = %+v", vc)
			}
			sec := st.Security
			if !sec.EnableGuestAuth || sec.EnablePluginAuth || len(sec.Plugins) != 0 ||
				sec.Token.Expiration != 28800 || sec.Token.PrivateKeyFile != pc.TokenPrivateKey ||
				sec.Token.PublicKeyFile != pc.TokenPublicKey || sec.Token.Issuer != "https://vcs.itest.local" ||
				sec.Token.Subject != "vcs.itest.local" {
				t.Errorf("security = %+v", sec)
			}
			if st.General.MaxRadiosPerUser != 20 || st.Api.Key != pc.APIKey {
				t.Errorf("general/api = %+v %+v", st.General, st.Api)
			}
			if st.ClientTLS != (state.ClientTLSSettings{}) {
				t.Errorf("clientTLS must be empty: %+v", st.ClientTLS)
			}
			if len(st.Coalitions) != len(pc.Coalitions) {
				t.Fatalf("coalitions = %+v", st.Coalitions)
			}
			for i, c := range pc.Coalitions {
				if st.Coalitions[i].Name != c.Name || st.Coalitions[i].Password != c.Password {
					t.Errorf("coalition %d = %+v", i, st.Coalitions[i])
				}
			}
			if !floatsEq(st.Frequencies.GlobalFrequencies, pc.GlobalFrequencies) ||
				!floatsEq(st.Frequencies.TestFrequencies, pc.TestFrequencies) {
				t.Errorf("frequencies = %+v", st.Frequencies)
			}
		})
	}
}

func floatsEq(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRenderConfigNeverWildcard(t *testing.T) {
	for name, pc := range rows() {
		b, err := RenderConfig(pc)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if strings.Contains(s, "0.0.0.0") || strings.Contains(s, "port: 80\n") || strings.Contains(s, "port: 80 ") {
			t.Errorf("%s renders a wildcard host or port 80:\n%s", name, s)
		}
	}
}

func TestFreePorts(t *testing.T) {
	for i := 0; i < 20; i++ {
		p, err := FreeUDPPort()
		if err != nil {
			t.Fatal(err)
		}
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: p})
		if err != nil {
			t.Fatalf("udp port %d not bindable: %v", p, err)
		}
		c.Close()
		q, err := FreeTCPPort()
		if err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(q))
		if err != nil {
			t.Fatalf("tcp port %d not bindable: %v", q, err)
		}
		l.Close()
	}
}
