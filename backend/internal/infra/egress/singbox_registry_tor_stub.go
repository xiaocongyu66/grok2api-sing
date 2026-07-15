//go:build !with_tor

package egress

import "github.com/sagernet/sing-box/adapter/outbound"

func registerTorOutbound(*outbound.Registry) {}
