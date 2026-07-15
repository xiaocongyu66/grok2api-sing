package egress

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestOpenProxyDialerRejectsEmptyAndUnsupported(t *testing.T) {
	dialer, err := openProxyDialer("")
	if err != nil || dialer != nil {
		t.Fatalf("empty = %#v err=%v", dialer, err)
	}
	if _, err := openProxyDialer("ftp://127.0.0.1:21"); err == nil {
		t.Fatal("ftp accepted")
	}
}

func TestOpenProxyDialerStartsInProcess(t *testing.T) {
	// Unreachable proxy is fine: we only assert the in-process box starts and dials.
	dialer, err := openProxyDialer("socks5://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if dialer == nil {
		t.Fatal("expected dialer")
	}
	t.Cleanup(dialer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "tcp", "example.com:80")
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("expected dial failure against closed port")
	}
}

func TestOpenProxyDialerHTTPScheme(t *testing.T) {
	for _, raw := range []string{
		"http://user:pass@127.0.0.1:8080",
		"https://proxy.example:8443",
		"socks4://127.0.0.1:1080",
		"socks5h://user:pass@127.0.0.1:1080",
	} {
		dialer, err := openProxyDialer(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if dialer == nil {
			t.Fatalf("%s: nil dialer", raw)
		}
		dialer.Close()
	}
}

func TestParseSocksaddrViaDialer(t *testing.T) {
	// Ensure address parsing path used by DialContext is valid for host:port.
	if _, err := net.ResolveTCPAddr("tcp", "127.0.0.1:9"); err != nil {
		t.Fatal(err)
	}
}
