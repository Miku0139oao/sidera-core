package dns

import (
	"context"
	"testing"

	"github.com/sagernet/sing/common/logger"

	mDNS "github.com/miekg/dns"
)

func TestIsResolverDiscoveryQuery(t *testing.T) {
	testCases := []struct {
		name     string
		question mDNS.Question
		want     bool
	}{
		{"resolver discovery", mDNS.Question{Name: "_dns.example.com.", Qtype: mDNS.TypeSVCB, Qclass: mDNS.ClassINET}, true},
		{"case insensitive", mDNS.Question{Name: "_DNS.example.com.", Qtype: mDNS.TypeSVCB, Qclass: mDNS.ClassINET}, true},
		{"too short", mDNS.Question{Name: "_dns.", Qtype: mDNS.TypeSVCB, Qclass: mDNS.ClassINET}, false},
		{"not svcb", mDNS.Question{Name: "_dns.example.com.", Qtype: mDNS.TypeHTTPS, Qclass: mDNS.ClassINET}, false},
		{"not underscore prefix", mDNS.Question{Name: "x_dns.example.com.", Qtype: mDNS.TypeSVCB, Qclass: mDNS.ClassINET}, false},
	}
	for _, testCase := range testCases {
		if got := isResolverDiscoveryQuery(testCase.question); got != testCase.want {
			t.Errorf("%s: isResolverDiscoveryQuery() = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

func TestPrepareExchangeRejectsResolverDiscovery(t *testing.T) {
	router := &Router{logger: logger.NOP()}
	message := new(mDNS.Msg)
	message.SetQuestion("_dns.example.com.", mDNS.TypeSVCB)

	exchangeCtx, response, err := router.prepareExchange(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if exchangeCtx != nil {
		t.Fatal("resolver discovery query must not be forwarded upstream")
	}
	if response == nil {
		t.Fatal("resolver discovery query did not get an early response")
	}
	if !response.Response {
		t.Fatal("early response is not marked as a response")
	}
	if response.Rcode != mDNS.RcodeSuccess {
		t.Fatalf("early response rcode = %d, want success", response.Rcode)
	}
	if response.Id != message.Id {
		t.Fatalf("early response id = %d, want %d", response.Id, message.Id)
	}
	if response.RecursionDesired != message.RecursionDesired {
		t.Fatalf("early response recursion desired = %v, want %v", response.RecursionDesired, message.RecursionDesired)
	}
	if !response.RecursionAvailable {
		t.Fatal("early response is not recursion available")
	}
	if len(response.Question) != 1 || response.Question[0] != message.Question[0] {
		t.Fatalf("early response question = %v, want %v", response.Question, message.Question)
	}
}

func TestPrepareExchangeForwardsNormalQuery(t *testing.T) {
	router := &Router{logger: logger.NOP()}
	message := new(mDNS.Msg)
	message.SetQuestion("example.com.", mDNS.TypeA)

	exchangeCtx, response, err := router.prepareExchange(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if exchangeCtx == nil {
		t.Fatal("normal query was not forwarded")
	}
	if response != nil {
		t.Fatalf("normal query got an unexpected early response: %v", response)
	}
}
