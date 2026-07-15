package egress

import (
	"encoding/base64"
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

func TestOutboundFromClassicProxy(t *testing.T) {
	out, err := outboundFromProxyURL("socks5h://user:pass@127.0.0.1:1080")
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != C.TypeSOCKS || out.Tag != outboundTag {
		t.Fatalf("out = %#v", out)
	}
}

func TestOutboundFromVMessShareLink(t *testing.T) {
	jsonCfg := `{"v":"2","ps":"n","add":"1.2.3.4","port":"443","id":"11111111-1111-1111-1111-111111111111","aid":"0","scy":"auto","net":"ws","type":"none","host":"example.com","path":"/ws","tls":"tls","sni":"example.com"}`
	link := "vmess://" + base64.StdEncoding.EncodeToString([]byte(jsonCfg))
	out, err := outboundFromProxyURL(link)
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != C.TypeVMess {
		t.Fatalf("type = %s", out.Type)
	}
	opts, ok := out.Options.(*option.VMessOutboundOptions)
	if !ok {
		t.Fatalf("options = %T", out.Options)
	}
	if opts.Server != "1.2.3.4" || opts.ServerPort != 443 || opts.UUID == "" {
		t.Fatalf("opts = %#v", opts)
	}
	if opts.Transport == nil || opts.Transport.Type != C.V2RayTransportTypeWebsocket {
		t.Fatalf("transport = %#v", opts.Transport)
	}
	if opts.TLS == nil || !opts.TLS.Enabled || opts.TLS.ServerName != "example.com" {
		t.Fatalf("tls = %#v", opts.TLS)
	}
}

func TestOutboundFromVLESSShareLink(t *testing.T) {
	link := "vless://11111111-1111-1111-1111-111111111111@1.2.3.4:443?encryption=none&security=tls&sni=example.com&type=ws&host=example.com&path=%2Fws#name"
	out, err := outboundFromProxyURL(link)
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != C.TypeVLESS {
		t.Fatalf("type = %s", out.Type)
	}
	opts := out.Options.(*option.VLESSOutboundOptions)
	if opts.UUID == "" || opts.Server != "1.2.3.4" || opts.TLS == nil {
		t.Fatalf("opts = %#v", opts)
	}
}

func TestOutboundFromHysteria2ShareLink(t *testing.T) {
	link := "hysteria2://secret@1.2.3.4:8443?sni=example.com&insecure=1&obfs=salamander&obfs-password=obfs-secret"
	out, err := outboundFromProxyURL(link)
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != C.TypeHysteria2 {
		t.Fatalf("type = %s", out.Type)
	}
	opts := out.Options.(*option.Hysteria2OutboundOptions)
	if opts.Password != "secret" || opts.Obfs == nil || opts.Obfs.Password != "obfs-secret" {
		t.Fatalf("opts = %#v", opts)
	}
}

func TestOutboundFromHysteriaShareLink(t *testing.T) {
	link := "hysteria://1.2.3.4:443?auth=secret&peer=example.com&upmbps=100&downmbps=100&insecure=1"
	out, err := outboundFromProxyURL(link)
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != C.TypeHysteria {
		t.Fatalf("type = %s", out.Type)
	}
}

func TestOpenProxyDialerVMessStarts(t *testing.T) {
	jsonCfg := `{"v":"2","ps":"n","add":"127.0.0.1","port":"1","id":"11111111-1111-1111-1111-111111111111","aid":"0","scy":"auto","net":"tcp","tls":"none"}`
	link := "vmess://" + base64.RawStdEncoding.EncodeToString([]byte(jsonCfg))
	dialer, err := openProxyDialer(link)
	if err != nil {
		t.Fatal(err)
	}
	if dialer == nil {
		t.Fatal("nil dialer")
	}
	dialer.Close()
}

func TestOutboundFromTrojanAndSS(t *testing.T) {
	out, err := outboundFromProxyURL("trojan://secret@1.2.3.4:443?sni=example.com&type=ws&path=%2F")
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != C.TypeTrojan {
		t.Fatalf("type=%s", out.Type)
	}
	out, err = outboundFromProxyURL("ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@1.2.3.4:8388")
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != C.TypeShadowsocks {
		t.Fatalf("type=%s", out.Type)
	}
}

func TestOutboundFromJSON(t *testing.T) {
	raw := `{"type":"socks","tag":"x","server":"127.0.0.1","server_port":1080}`
	out, err := outboundFromProxyURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != C.TypeSOCKS {
		t.Fatalf("type=%s", out.Type)
	}
}
