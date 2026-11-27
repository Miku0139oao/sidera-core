package fakeip

import (
	"context"
	"net/netip"
	"strings"
	"testing"

	"github.com/sagernet/sing/common/logger"

	mDNS "github.com/miekg/dns"
)

func newTestTransport(t *testing.T) *Transport {
	t.Helper()
	store := NewStore(context.Background(), logger.NOP(), netip.MustParsePrefix("198.18.0.0/15"), netip.Prefix{})
	if err := store.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return &Transport{
		store:        store,
		inet4Enabled: true,
		inet6Enabled: false,
	}
}

func fakeIPQuery(name string) *mDNS.Msg {
	return &mDNS.Msg{
		MsgHdr: mDNS.MsgHdr{Id: 1},
		Question: []mDNS.Question{{
			Name:  name,
			Qtype: mDNS.TypeA,
		}},
	}
}

func TestExchangeRejectsOversizedDomain(t *testing.T) {
	transport := newTestTransport(t)
	// FqdnToDomain strips the trailing dot, so the resulting domain is 256 bytes.
	name := strings.Repeat("a", 256) + "."
	_, err := transport.Exchange(context.Background(), fakeIPQuery(name))
	if err == nil {
		t.Fatal("expected error for domain longer than 255 bytes")
	}
}

func TestExchangeAcceptsMaximumLengthDomain(t *testing.T) {
	transport := newTestTransport(t)
	// Exactly 255 bytes after FqdnToDomain strips the trailing dot.
	name := strings.Repeat("a", 255) + "."
	response, err := transport.Exchange(context.Background(), fakeIPQuery(name))
	if err != nil {
		t.Fatalf("expected domain of exactly 255 bytes to be accepted: %v", err)
	}
	if response == nil || len(response.Answer) != 1 {
		t.Fatal("expected a fakeip answer")
	}
}
