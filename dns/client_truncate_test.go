package dns

import (
	"bytes"
	"net/netip"
	"testing"

	mDNS "github.com/miekg/dns"
)

func testTruncateRequest() *mDNS.Msg {
	request := new(mDNS.Msg)
	request.SetQuestion("cache.example.", mDNS.TypeA)
	return request
}

func TestTruncateDNSMessageHeadroom(t *testing.T) {
	request := testTruncateRequest()
	response := testAResponse(request, 300)
	expected, err := response.Pack()
	if err != nil {
		t.Fatal(err)
	}

	for _, headroom := range [][2]int{{0, 0}, {1024, 1024}, {12, 128}} {
		front, rear := headroom[0], headroom[1]
		buffer, err := TruncateDNSMessage(request, response, front, rear)
		if err != nil {
			t.Fatalf("front=%d rear=%d: %v", front, rear, err)
		}
		if start := buffer.Start(); start != front {
			t.Fatalf("front=%d rear=%d: buffer start = %d, want %d", front, rear, start, front)
		}
		if !bytes.Equal(buffer.Bytes(), expected) {
			t.Fatalf("front=%d rear=%d: packed message differs from response", front, rear)
		}
		trailing := buffer.RawCap() - (buffer.Start() + buffer.Len())
		if trailing < rear {
			t.Fatalf("front=%d rear=%d: trailing headroom = %d, want >= %d", front, rear, trailing, rear)
		}
		buffer.Release()
	}
}

func TestTruncateDNSMessageTruncatesOversize(t *testing.T) {
	request := testTruncateRequest()
	response := testAResponse(request, 300)
	for i := 0; i < 100; i++ {
		response.Answer = append(response.Answer, &mDNS.A{
			Hdr: mDNS.RR_Header{
				Name:   response.Question[0].Name,
				Rrtype: mDNS.TypeA,
				Class:  mDNS.ClassINET,
				Ttl:    300,
			},
			A: netip.MustParseAddr("192.0.2.2").AsSlice(),
		})
	}

	buffer, err := TruncateDNSMessage(request, response, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer buffer.Release()
	if l := buffer.Len(); l > 512 {
		t.Fatalf("truncated message length = %d, want <= 512", l)
	}
	unpacked := new(mDNS.Msg)
	if err := unpacked.Unpack(buffer.Bytes()); err != nil {
		t.Fatalf("truncated message does not unpack: %v", err)
	}
	if !unpacked.Truncated {
		t.Fatal("truncated message does not have the TC bit set")
	}
}
