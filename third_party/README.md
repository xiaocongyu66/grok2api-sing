# third_party

## sing-box

完整迁入的 [SagerNet/sing-box](https://github.com/SagerNet/sing-box) 源码树（tag `v1.12.12`），**不做精简**。

- 模块路径仍为 `github.com/sagernet/sing-box`
- `backend/go.mod`：`replace github.com/sagernet/sing-box => ../third_party/sing-box`
- 许可证见 `sing-box/LICENSE`
- grok2api 以 **进程内库** 方式调用：注册 **全部 outbound 协议** + `box.New` 拨号，不另起 sing-box 进程，不启本地 mixed/tun 入站
- 协议覆盖（默认注册）：HTTP/SOCKS、Shadowsocks、VMess、VLESS、Trojan、SSH、ShadowTLS、AnyTLS  
- 可选 build tags：`with_quic`（Hysteria/Hysteria2/TUIC）、`with_wireguard`（WireGuard）、`with_utls`（Reality/uTLS 指纹）、`with_tor`（Tor）

推荐构建标签（对齐上游 Makefile 客户端能力）：

```
with_gvisor,with_quic,with_wireguard,with_utls
```

Docker 默认 `SINGBOX_TAGS=with_gvisor,with_quic,with_wireguard,with_utls`（见根目录 Dockerfile）。
