package egress

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	sjson "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badoption"
)

const outboundTag = "proxy"

// outboundFromProxyURL converts a proxy URL / share link / sing-box outbound JSON
// into one option.Outbound. Formats follow common panel share links and sing-box outbound JSON.
func outboundFromProxyURL(raw string) (option.Outbound, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return option.Outbound{}, fmt.Errorf("代理地址为空")
	}
	// Raw JSON: single outbound object or full options with outbounds[]
	if strings.HasPrefix(raw, "{") {
		return parseJSONOutbound(raw)
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "vmess://"):
		return parseVMessLink(raw)
	case strings.HasPrefix(lower, "ss://"):
		return parseShadowsocksLink(raw)
	case strings.HasPrefix(lower, "ssr://"):
		return option.Outbound{}, fmt.Errorf("ShadowsocksR 已在 sing-box 中移除，请改用 ss/vmess/vless 等协议")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return option.Outbound{}, fmt.Errorf("解析代理地址: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "http", "https", "socks4", "socks4a", "socks5", "socks5h":
		return classicOutbound(parsed)
	case "vless":
		return parseVLESSLink(parsed)
	case "trojan":
		return parseTrojanLink(parsed)
	case "hysteria", "hy":
		return parseHysteriaLink(parsed)
	case "hysteria2", "hy2":
		return parseHysteria2Link(parsed)
	case "tuic":
		return parseTUICLink(parsed)
	case "anytls":
		return parseAnyTLSLink(parsed)
	case "ssh":
		return parseSSHLink(parsed)
	case "wireguard", "wg":
		return parseWireGuardLink(parsed)
	case "shadowtls":
		return parseShadowTLSLink(parsed)
	default:
		return option.Outbound{}, fmt.Errorf("不支持的代理协议 %q（支持 http/socks/ss/vmess/vless/trojan/hysteria/hysteria2/tuic/anytls/ssh/wireguard 及 sing-box outbound JSON）", scheme)
	}
}

func parseJSONOutbound(raw string) (option.Outbound, error) {
	// Same outbound options registry as the embedded box (all protocol types).
	ctx := newProxyBoxContext(context.Background())
	// Prefer full options document with outbounds.
	if options, err := sjson.UnmarshalExtendedContext[option.Options](ctx, []byte(raw)); err == nil && len(options.Outbounds) > 0 {
		out := options.Outbounds[0]
		if out.Tag == "" {
			out.Tag = outboundTag
		}
		return out, nil
	}
	// Single outbound object { "type": "...", ... }
	out, err := sjson.UnmarshalExtendedContext[option.Outbound](ctx, []byte(raw))
	if err != nil {
		return option.Outbound{}, fmt.Errorf("解析 sing-box outbound JSON: %w", err)
	}
	if out.Type == "" {
		return option.Outbound{}, fmt.Errorf("outbound JSON 缺少 type")
	}
	if out.Tag == "" {
		out.Tag = outboundTag
	}
	return out, nil
}

func classicOutbound(parsed *url.URL) (option.Outbound, error) {
	host := parsed.Hostname()
	if host == "" {
		return option.Outbound{}, fmt.Errorf("代理地址缺少主机")
	}
	port, err := parsePort(parsed.Port(), defaultPort(parsed.Scheme))
	if err != nil {
		return option.Outbound{}, err
	}
	username := ""
	password := ""
	if parsed.User != nil {
		username = parsed.User.Username()
		password, _ = parsed.User.Password()
	}
	server := option.ServerOptions{Server: host, ServerPort: port}
	switch strings.ToLower(parsed.Scheme) {
	case "socks4", "socks4a":
		return option.Outbound{
			Type: C.TypeSOCKS, Tag: outboundTag,
			Options: &option.SOCKSOutboundOptions{ServerOptions: server, Version: "4", Username: username},
		}, nil
	case "socks5", "socks5h":
		return option.Outbound{
			Type: C.TypeSOCKS, Tag: outboundTag,
			Options: &option.SOCKSOutboundOptions{ServerOptions: server, Version: "5", Username: username, Password: password},
		}, nil
	case "http":
		return option.Outbound{
			Type: C.TypeHTTP, Tag: outboundTag,
			Options: &option.HTTPOutboundOptions{ServerOptions: server, Username: username, Password: password},
		}, nil
	case "https":
		return option.Outbound{
			Type: C.TypeHTTP, Tag: outboundTag,
			Options: &option.HTTPOutboundOptions{
				ServerOptions: server, Username: username, Password: password,
				OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
					TLS: &option.OutboundTLSOptions{Enabled: true, ServerName: host},
				},
			},
		}, nil
	default:
		return option.Outbound{}, fmt.Errorf("内部错误: 未知经典协议")
	}
}

func parseShadowsocksLink(raw string) (option.Outbound, error) {
	// ss://method:password@host:port  or  ss://base64(method:password)@host:port  or  ss://base64(method:password@host:port)
	payload := strings.TrimSpace(raw[len("ss://"):])
	if i := strings.IndexAny(payload, "#"); i >= 0 {
		payload = payload[:i]
	}
	var method, password, host string
	var port uint16
	if strings.Contains(payload, "@") {
		// userinfo@host:port — userinfo may be base64
		at := strings.LastIndex(payload, "@")
		userinfo, serverPart := payload[:at], payload[at+1:]
		if decoded, err := decodeShareBase64(userinfo); err == nil && strings.Contains(string(decoded), ":") {
			userinfo = string(decoded)
		} else if unesc, err := url.QueryUnescape(userinfo); err == nil {
			userinfo = unesc
		}
		method, password, _ = strings.Cut(userinfo, ":")
		u, err := url.Parse("ss://" + serverPart)
		if err != nil {
			// host:port only
			h, p, ok := strings.Cut(serverPart, ":")
			if !ok {
				return option.Outbound{}, fmt.Errorf("ss 链接服务器无效")
			}
			host = h
			port, err = parsePort(p, 8388)
			if err != nil {
				return option.Outbound{}, err
			}
		} else {
			host = u.Hostname()
			if host == "" {
				host = strings.Split(serverPart, ":")[0]
			}
			port, err = parsePort(u.Port(), 8388)
			if err != nil {
				// try manual
				if _, p, ok := strings.Cut(serverPart, ":"); ok {
					port, err = parsePort(p, 8388)
				}
				if err != nil {
					return option.Outbound{}, err
				}
			}
		}
	} else {
		decoded, err := decodeShareBase64(payload)
		if err != nil {
			return option.Outbound{}, fmt.Errorf("ss 链接无效: %w", err)
		}
		// method:password@host:port
		text := string(decoded)
		at := strings.LastIndex(text, "@")
		if at < 0 {
			return option.Outbound{}, fmt.Errorf("ss 链接格式无效")
		}
		method, password, _ = strings.Cut(text[:at], ":")
		h, p, ok := strings.Cut(text[at+1:], ":")
		if !ok {
			return option.Outbound{}, fmt.Errorf("ss 链接缺少端口")
		}
		host = h
		port, err = parsePort(p, 8388)
		if err != nil {
			return option.Outbound{}, err
		}
	}
	if method == "" || password == "" || host == "" {
		return option.Outbound{}, fmt.Errorf("ss 链接缺少 method/password/host")
	}
	return option.Outbound{
		Type: C.TypeShadowsocks, Tag: outboundTag,
		Options: &option.ShadowsocksOutboundOptions{
			ServerOptions: option.ServerOptions{Server: host, ServerPort: port},
			Method:        method,
			Password:      password,
		},
	}, nil
}

func parseVMessLink(raw string) (option.Outbound, error) {
	payload := strings.TrimSpace(raw[len("vmess://"):])
	payload = strings.TrimSuffix(payload, "/")
	decoded, err := decodeShareBase64(payload)
	if err != nil {
		return option.Outbound{}, fmt.Errorf("vmess 链接 base64 无效: %w", err)
	}
	var cfg struct {
		Add  string      `json:"add"`
		Port json.Number `json:"port"`
		ID   string      `json:"id"`
		Aid  json.Number `json:"aid"`
		Scy  string      `json:"scy"`
		Net  string      `json:"net"`
		Type string      `json:"type"`
		Host string      `json:"host"`
		Path string      `json:"path"`
		TLS  string      `json:"tls"`
		SNI  string      `json:"sni"`
		ALPN string      `json:"alpn"`
		FP   string      `json:"fp"`
	}
	if err := json.Unmarshal(decoded, &cfg); err != nil {
		return option.Outbound{}, fmt.Errorf("vmess 链接 JSON 无效: %w", err)
	}
	if strings.TrimSpace(cfg.Add) == "" || strings.TrimSpace(cfg.ID) == "" {
		return option.Outbound{}, fmt.Errorf("vmess 链接缺少服务器或 UUID")
	}
	port, err := parsePort(cfg.Port.String(), 443)
	if err != nil {
		return option.Outbound{}, err
	}
	alterID := 0
	if cfg.Aid.String() != "" {
		if n, convErr := strconv.Atoi(cfg.Aid.String()); convErr == nil {
			alterID = n
		}
	}
	security := strings.TrimSpace(cfg.Scy)
	if security == "" {
		security = "auto"
	}
	opts := &option.VMessOutboundOptions{
		ServerOptions: option.ServerOptions{Server: cfg.Add, ServerPort: port},
		UUID:          strings.TrimSpace(cfg.ID),
		Security:      security,
		AlterId:       alterID,
	}
	transport, err := v2rayTransport(cfg.Net, cfg.Type, cfg.Host, cfg.Path, "")
	if err != nil {
		return option.Outbound{}, err
	}
	opts.Transport = transport
	if tlsEnabled(cfg.TLS) {
		opts.TLS = buildTLS(firstNonEmpty(cfg.SNI, cfg.Host, cfg.Add), cfg.ALPN, cfg.FP, false)
	}
	return option.Outbound{Type: C.TypeVMess, Tag: outboundTag, Options: opts}, nil
}

func parseVLESSLink(parsed *url.URL) (option.Outbound, error) {
	host := parsed.Hostname()
	uuid := ""
	if parsed.User != nil {
		uuid = parsed.User.Username()
	}
	if host == "" || uuid == "" {
		return option.Outbound{}, fmt.Errorf("vless 链接缺少服务器或 UUID")
	}
	port, err := parsePort(parsed.Port(), 443)
	if err != nil {
		return option.Outbound{}, err
	}
	q := parsed.Query()
	opts := &option.VLESSOutboundOptions{
		ServerOptions: option.ServerOptions{Server: host, ServerPort: port},
		UUID:          uuid,
		Flow:          q.Get("flow"),
	}
	network := firstNonEmpty(q.Get("type"), q.Get("net"), "tcp")
	transport, err := v2rayTransport(network, q.Get("headerType"), firstNonEmpty(q.Get("host"), q.Get("sni")), q.Get("path"), q.Get("serviceName"))
	if err != nil {
		return option.Outbound{}, err
	}
	opts.Transport = transport
	security := strings.ToLower(firstNonEmpty(q.Get("security"), q.Get("tls")))
	if security == "tls" || security == "reality" {
		insecure := q.Get("allowInsecure") == "1" || q.Get("insecure") == "1"
		tls := buildTLS(firstNonEmpty(q.Get("sni"), q.Get("host"), host), q.Get("alpn"), q.Get("fp"), insecure)
		if security == "reality" {
			tls.Reality = &option.OutboundRealityOptions{
				Enabled:   true,
				PublicKey: q.Get("pbk"),
				ShortID:   q.Get("sid"),
			}
			if tls.UTLS == nil && q.Get("fp") != "" {
				tls.UTLS = &option.OutboundUTLSOptions{Enabled: true, Fingerprint: q.Get("fp")}
			}
		}
		opts.TLS = tls
	}
	return option.Outbound{Type: C.TypeVLESS, Tag: outboundTag, Options: opts}, nil
}

func parseTrojanLink(parsed *url.URL) (option.Outbound, error) {
	host := parsed.Hostname()
	password := ""
	if parsed.User != nil {
		password = parsed.User.Username()
	}
	if host == "" || password == "" {
		return option.Outbound{}, fmt.Errorf("trojan 链接缺少服务器或密码")
	}
	port, err := parsePort(parsed.Port(), 443)
	if err != nil {
		return option.Outbound{}, err
	}
	q := parsed.Query()
	opts := &option.TrojanOutboundOptions{
		ServerOptions: option.ServerOptions{Server: host, ServerPort: port},
		Password:      password,
	}
	network := firstNonEmpty(q.Get("type"), q.Get("net"), "tcp")
	transport, err := v2rayTransport(network, q.Get("headerType"), firstNonEmpty(q.Get("host"), q.Get("sni")), q.Get("path"), q.Get("serviceName"))
	if err != nil {
		return option.Outbound{}, err
	}
	opts.Transport = transport
	insecure := q.Get("allowInsecure") == "1" || q.Get("insecure") == "1"
	// Trojan almost always uses TLS
	opts.TLS = buildTLS(firstNonEmpty(q.Get("sni"), q.Get("peer"), host), q.Get("alpn"), q.Get("fp"), insecure)
	return option.Outbound{Type: C.TypeTrojan, Tag: outboundTag, Options: opts}, nil
}

func parseHysteria2Link(parsed *url.URL) (option.Outbound, error) {
	host := parsed.Hostname()
	if host == "" {
		return option.Outbound{}, fmt.Errorf("hysteria2 链接缺少服务器")
	}
	port, err := parsePort(parsed.Port(), 443)
	if err != nil {
		return option.Outbound{}, err
	}
	password := ""
	if parsed.User != nil {
		password = parsed.User.Username()
		if pass, ok := parsed.User.Password(); ok && pass != "" {
			if password != "" {
				password = password + ":" + pass
			} else {
				password = pass
			}
		}
	}
	q := parsed.Query()
	if password == "" {
		password = firstNonEmpty(q.Get("password"), q.Get("auth"))
	}
	opts := &option.Hysteria2OutboundOptions{
		ServerOptions: option.ServerOptions{Server: host, ServerPort: port},
		Password:      password,
		UpMbps:        atoiDefault(q.Get("upmbps"), 0),
		DownMbps:      atoiDefault(q.Get("downmbps"), 0),
	}
	if obfs := firstNonEmpty(q.Get("obfs"), q.Get("obfsType")); obfs != "" {
		opts.Obfs = &option.Hysteria2Obfs{
			Type:     obfs,
			Password: firstNonEmpty(q.Get("obfs-password"), q.Get("obfsPassword"), q.Get("obfs_password")),
		}
	}
	insecure := q.Get("insecure") == "1" || q.Get("allowInsecure") == "1"
	opts.TLS = buildTLS(firstNonEmpty(q.Get("sni"), q.Get("peer"), host), firstNonEmpty(q.Get("alpn"), "h3"), q.Get("fp"), insecure)
	return option.Outbound{Type: C.TypeHysteria2, Tag: outboundTag, Options: opts}, nil
}

func parseHysteriaLink(parsed *url.URL) (option.Outbound, error) {
	host := parsed.Hostname()
	if host == "" {
		return option.Outbound{}, fmt.Errorf("hysteria 链接缺少服务器")
	}
	port, err := parsePort(parsed.Port(), 443)
	if err != nil {
		return option.Outbound{}, err
	}
	q := parsed.Query()
	auth := ""
	if parsed.User != nil {
		auth = parsed.User.Username()
	}
	auth = firstNonEmpty(auth, q.Get("auth"), q.Get("auth_str"), q.Get("password"))
	opts := &option.HysteriaOutboundOptions{
		ServerOptions: option.ServerOptions{Server: host, ServerPort: port},
		AuthString:    auth,
		UpMbps:        atoiDefault(firstNonEmpty(q.Get("upmbps"), q.Get("up")), 0),
		DownMbps:      atoiDefault(firstNonEmpty(q.Get("downmbps"), q.Get("down")), 0),
		Obfs:          q.Get("obfs"),
	}
	insecure := q.Get("insecure") == "1" || q.Get("allowInsecure") == "1"
	opts.TLS = buildTLS(firstNonEmpty(q.Get("peer"), q.Get("sni"), host), firstNonEmpty(q.Get("alpn"), "h3"), q.Get("fp"), insecure)
	return option.Outbound{Type: C.TypeHysteria, Tag: outboundTag, Options: opts}, nil
}

func parseTUICLink(parsed *url.URL) (option.Outbound, error) {
	// tuic://uuid:password@host:port?sni=...&alpn=h3&congestion_control=bbr
	host := parsed.Hostname()
	uuid, password := "", ""
	if parsed.User != nil {
		uuid = parsed.User.Username()
		password, _ = parsed.User.Password()
	}
	if host == "" || uuid == "" {
		return option.Outbound{}, fmt.Errorf("tuic 链接缺少服务器或 UUID")
	}
	port, err := parsePort(parsed.Port(), 443)
	if err != nil {
		return option.Outbound{}, err
	}
	q := parsed.Query()
	if password == "" {
		password = q.Get("password")
	}
	opts := &option.TUICOutboundOptions{
		ServerOptions:     option.ServerOptions{Server: host, ServerPort: port},
		UUID:              uuid,
		Password:          password,
		CongestionControl: firstNonEmpty(q.Get("congestion_control"), q.Get("congestion-control")),
		UDPRelayMode:      firstNonEmpty(q.Get("udp_relay_mode"), q.Get("udp-relay-mode")),
		ZeroRTTHandshake:  q.Get("zero_rtt_handshake") == "1" || q.Get("zero-rtt-handshake") == "1",
	}
	insecure := q.Get("insecure") == "1" || q.Get("allowInsecure") == "1"
	opts.TLS = buildTLS(firstNonEmpty(q.Get("sni"), host), firstNonEmpty(q.Get("alpn"), "h3"), q.Get("fp"), insecure)
	return option.Outbound{Type: C.TypeTUIC, Tag: outboundTag, Options: opts}, nil
}

func parseAnyTLSLink(parsed *url.URL) (option.Outbound, error) {
	host := parsed.Hostname()
	password := ""
	if parsed.User != nil {
		password = parsed.User.Username()
	}
	if host == "" || password == "" {
		return option.Outbound{}, fmt.Errorf("anytls 链接缺少服务器或密码")
	}
	port, err := parsePort(parsed.Port(), 443)
	if err != nil {
		return option.Outbound{}, err
	}
	q := parsed.Query()
	opts := &option.AnyTLSOutboundOptions{
		ServerOptions: option.ServerOptions{Server: host, ServerPort: port},
		Password:      password,
	}
	insecure := q.Get("insecure") == "1" || q.Get("allowInsecure") == "1"
	opts.TLS = buildTLS(firstNonEmpty(q.Get("sni"), host), q.Get("alpn"), q.Get("fp"), insecure)
	return option.Outbound{Type: C.TypeAnyTLS, Tag: outboundTag, Options: opts}, nil
}

func parseSSHLink(parsed *url.URL) (option.Outbound, error) {
	host := parsed.Hostname()
	if host == "" {
		return option.Outbound{}, fmt.Errorf("ssh 链接缺少服务器")
	}
	port, err := parsePort(parsed.Port(), 22)
	if err != nil {
		return option.Outbound{}, err
	}
	user := "root"
	password := ""
	if parsed.User != nil {
		if u := parsed.User.Username(); u != "" {
			user = u
		}
		password, _ = parsed.User.Password()
	}
	q := parsed.Query()
	if password == "" {
		password = q.Get("password")
	}
	opts := &option.SSHOutboundOptions{
		ServerOptions:  option.ServerOptions{Server: host, ServerPort: port},
		User:           firstNonEmpty(q.Get("user"), user),
		Password:       password,
		PrivateKeyPath: q.Get("private_key_path"),
	}
	if key := q.Get("private_key"); key != "" {
		opts.PrivateKey = badoption.Listable[string]{key}
	}
	return option.Outbound{Type: C.TypeSSH, Tag: outboundTag, Options: opts}, nil
}

func parseWireGuardLink(parsed *url.URL) (option.Outbound, error) {
	// Prefer JSON for wireguard; URL form: wireguard://privatekey@host:port?publickey=...&address=...&allowed_ips=...
	host := parsed.Hostname()
	privateKey := ""
	if parsed.User != nil {
		privateKey = parsed.User.Username()
	}
	if host == "" || privateKey == "" {
		return option.Outbound{}, fmt.Errorf("wireguard 链接缺少服务器或私钥；复杂配置请粘贴 sing-box outbound JSON")
	}
	port, err := parsePort(parsed.Port(), 51820)
	if err != nil {
		return option.Outbound{}, err
	}
	q := parsed.Query()
	// WireGuard is endpoint-based in recent sing-box; use legacy outbound if available.
	opts := &option.LegacyWireGuardOutboundOptions{
		ServerOptions: option.ServerOptions{Server: host, ServerPort: port},
		PrivateKey:    privateKey,
		PeerPublicKey: firstNonEmpty(q.Get("publickey"), q.Get("public_key"), q.Get("peer_public_key")),
		PreSharedKey:  firstNonEmpty(q.Get("presharedkey"), q.Get("pre_shared_key")),
	}
	if addr := q.Get("address"); addr != "" {
		prefix, err := netip.ParsePrefix(addr)
		if err != nil {
			// allow bare IP → /32 or /128
			if ip, ipErr := netip.ParseAddr(addr); ipErr == nil {
				if ip.Is4() {
					prefix = netip.PrefixFrom(ip, 32)
				} else {
					prefix = netip.PrefixFrom(ip, 128)
				}
			} else {
				return option.Outbound{}, fmt.Errorf("wireguard address 无效: %w", err)
			}
		}
		opts.LocalAddress = badoption.Listable[netip.Prefix]{prefix}
	}
	return option.Outbound{Type: C.TypeWireGuard, Tag: outboundTag, Options: opts}, nil
}

func parseShadowTLSLink(parsed *url.URL) (option.Outbound, error) {
	host := parsed.Hostname()
	password := ""
	if parsed.User != nil {
		password = parsed.User.Username()
	}
	if host == "" {
		return option.Outbound{}, fmt.Errorf("shadowtls 链接缺少服务器")
	}
	port, err := parsePort(parsed.Port(), 443)
	if err != nil {
		return option.Outbound{}, err
	}
	q := parsed.Query()
	if password == "" {
		password = q.Get("password")
	}
	version := atoiDefault(q.Get("version"), 3)
	opts := &option.ShadowTLSOutboundOptions{
		ServerOptions: option.ServerOptions{Server: host, ServerPort: port},
		Version:       version,
		Password:      password,
	}
	if sni := firstNonEmpty(q.Get("sni"), q.Get("host")); sni != "" {
		opts.TLS = &option.OutboundTLSOptions{Enabled: true, ServerName: sni}
	}
	return option.Outbound{Type: C.TypeShadowTLS, Tag: outboundTag, Options: opts}, nil
}

func v2rayTransport(network, headerType, host, path, serviceName string) (*option.V2RayTransportOptions, error) {
	network = strings.ToLower(strings.TrimSpace(network))
	if network == "" || network == "tcp" {
		return nil, nil
	}
	path, _ = url.PathUnescape(path)
	host, _ = url.QueryUnescape(host)
	switch network {
	case "ws", "websocket":
		headers := badoption.HTTPHeader{}
		if host != "" {
			headers["Host"] = []string{host}
		}
		return &option.V2RayTransportOptions{
			Type: C.V2RayTransportTypeWebsocket,
			WebsocketOptions: option.V2RayWebsocketOptions{
				Path:    path,
				Headers: headers,
			},
		}, nil
	case "http", "h2":
		hosts := badoption.Listable[string]{}
		if host != "" {
			hosts = append(hosts, host)
		}
		return &option.V2RayTransportOptions{
			Type: C.V2RayTransportTypeHTTP,
			HTTPOptions: option.V2RayHTTPOptions{
				Host: hosts,
				Path: path,
			},
		}, nil
	case "grpc":
		return &option.V2RayTransportOptions{
			Type: C.V2RayTransportTypeGRPC,
			GRPCOptions: option.V2RayGRPCOptions{
				ServiceName: firstNonEmpty(serviceName, path),
			},
		}, nil
	case "httpupgrade":
		return &option.V2RayTransportOptions{
			Type: C.V2RayTransportTypeHTTPUpgrade,
			HTTPUpgradeOptions: option.V2RayHTTPUpgradeOptions{
				Host: host,
				Path: path,
			},
		}, nil
	case "quic":
		return &option.V2RayTransportOptions{Type: C.V2RayTransportTypeQUIC}, nil
	default:
		if headerType != "" && network == "tcp" {
			return nil, nil
		}
		return nil, fmt.Errorf("暂不支持传输类型 %q", network)
	}
}

func buildTLS(serverName, alpn, fingerprint string, insecure bool) *option.OutboundTLSOptions {
	tls := &option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: serverName,
		Insecure:   insecure,
	}
	if alpn != "" {
		parts := strings.Split(alpn, ",")
		list := make(badoption.Listable[string], 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				list = append(list, part)
			}
		}
		if len(list) > 0 {
			tls.ALPN = list
		}
	}
	if fingerprint != "" {
		tls.UTLS = &option.OutboundUTLSOptions{Enabled: true, Fingerprint: fingerprint}
	}
	return tls
}

func decodeShareBase64(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if i := strings.IndexAny(value, "#?"); i >= 0 {
		value = value[:i]
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var last error
	for _, enc := range encodings {
		decoded, err := enc.DecodeString(value)
		if err == nil {
			return decoded, nil
		}
		last = err
	}
	return nil, last
}

func parsePort(raw string, fallback uint16) (uint16, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("代理端口无效: %s", raw)
	}
	return uint16(n), nil
}

func defaultPort(scheme string) uint16 {
	switch strings.ToLower(scheme) {
	case "http":
		return 80
	case "https", "vmess", "vless", "trojan", "hysteria", "hysteria2", "hy", "hy2", "tuic", "anytls", "shadowtls":
		return 443
	case "ssh":
		return 22
	case "wireguard", "wg":
		return 51820
	default:
		return 1080
	}
}

func tlsEnabled(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "tls" || value == "1" || value == "true" || value == "reality"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func atoiDefault(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}
