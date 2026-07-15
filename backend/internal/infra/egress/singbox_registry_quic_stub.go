//go:build !with_quic

package egress

import (
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/dns"
)

func registerQUICOutbounds(*outbound.Registry) {}

func registerQUICDNSTransports(*dns.TransportRegistry) {}
