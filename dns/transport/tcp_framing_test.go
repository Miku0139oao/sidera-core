package transport

import (
	"bytes"
	"encoding/binary"
	"testing"

	mDNS "github.com/miekg/dns"
)

func TestWriteMessageUsesCompressedWireLength(t *testing.T) {
	message := new(mDNS.Msg)
	message.SetQuestion("www.example.com.", mDNS.TypeA)
	for range 8 {
		message.Answer = append(message.Answer, &mDNS.CNAME{
			Hdr:    mDNS.RR_Header{Name: "www.example.com.", Rrtype: mDNS.TypeCNAME, Class: mDNS.ClassINET, Ttl: 60},
			Target: "target.example.com.",
		})
	}

	var wire bytes.Buffer
	if err := WriteMessage(&wire, 42, message); err != nil {
		t.Fatal(err)
	}
	payload := wire.Bytes()
	if len(payload) < 2 {
		t.Fatal("missing TCP DNS length prefix")
	}
	framedLength := int(binary.BigEndian.Uint16(payload[:2]))
	if framedLength != len(payload)-2 {
		t.Fatalf("length prefix = %d, packed payload = %d", framedLength, len(payload)-2)
	}
	if framedLength >= message.Len() {
		t.Fatalf("test message did not compress: packed=%d uncompressed=%d", framedLength, message.Len())
	}
	var decoded mDNS.Msg
	if err := decoded.Unpack(payload[2:]); err != nil {
		t.Fatal(err)
	}
	if decoded.Id != 42 || len(decoded.Question) != 1 || len(decoded.Answer) != len(message.Answer) {
		t.Fatalf("unexpected decoded message: id=%d questions=%d answers=%d", decoded.Id, len(decoded.Question), len(decoded.Answer))
	}
}

func TestValidateDNSResponse(t *testing.T) {
	query := new(mDNS.Msg)
	query.SetQuestion("Example.COM.", mDNS.TypeAAAA)
	response := new(mDNS.Msg)
	response.SetReply(query)
	response.Question[0].Name = "example.com."
	if err := validateDNSResponse(query, response); err != nil {
		t.Fatal(err)
	}

	response.Response = false
	if err := validateDNSResponse(query, response); err == nil {
		t.Fatal("accepted a DNS query as a response")
	}
	response.Response = true
	response.Question[0].Qtype = mDNS.TypeA
	if err := validateDNSResponse(query, response); err == nil {
		t.Fatal("accepted a response for a different question")
	}

	refused := new(mDNS.Msg)
	refused.Response = true
	refused.Opcode = query.Opcode
	refused.Rcode = mDNS.RcodeRefused
	if err := validateDNSResponse(query, refused); err != nil {
		t.Fatalf("rejected a header-only error response: %v", err)
	}
}

func TestMismatchedDNSResponseDoesNotConsumePendingQuery(t *testing.T) {
	query := new(mDNS.Msg)
	query.Id = 99
	query.SetQuestion("expected.example.", mDNS.TypeA)
	callback := make(chan *mDNS.Msg, 1)
	multiplexer := newQueryMultiplexer(queryMultiplexerOptions{})
	multiplexer.queries[7] = &pendingQuery{
		message:     query,
		callback:    func(response *mDNS.Msg, _ error) { callback <- response },
		stopContext: func() bool { return true },
		stopConn:    func() bool { return true },
	}

	wrong := new(mDNS.Msg)
	wrong.SetReply(query)
	wrong.Question[0].Name = "wrong.example."
	multiplexer.complete(7, wrong, nil, false)
	if len(multiplexer.queries) != 1 {
		t.Fatal("mismatched response consumed the pending query")
	}
	select {
	case <-callback:
		t.Fatal("mismatched response invoked the callback")
	default:
	}

	correct := new(mDNS.Msg)
	correct.SetReply(query)
	multiplexer.complete(7, correct, nil, false)
	if len(multiplexer.queries) != 0 {
		t.Fatal("matching response did not consume the pending query")
	}
	if response := <-callback; response.Id != query.Id {
		t.Fatalf("restored response ID = %d, want %d", response.Id, query.Id)
	}
}
