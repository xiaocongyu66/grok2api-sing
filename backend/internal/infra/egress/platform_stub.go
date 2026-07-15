package egress

import (
	"context"
	"net/netip"
	"os"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/process"
	"github.com/sagernet/sing-box/experimental/libbox/platform"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/control"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/common/x/list"
	tun "github.com/sagernet/sing-tun"
)

// stubPlatform disables host netlink/interface monitors inside embedded sing-box.
// Outbound-only mode never needs TUN or default-route tracking; enabling the real
// monitors can hang under restricted / container environments.
type stubPlatform struct{}

func (stubPlatform) Initialize(adapter.NetworkManager) error { return nil }
func (stubPlatform) UsePlatformAutoDetectInterfaceControl() bool {
	return true
}
func (stubPlatform) AutoDetectInterfaceControl(int) error { return nil }
func (stubPlatform) OpenTun(*tun.Options, option.TunPlatformOptions) (tun.Tun, error) {
	return nil, os.ErrInvalid
}
func (stubPlatform) CreateDefaultInterfaceMonitor(logger.Logger) tun.DefaultInterfaceMonitor {
	return stubInterfaceMonitor{}
}
func (stubPlatform) Interfaces() ([]adapter.NetworkInterface, error) { return nil, nil }
func (stubPlatform) UnderNetworkExtension() bool                     { return false }
func (stubPlatform) IncludeAllNetworks() bool                        { return false }
func (stubPlatform) ClearDNSCache()                                  {}
func (stubPlatform) ReadWIFIState() adapter.WIFIState                { return adapter.WIFIState{} }
func (stubPlatform) SystemCertificates() []string                    { return nil }
func (stubPlatform) FindProcessInfo(context.Context, string, netip.AddrPort, netip.AddrPort) (*process.Info, error) {
	return nil, process.ErrNotFound
}
func (stubPlatform) SendNotification(*platform.Notification) error { return nil }

type stubInterfaceMonitor struct{}

func (stubInterfaceMonitor) Start() error { return nil }
func (stubInterfaceMonitor) Close() error { return nil }
func (stubInterfaceMonitor) DefaultInterface() *control.Interface {
	return nil
}
func (stubInterfaceMonitor) OverrideAndroidVPN() bool { return false }
func (stubInterfaceMonitor) AndroidVPNEnabled() bool  { return false }
func (stubInterfaceMonitor) RegisterCallback(tun.DefaultInterfaceUpdateCallback) *list.Element[tun.DefaultInterfaceUpdateCallback] {
	return nil
}
func (stubInterfaceMonitor) UnregisterCallback(*list.Element[tun.DefaultInterfaceUpdateCallback]) {
}
func (stubInterfaceMonitor) RegisterMyInterface(string) {}
func (stubInterfaceMonitor) MyInterface() string        { return "" }

var _ platform.Interface = stubPlatform{}
