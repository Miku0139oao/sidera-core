package v2rayapi

import (
	"context"
	"net"
	"net/netip"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	_ adapter.ConnectionTracker = (*StatsService)(nil)
	_ StatsServiceServer        = (*StatsService)(nil)
)

type StatsService struct {
	createdAt   time.Time
	inbounds    map[string]bool
	outbounds   map[string]bool
	users       map[string]bool
	onlineUsers map[string]bool
	access      sync.Mutex
	counters    map[string]*atomic.Int64
	online      map[string]map[string]onlineEntry
}

type onlineEntry struct {
	references int
	lastSeen   int64
}

func NewStatsService(options option.V2RayStatsServiceOptions) *StatsService {
	if !options.Enabled {
		return nil
	}
	inbounds := make(map[string]bool)
	outbounds := make(map[string]bool)
	users := make(map[string]bool)
	onlineUsers := make(map[string]bool)
	for _, inbound := range options.Inbounds {
		inbounds[inbound] = true
	}
	for _, outbound := range options.Outbounds {
		outbounds[outbound] = true
	}
	for _, user := range options.Users {
		users[user] = true
	}
	for _, user := range options.UsersOnline {
		onlineUsers[user] = true
	}
	return &StatsService{
		createdAt:   time.Now(),
		inbounds:    inbounds,
		outbounds:   outbounds,
		users:       users,
		onlineUsers: onlineUsers,
		counters:    make(map[string]*atomic.Int64),
		online:      make(map[string]map[string]onlineEntry),
	}
}

func (s *StatsService) RoutedConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, matchedRule adapter.Rule, matchOutbound adapter.Outbound) net.Conn {
	inbound := metadata.Inbound
	user := metadata.User
	outbound := matchOutbound.Tag()
	var readCounter []*atomic.Int64
	var writeCounter []*atomic.Int64
	countInbound := inbound != "" && s.inbounds[inbound]
	countOutbound := outbound != "" && s.outbounds[outbound]
	countUser := user != "" && s.users[user]
	countOnline := user != "" && s.onlineUsers[user]
	if !countInbound && !countOutbound && !countUser && !countOnline {
		return conn
	}
	s.access.Lock()
	if countInbound {
		readCounter = append(readCounter, s.loadOrCreateCounter("inbound>>>"+inbound+">>>traffic>>>uplink"))
		writeCounter = append(writeCounter, s.loadOrCreateCounter("inbound>>>"+inbound+">>>traffic>>>downlink"))
	}
	if countOutbound {
		readCounter = append(readCounter, s.loadOrCreateCounter("outbound>>>"+outbound+">>>traffic>>>uplink"))
		writeCounter = append(writeCounter, s.loadOrCreateCounter("outbound>>>"+outbound+">>>traffic>>>downlink"))
	}
	if countUser {
		readCounter = append(readCounter, s.loadOrCreateCounter("user>>>"+user+">>>traffic>>>uplink"))
		writeCounter = append(writeCounter, s.loadOrCreateCounter("user>>>"+user+">>>traffic>>>downlink"))
	}
	s.access.Unlock()
	if len(readCounter) > 0 || len(writeCounter) > 0 {
		conn = bufio.NewInt64CounterConn(conn, readCounter, writeCounter)
	}
	if countOnline {
		conn = &onlineConn{Conn: conn, release: s.trackOnline(user, metadata.Source.Addr.String())}
	}
	return conn
}

func (s *StatsService) RoutedPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, matchedRule adapter.Rule, matchOutbound adapter.Outbound) N.PacketConn {
	inbound := metadata.Inbound
	user := metadata.User
	outbound := matchOutbound.Tag()
	var readCounter []*atomic.Int64
	var writeCounter []*atomic.Int64
	countInbound := inbound != "" && s.inbounds[inbound]
	countOutbound := outbound != "" && s.outbounds[outbound]
	countUser := user != "" && s.users[user]
	countOnline := user != "" && s.onlineUsers[user]
	if !countInbound && !countOutbound && !countUser && !countOnline {
		return conn
	}
	s.access.Lock()
	if countInbound {
		readCounter = append(readCounter, s.loadOrCreateCounter("inbound>>>"+inbound+">>>traffic>>>uplink"))
		writeCounter = append(writeCounter, s.loadOrCreateCounter("inbound>>>"+inbound+">>>traffic>>>downlink"))
	}
	if countOutbound {
		readCounter = append(readCounter, s.loadOrCreateCounter("outbound>>>"+outbound+">>>traffic>>>uplink"))
		writeCounter = append(writeCounter, s.loadOrCreateCounter("outbound>>>"+outbound+">>>traffic>>>downlink"))
	}
	if countUser {
		readCounter = append(readCounter, s.loadOrCreateCounter("user>>>"+user+">>>traffic>>>uplink"))
		writeCounter = append(writeCounter, s.loadOrCreateCounter("user>>>"+user+">>>traffic>>>downlink"))
	}
	s.access.Unlock()
	if len(readCounter) > 0 || len(writeCounter) > 0 {
		conn = bufio.NewInt64CounterPacketConn(conn, readCounter, nil, writeCounter, nil)
	}
	if countOnline {
		conn = &onlinePacketConn{PacketConn: conn, release: s.trackOnline(user, metadata.Source.Addr.String())}
	}
	return conn
}

func (s *StatsService) RoutedFlow(ctx context.Context, metadata adapter.InboundContext, matchedRule adapter.Rule, matchOutbound adapter.Outbound) tun.FlowTracker {
	inbound := metadata.Inbound
	user := metadata.User
	outbound := matchOutbound.Tag()
	var uplinkCounter []*atomic.Int64
	var downlinkCounter []*atomic.Int64
	countInbound := inbound != "" && s.inbounds[inbound]
	countOutbound := outbound != "" && s.outbounds[outbound]
	countUser := user != "" && s.users[user]
	countOnline := user != "" && s.onlineUsers[user]
	if !countInbound && !countOutbound && !countUser && !countOnline {
		return nil
	}
	s.access.Lock()
	if countInbound {
		uplinkCounter = append(uplinkCounter, s.loadOrCreateCounter("inbound>>>"+inbound+">>>traffic>>>uplink"))
		downlinkCounter = append(downlinkCounter, s.loadOrCreateCounter("inbound>>>"+inbound+">>>traffic>>>downlink"))
	}
	if countOutbound {
		uplinkCounter = append(uplinkCounter, s.loadOrCreateCounter("outbound>>>"+outbound+">>>traffic>>>uplink"))
		downlinkCounter = append(downlinkCounter, s.loadOrCreateCounter("outbound>>>"+outbound+">>>traffic>>>downlink"))
	}
	if countUser {
		uplinkCounter = append(uplinkCounter, s.loadOrCreateCounter("user>>>"+user+">>>traffic>>>uplink"))
		downlinkCounter = append(downlinkCounter, s.loadOrCreateCounter("user>>>"+user+">>>traffic>>>downlink"))
	}
	s.access.Unlock()
	var release func()
	if countOnline {
		release = s.trackOnline(user, metadata.Source.Addr.String())
	}
	return &statsFlowTracker{uplinkCounter: uplinkCounter, downlinkCounter: downlinkCounter, release: release}
}

var _ tun.FlowTracker = (*statsFlowTracker)(nil)

type statsFlowTracker struct {
	uplinkCounter   []*atomic.Int64
	downlinkCounter []*atomic.Int64
	release         func()
	closeOnce       sync.Once
}

func (t *statsFlowTracker) AttachFlow(handle tun.FlowHandle) {
}

func (t *statsFlowTracker) CountForward(n int) {
	for _, counter := range t.uplinkCounter {
		counter.Add(int64(n))
	}
}

func (t *statsFlowTracker) CountReverse(n int) {
	for _, counter := range t.downlinkCounter {
		counter.Add(int64(n))
	}
}

func (t *statsFlowTracker) FlowEstablished() {
}

func (t *statsFlowTracker) CloseFlow(reason tun.FlowCloseReason) {
	if t.release != nil {
		t.closeOnce.Do(t.release)
	}
}

type onlineConn struct {
	net.Conn
	release   func()
	closeOnce sync.Once
}

func (c *onlineConn) Close() error {
	err := c.Conn.Close()
	c.closeOnce.Do(c.release)
	return err
}

func (c *onlineConn) Upstream() any {
	return c.Conn
}

func (c *onlineConn) ReaderReplaceable() bool {
	return true
}

func (c *onlineConn) WriterReplaceable() bool {
	return true
}

func (c *onlineConn) CloseRead() error {
	if closer, loaded := common.Cast[N.ReadCloser](c.Conn); loaded {
		return closer.CloseRead()
	}
	return c.Conn.Close()
}

func (c *onlineConn) CloseWrite() error {
	if closer, loaded := common.Cast[N.WriteCloser](c.Conn); loaded {
		return closer.CloseWrite()
	}
	return c.Conn.Close()
}

type onlinePacketConn struct {
	N.PacketConn
	release   func()
	closeOnce sync.Once
}

func (c *onlinePacketConn) Close() error {
	err := c.PacketConn.Close()
	c.closeOnce.Do(c.release)
	return err
}

func (c *onlinePacketConn) Upstream() any {
	return c.PacketConn
}

func (c *onlinePacketConn) ReaderReplaceable() bool {
	return true
}

func (c *onlinePacketConn) WriterReplaceable() bool {
	return true
}

func (s *StatsService) trackOnline(user string, source string) func() {
	address, err := netip.ParseAddr(source)
	if err != nil || address.IsLoopback() {
		return func() {}
	}
	address = address.Unmap()
	source = address.String()
	s.access.Lock()
	addresses := s.online[user]
	if addresses == nil {
		addresses = make(map[string]onlineEntry)
		s.online[user] = addresses
	}
	entry := addresses[source]
	entry.references++
	entry.lastSeen = time.Now().Unix()
	addresses[source] = entry
	s.access.Unlock()
	return func() {
		s.access.Lock()
		entry, loaded := addresses[source]
		if loaded {
			entry.references--
			if entry.references <= 0 {
				delete(addresses, source)
			} else {
				addresses[source] = entry
			}
		}
		s.access.Unlock()
	}
}

func onlineUserFromStatName(name string) (string, bool) {
	const prefix = "user>>>"
	const suffix = ">>>online"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return "", false
	}
	user := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	return user, user != ""
}

func (s *StatsService) GetStats(ctx context.Context, request *GetStatsRequest) (*GetStatsResponse, error) {
	s.access.Lock()
	counter, loaded := s.counters[request.Name]
	s.access.Unlock()
	if !loaded {
		return nil, status.Error(codes.NotFound, request.Name+" not found.")
	}
	var value int64
	if request.Reset_ {
		value = counter.Swap(0)
	} else {
		value = counter.Load()
	}
	return &GetStatsResponse{Stat: &Stat{Name: request.Name, Value: value}}, nil
}

func (s *StatsService) GetStatsOnline(ctx context.Context, request *GetStatsRequest) (*GetStatsResponse, error) {
	user, loaded := onlineUserFromStatName(request.Name)
	if !loaded || !s.onlineUsers[user] {
		return nil, status.Error(codes.NotFound, request.Name+" not found.")
	}
	s.access.Lock()
	value := int64(len(s.online[user]))
	s.access.Unlock()
	return &GetStatsResponse{Stat: &Stat{Name: request.Name, Value: value}}, nil
}

func (s *StatsService) GetStatsOnlineIpList(ctx context.Context, request *GetStatsRequest) (*GetStatsOnlineIpListResponse, error) {
	user, loaded := onlineUserFromStatName(request.Name)
	if !loaded || !s.onlineUsers[user] {
		return nil, status.Error(codes.NotFound, request.Name+" not found.")
	}
	s.access.Lock()
	addresses := make(map[string]int64, len(s.online[user]))
	for address, entry := range s.online[user] {
		addresses[address] = entry.lastSeen
	}
	s.access.Unlock()
	return &GetStatsOnlineIpListResponse{Name: request.Name, Ips: addresses}, nil
}

func (s *StatsService) GetAllOnlineUsers(ctx context.Context, request *GetAllOnlineUsersRequest) (*GetAllOnlineUsersResponse, error) {
	s.access.Lock()
	users := make([]string, 0, len(s.online))
	for user, addresses := range s.online {
		if len(addresses) > 0 {
			users = append(users, user)
		}
	}
	s.access.Unlock()
	sort.Strings(users)
	return &GetAllOnlineUsersResponse{Users: users}, nil
}

func (s *StatsService) GetUsersStats(ctx context.Context, request *GetUsersStatsRequest) (*GetUsersStatsResponse, error) {
	s.access.Lock()
	defer s.access.Unlock()
	users := make([]string, 0, len(s.online))
	for user, addresses := range s.online {
		if len(addresses) > 0 {
			users = append(users, user)
		}
	}
	sort.Strings(users)
	response := &GetUsersStatsResponse{Users: make([]*UserStat, 0, len(users))}
	for _, user := range users {
		addresses := s.online[user]
		addressNames := make([]string, 0, len(addresses))
		for address := range addresses {
			addressNames = append(addressNames, address)
		}
		sort.Strings(addressNames)
		userStats := &UserStat{Email: user, Ips: make([]*OnlineIPEntry, 0, len(addressNames))}
		for _, address := range addressNames {
			userStats.Ips = append(userStats.Ips, &OnlineIPEntry{Ip: address, LastSeen: addresses[address].lastSeen})
		}
		if request.IncludeTraffic {
			userStats.Traffic = &TrafficUserStat{
				Uplink:   s.counterValueLocked("user>>>"+user+">>>traffic>>>uplink", request.Reset_),
				Downlink: s.counterValueLocked("user>>>"+user+">>>traffic>>>downlink", request.Reset_),
			}
		}
		response.Users = append(response.Users, userStats)
	}
	return response, nil
}

func (s *StatsService) QueryStats(ctx context.Context, request *QueryStatsRequest) (*QueryStatsResponse, error) {
	var response QueryStatsResponse
	s.access.Lock()
	defer s.access.Unlock()
	patterns := request.Patterns
	if len(patterns) == 0 && request.Pattern != "" {
		patterns = []string{request.Pattern}
	}
	if len(patterns) == 0 {
		for name, counter := range s.counters {
			var value int64
			if request.Reset_ {
				value = counter.Swap(0)
			} else {
				value = counter.Load()
			}
			response.Stat = append(response.Stat, &Stat{Name: name, Value: value})
		}
	} else if request.Regexp {
		matchers := make([]*regexp.Regexp, 0, len(patterns))
		for _, pattern := range patterns {
			matcher, err := regexp.Compile(pattern)
			if err != nil {
				return nil, err
			}
			matchers = append(matchers, matcher)
		}
		for name, counter := range s.counters {
			for _, matcher := range matchers {
				if matcher.MatchString(name) {
					var value int64
					if request.Reset_ {
						value = counter.Swap(0)
					} else {
						value = counter.Load()
					}
					response.Stat = append(response.Stat, &Stat{Name: name, Value: value})
					break
				}
			}
		}
	} else {
		for name, counter := range s.counters {
			for _, matcher := range patterns {
				if strings.Contains(name, matcher) {
					var value int64
					if request.Reset_ {
						value = counter.Swap(0)
					} else {
						value = counter.Load()
					}
					response.Stat = append(response.Stat, &Stat{Name: name, Value: value})
					break
				}
			}
		}
	}
	return &response, nil
}

func (s *StatsService) GetSysStats(ctx context.Context, request *SysStatsRequest) (*SysStatsResponse, error) {
	var rtm runtime.MemStats
	runtime.ReadMemStats(&rtm)
	response := &SysStatsResponse{
		Uptime:       uint32(time.Since(s.createdAt).Seconds()),
		NumGoroutine: uint32(runtime.NumGoroutine()),
		Alloc:        rtm.Alloc,
		TotalAlloc:   rtm.TotalAlloc,
		Sys:          rtm.Sys,
		Mallocs:      rtm.Mallocs,
		Frees:        rtm.Frees,
		LiveObjects:  rtm.Mallocs - rtm.Frees,
		NumGC:        rtm.NumGC,
		PauseTotalNs: rtm.PauseTotalNs,
	}

	return response, nil
}

func (s *StatsService) Snapshot() map[string]map[string]map[string]int64 {
	result := map[string]map[string]map[string]int64{
		"inbound":  {},
		"outbound": {},
		"user":     {},
	}
	s.access.Lock()
	defer s.access.Unlock()
	for name, counter := range s.counters {
		parts := strings.Split(name, ">>>")
		if len(parts) != 4 || parts[2] != "traffic" {
			continue
		}
		category := result[parts[0]]
		if category == nil {
			category = make(map[string]map[string]int64)
			result[parts[0]] = category
		}
		item := category[parts[1]]
		if item == nil {
			item = make(map[string]int64)
			category[parts[1]] = item
		}
		item[parts[3]] = counter.Load()
	}
	return result
}

func (s *StatsService) counterValueLocked(name string, reset bool) int64 {
	counter := s.counters[name]
	if counter == nil {
		return 0
	}
	if reset {
		return counter.Swap(0)
	}
	return counter.Load()
}

func (s *StatsService) mustEmbedUnimplementedStatsServiceServer() {
}

//nolint:staticcheck
func (s *StatsService) loadOrCreateCounter(name string) *atomic.Int64 {
	counter, loaded := s.counters[name]
	if loaded {
		return counter
	}
	counter = &atomic.Int64{}
	s.counters[name] = counter
	return counter
}
