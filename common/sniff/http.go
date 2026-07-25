package sniff

import (
	std_bufio "bufio"
	"context"
	"errors"
	"io"

	"github.com/Miku0139oao/sidera-core/adapter"
	C "github.com/Miku0139oao/sidera-core/constant"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/protocol/http"
)

func HTTPHost(_ context.Context, metadata *adapter.InboundContext, reader io.Reader) error {
	request, err := http.ReadRequest(std_bufio.NewReader(reader))
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return E.Cause1(ErrNeedMoreData, err)
		} else {
			return err
		}
	}
	metadata.Protocol = C.ProtocolHTTP
	metadata.Domain = M.ParseSocksaddr(request.Host).AddrString()
	return nil
}
