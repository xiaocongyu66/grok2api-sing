//go:build !with_wireguard

package egress

import (
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/outbound"
)

func registerWireGuardOutbound(*outbound.Registry) {}

func newEndpointRegistry() *endpoint.Registry {
	return endpoint.NewRegistry()
}
