package egress

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/experimental/libbox/platform"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
)

// proxyDialer is a process-local sing-box outbound used as net.Dialer.
// No separate sing-box process or local mixed inbound is started.
type proxyDialer struct {
	instance *box.Box
	tag      string
	close    func()
}

func (d *proxyDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if d == nil || d.instance == nil {
		return nil, fmt.Errorf("sing-box 出口未初始化")
	}
	out, ok := d.instance.Outbound().Outbound(d.tag)
	if !ok {
		return nil, fmt.Errorf("sing-box outbound %q 不存在", d.tag)
	}
	destination := M.ParseSocksaddr(address)
	if !destination.IsValid() {
		return nil, fmt.Errorf("无效目标地址: %s", address)
	}
	return out.DialContext(ctx, network, destination)
}

func (d *proxyDialer) Close() {
	if d != nil && d.close != nil {
		d.close()
		d.close = nil
	}
}

// openProxyDialer embeds the vendored sing-box tree in-process for one proxy
// URL / share link / outbound JSON. All outbound protocols registered in
// newOutboundRegistry are available (build tags enable QUIC/WireGuard extras).
func openProxyDialer(proxyURL string) (*proxyDialer, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return nil, nil
	}
	outboundOption, err := outboundFromProxyURL(proxyURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = newProxyBoxContext(ctx)
	// Avoid host netlink interface monitors hanging under restricted environments.
	ctx = service.ContextWith[platform.Interface](ctx, stubPlatform{})
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: option.Options{
			Log:       &option.LogOptions{Disabled: true, Level: "error"},
			Outbounds: []option.Outbound{outboundOption},
		},
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("创建内置 sing-box: %w", err)
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()
		cancel()
		return nil, fmt.Errorf("启动内置 sing-box: %w", err)
	}
	var once sync.Once
	return &proxyDialer{
		instance: instance,
		tag:      outboundTag,
		close: func() {
			once.Do(func() {
				_ = instance.Close()
				cancel()
			})
		},
	}, nil
}
