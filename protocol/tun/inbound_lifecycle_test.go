package tun

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/log"
	stun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/x/list"

	"github.com/stretchr/testify/require"
	"go4.org/netipx"
)

type tunLifecycleRuleSet struct {
	name      string
	ipSets    []*netipx.IPSet
	refs      atomic.Int32
	access    sync.Mutex
	callbacks list.List[adapter.RuleSetUpdateCallback]
}

func newTUNLifecycleRuleSet(t *testing.T, name string, prefix string) *tunLifecycleRuleSet {
	t.Helper()
	var builder netipx.IPSetBuilder
	builder.AddPrefix(netip.MustParsePrefix(prefix))
	ipSet, err := builder.IPSet()
	require.NoError(t, err)
	return &tunLifecycleRuleSet{name: name, ipSets: []*netipx.IPSet{ipSet}}
}

func (s *tunLifecycleRuleSet) Name() string { return s.name }
func (s *tunLifecycleRuleSet) StartContext(context.Context, *adapter.HTTPStartContext) error {
	return nil
}

func (s *tunLifecycleRuleSet) Metadata() adapter.RuleSetMetadata { return adapter.RuleSetMetadata{} }
func (s *tunLifecycleRuleSet) ExtractIPSet() []*netipx.IPSet     { return s.ipSets }
func (s *tunLifecycleRuleSet) IncRef()                           { s.refs.Add(1) }
func (s *tunLifecycleRuleSet) DecRef() {
	if s.refs.Add(-1) < 0 {
		panic("negative ref")
	}
}
func (s *tunLifecycleRuleSet) Cleanup() {}
func (s *tunLifecycleRuleSet) RegisterCallback(callback adapter.RuleSetUpdateCallback) *list.Element[adapter.RuleSetUpdateCallback] {
	s.access.Lock()
	defer s.access.Unlock()
	return s.callbacks.PushBack(callback)
}

func (s *tunLifecycleRuleSet) UnregisterCallback(element *list.Element[adapter.RuleSetUpdateCallback]) {
	s.access.Lock()
	s.callbacks.Remove(element)
	s.access.Unlock()
}
func (s *tunLifecycleRuleSet) Close() error                       { return nil }
func (s *tunLifecycleRuleSet) Match(*adapter.InboundContext) bool { return false }
func (s *tunLifecycleRuleSet) String() string                     { return s.name }
func (s *tunLifecycleRuleSet) callbackCount() int {
	s.access.Lock()
	defer s.access.Unlock()
	return s.callbacks.Len()
}

func (s *tunLifecycleRuleSet) fire() {
	s.access.Lock()
	callbacks := s.callbacks.Array()
	s.access.Unlock()
	for _, callback := range callbacks {
		callback(s)
	}
}

type tunLifecycleAutoRedirect struct {
	started atomic.Int32
	closed  atomic.Int32
	updates atomic.Int32
}

func (r *tunLifecycleAutoRedirect) Start() error { r.started.Add(1); return nil }
func (r *tunLifecycleAutoRedirect) Close() error { r.closed.Add(1); return nil }
func (r *tunLifecycleAutoRedirect) UpdateRouteAddressSet() {
	r.updates.Add(1)
}

type tunLifecycleInterface struct {
	closed atomic.Int32
}

func (t *tunLifecycleInterface) Read([]byte) (int, error)              { return 0, errors.New("not implemented") }
func (t *tunLifecycleInterface) Write(payload []byte) (int, error)     { return len(payload), nil }
func (t *tunLifecycleInterface) Name() (string, error)                 { return "test-tun", nil }
func (t *tunLifecycleInterface) Start() error                          { return nil }
func (t *tunLifecycleInterface) Close() error                          { t.closed.Add(1); return nil }
func (t *tunLifecycleInterface) UpdateRouteOptions(stun.Options) error { return nil }

func TestTUNCloseBalancesRuleSetRefsAndCallbacks(t *testing.T) {
	routeSet := newTUNLifecycleRuleSet(t, "route", "192.0.2.0/24")
	excludeSet := newTUNLifecycleRuleSet(t, "exclude", "198.51.100.0/24")
	autoRedirect := new(tunLifecycleAutoRedirect)
	inbound := &Inbound{
		logger:              log.NewNOPFactory().NewLogger("tun-test"),
		autoRedirect:        autoRedirect,
		routeRuleSet:        []adapter.RuleSet{routeSet},
		routeExcludeRuleSet: []adapter.RuleSet{excludeSet},
	}

	inbound.acquireRouteRuleSets()
	require.Equal(t, int32(1), routeSet.refs.Load())
	require.Equal(t, int32(1), excludeSet.refs.Load())
	require.Equal(t, 1, routeSet.callbackCount())
	require.Equal(t, 1, excludeSet.callbackCount())
	require.Len(t, inbound.routeAddressSet, 1, "route sets must not be duplicated during acquisition")
	require.Len(t, inbound.routeExcludeAddressSet, 1)

	var callbacks sync.WaitGroup
	callbacks.Go(func() {
		routeSet.fire()
	})
	require.NoError(t, inbound.Close())
	callbacks.Wait()
	require.NoError(t, inbound.Close())
	require.Zero(t, routeSet.refs.Load())
	require.Zero(t, excludeSet.refs.Load())
	require.Zero(t, routeSet.callbackCount())
	require.Zero(t, excludeSet.callbackCount())
	require.Equal(t, int32(1), autoRedirect.closed.Load())
}

func TestTUNStackFailureClosesInterfaceAndBalancesRefs(t *testing.T) {
	fakeInterface := new(tunLifecycleInterface)
	stackErr := errors.New("stack construction failed")
	ruleSet := newTUNLifecycleRuleSet(t, "route", "192.0.2.0/24")
	inbound := &Inbound{
		ctx:          context.Background(),
		logger:       log.NewNOPFactory().NewLogger("tun-test"),
		tunOptions:   stun.Options{Name: "test-tun", GSO: true},
		routeRuleSet: []adapter.RuleSet{ruleSet},
		openTUN: func(stun.Options) (stun.Tun, error) {
			return fakeInterface, nil
		},
		createTUNStack: func(string, stun.StackOptions) (stun.Stack, error) {
			return nil, stackErr
		},
	}

	err := inbound.Start(adapter.StartStateStart)
	require.ErrorIs(t, err, stackErr)
	require.Equal(t, int32(1), fakeInterface.closed.Load())
	require.Zero(t, ruleSet.refs.Load())
	require.NoError(t, inbound.Close())
	require.Equal(t, int32(1), fakeInterface.closed.Load())
}
