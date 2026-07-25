//go:build with_quic

package v2rayquic

import "github.com/Miku0139oao/sidera-core/transport/v2ray"

func init() {
	v2ray.RegisterQUICConstructor(NewServer, NewClient)
}
