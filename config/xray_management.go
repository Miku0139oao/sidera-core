package config

import (
	"context"
	stdjson "encoding/json"
	"net"
	"net/netip"
	"strconv"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

type xrayTranslationExclusions struct {
	inbounds     map[int]bool
	routingRules map[int]bool
}

type xrayAPI struct {
	Tag      string   `json:"tag"`
	Listen   string   `json:"listen"`
	Services []string `json:"services"`
}

type xrayMetrics struct {
	Tag    string `json:"tag"`
	Listen string `json:"listen"`
}

type xrayPolicy struct {
	Levels map[string]*xrayPolicyLevel `json:"levels"`
	System *xraySystemPolicy           `json:"system"`
}

type xrayPolicyLevel struct {
	Handshake         *uint32 `json:"handshake"`
	ConnectionIdle    *uint32 `json:"connIdle"`
	UplinkOnly        *uint32 `json:"uplinkOnly"`
	DownlinkOnly      *uint32 `json:"downlinkOnly"`
	StatsUserUplink   bool    `json:"statsUserUplink"`
	StatsUserDownlink bool    `json:"statsUserDownlink"`
	StatsUserOnline   bool    `json:"statsUserOnline"`
	BufferSize        *int32  `json:"bufferSize"`
}

type xraySystemPolicy struct {
	StatsInboundUplink    bool `json:"statsInboundUplink"`
	StatsInboundDownlink  bool `json:"statsInboundDownlink"`
	StatsOutboundUplink   bool `json:"statsOutboundUplink"`
	StatsOutboundDownlink bool `json:"statsOutboundDownlink"`
}

type xrayTunnelSettings struct {
	AllowedNetwork stdjson.RawMessage `json:"allowedNetwork"`
	RewriteAddress *string            `json:"rewriteAddress"`
	RewritePort    uint16             `json:"rewritePort"`
	Network        stdjson.RawMessage `json:"network"`
	Address        *string            `json:"address"`
	Port           uint16             `json:"port"`
	PortMap        map[string]string  `json:"portMap"`
	FollowRedirect bool               `json:"followRedirect"`
	UserLevel      uint32             `json:"userLevel"`
}

type xrayStatsUser struct {
	name  string
	level uint32
}

func translateXrayManagement(ctx context.Context, source xrayRoot) (map[string]any, xrayTranslationExclusions, error) {
	exclusions := xrayTranslationExclusions{
		inbounds:     make(map[int]bool),
		routingRules: make(map[int]bool),
	}
	var (
		apiOptions     xrayAPI
		metricsOptions xrayMetrics
		policyOptions  xrayPolicy
	)
	if hasJSONValue(source.API) {
		var err error
		apiOptions, err = decodeXrayObject[xrayAPI](ctx, source.API, "api")
		if err != nil {
			return nil, exclusions, err
		}
	}
	if hasJSONValue(source.Metrics) {
		var err error
		metricsOptions, err = decodeXrayObject[xrayMetrics](ctx, source.Metrics, "metrics")
		if err != nil {
			return nil, exclusions, err
		}
	}
	if hasJSONValue(source.Policy) {
		var err error
		policyOptions, err = decodeXrayObject[xrayPolicy](ctx, source.Policy, "policy")
		if err != nil {
			return nil, exclusions, err
		}
	}
	if hasJSONValue(source.Stats) {
		if _, err := decodeXrayObject[struct{}](ctx, source.Stats, "stats"); err != nil {
			return nil, exclusions, err
		}
	}
	if !hasJSONValue(source.API) && !hasJSONValue(source.Metrics) && !hasJSONValue(source.Policy) && !hasJSONValue(source.Stats) {
		return nil, exclusions, nil
	}

	listen, err := translateXrayAPIListen(ctx, source, apiOptions, &exclusions)
	if err != nil {
		return nil, exclusions, err
	}
	services, statsEnabled := translateXrayAPIServices(apiOptions.Services)
	statsEnabled = statsEnabled || hasJSONValue(source.Stats)
	if hasJSONValue(source.Metrics) {
		statsEnabled = true
		if metricsOptions.Listen == "" {
			return nil, exclusions, E.New("Xray routed metrics handlers are not implemented; metrics.listen is required")
		}
		if metricsOptions.Tag != "" && xrayRoutingUsesOutboundTag(ctx, source.Routing, metricsOptions.Tag) {
			return nil, exclusions, E.New("Xray routed metrics tag has no current Sidera equivalent: ", metricsOptions.Tag)
		}
	}
	stats, err := translateXrayStats(ctx, source, policyOptions, exclusions)
	if err != nil {
		return nil, exclusions, err
	}
	stats["enabled"] = statsEnabled
	v2rayOptions := map[string]any{
		"listen":        listen,
		"stats":         stats,
		"xray_services": services,
	}
	if metricsOptions.Listen != "" {
		v2rayOptions["metrics"] = map[string]any{"listen": metricsOptions.Listen}
	}
	return map[string]any{"v2ray_api": v2rayOptions}, exclusions, nil
}

func translateXrayAPIListen(ctx context.Context, source xrayRoot, apiOptions xrayAPI, exclusions *xrayTranslationExclusions) (string, error) {
	if !hasJSONValue(source.API) {
		return "", nil
	}
	if apiOptions.Tag == "" {
		return "", E.New("Xray api.tag is required")
	}
	for _, outbound := range source.Outbounds {
		if outbound.Tag == apiOptions.Tag {
			return "", E.New("Xray api.tag collides with an explicit outbound: ", apiOptions.Tag)
		}
	}
	if apiOptions.Listen != "" {
		if _, _, err := net.SplitHostPort(apiOptions.Listen); err != nil {
			return "", E.Cause(err, "invalid Xray api.listen")
		}
		return apiOptions.Listen, nil
	}
	var inboundIndex = -1
	for index, inbound := range source.Inbounds {
		if inbound.Tag != apiOptions.Tag {
			continue
		}
		if inboundIndex >= 0 {
			return "", E.New("multiple Xray API tunnel inbounds use tag ", apiOptions.Tag)
		}
		protocol := strings.ToLower(inbound.Protocol)
		if protocol != "tunnel" && protocol != "dokodemo-door" {
			return "", E.New("Xray api.tag must refer to a tunnel inbound")
		}
		if inbound.StreamSettings != nil || inbound.Sniffing != nil {
			return "", E.New("Xray API tunnel cannot use stream or sniffing settings")
		}
		settings, err := decodeXrayObject[xrayTunnelSettings](ctx, inbound.Settings, "API tunnel settings")
		if err != nil {
			return "", err
		}
		address := settings.RewriteAddress
		if settings.Address != nil {
			address = settings.Address
		}
		if address == nil || !isLoopbackAddress(*address) || settings.RewritePort != 0 || settings.Port != 0 || len(settings.PortMap) > 0 || settings.FollowRedirect || settings.UserLevel != 0 || hasJSONValue(settings.AllowedNetwork) || hasJSONValue(settings.Network) {
			return "", E.New("Xray API tunnel must be a plain TCP loopback rewrite")
		}
		inboundIndex = index
	}
	if inboundIndex < 0 {
		return "", E.New("Xray api requires api.listen or a matching tunnel inbound")
	}
	if source.Routing == nil {
		return "", E.New("Xray API tunnel requires a routing rule")
	}
	routeIndex := -1
	for index, rawRule := range source.Routing.Rules {
		rule, err := decodeXrayObject[xrayRoutingRule](ctx, rawRule, "routing.rules["+strconv.Itoa(index)+"]")
		if err != nil {
			return "", err
		}
		if rule.OutboundTag != apiOptions.Tag {
			continue
		}
		if routeIndex >= 0 {
			return "", E.New("multiple Xray API routing rules target ", apiOptions.Tag)
		}
		converted, err := translateXrayRoutingRule(rule)
		if err != nil {
			return "", E.Cause(err, "Xray API routing rule")
		}
		inbounds, loaded := converted["inbound"].([]string)
		if !loaded || len(converted) != 2 || len(inbounds) != 1 || inbounds[0] != apiOptions.Tag {
			return "", E.New("Xray API routing rule must only match its tunnel inbound")
		}
		routeIndex = index
	}
	if routeIndex != 0 {
		return "", E.New("Xray API tunnel routing rule must be the first routing rule")
	}
	port, err := parseSinglePort(source.Inbounds[inboundIndex].Port)
	if err != nil {
		return "", E.Cause(err, "Xray API tunnel port")
	}
	listenAddress := "0.0.0.0"
	if hasJSONValue(source.Inbounds[inboundIndex].Listen) {
		if err = stdjson.Unmarshal(source.Inbounds[inboundIndex].Listen, &listenAddress); err != nil {
			return "", E.Cause(err, "Xray API tunnel listen")
		}
	}
	if !isLoopbackAddress(listenAddress) {
		return "", E.New("Xray API tunnel must listen on loopback")
	}
	exclusions.inbounds[inboundIndex] = true
	exclusions.routingRules[routeIndex] = true
	return net.JoinHostPort(listenAddress, strconv.Itoa(int(port))), nil
}

func translateXrayAPIServices(source []string) ([]string, bool) {
	services := make([]string, 0, len(source))
	seen := make(map[string]bool, len(source))
	var statsEnabled bool
	for _, service := range source {
		normalized := strings.ToLower(service)
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		switch normalized {
		case "statsservice":
			statsEnabled = true
			services = append(services, "StatsService")
		case "handlerservice":
			services = append(services, "HandlerService")
		case "loggerservice":
			services = append(services, "LoggerService")
		case "routingservice":
			services = append(services, "RoutingService")
		case "reflectionservice":
			services = append(services, "ReflectionService")
		case "observatoryservice":
			services = append(services, "ObservatoryService")
		}
	}
	return services, statsEnabled
}

func translateXrayStats(ctx context.Context, source xrayRoot, policy xrayPolicy, exclusions xrayTranslationExclusions) (map[string]any, error) {
	result := make(map[string]any)
	if policy.System != nil {
		if policy.System.StatsInboundUplink != policy.System.StatsInboundDownlink {
			return nil, E.New("Sidera cannot enable only one direction of Xray inbound statistics")
		}
		if policy.System.StatsOutboundUplink != policy.System.StatsOutboundDownlink {
			return nil, E.New("Sidera cannot enable only one direction of Xray outbound statistics")
		}
		if policy.System.StatsInboundUplink {
			var tags []string
			for index, inbound := range source.Inbounds {
				if !exclusions.inbounds[index] && inbound.Tag != "" {
					tags = append(tags, inbound.Tag)
				}
			}
			result["inbounds"] = tags
		}
		if policy.System.StatsOutboundUplink {
			var tags []string
			for _, outbound := range source.Outbounds {
				if outbound.Tag != "" {
					tags = append(tags, outbound.Tag)
				}
			}
			result["outbounds"] = tags
		}
	}
	levels := make(map[uint32]*xrayPolicyLevel, len(policy.Levels))
	for rawLevel, level := range policy.Levels {
		parsedLevel, err := strconv.ParseUint(rawLevel, 10, 32)
		if err != nil {
			return nil, E.New("invalid Xray policy level: ", rawLevel)
		}
		if level == nil {
			continue
		}
		if level.Handshake != nil || level.ConnectionIdle != nil || level.UplinkOnly != nil || level.DownlinkOnly != nil || level.BufferSize != nil {
			return nil, E.New("Xray policy timeout/buffer settings have no current Sidera equivalent")
		}
		if level.StatsUserUplink != level.StatsUserDownlink {
			return nil, E.New("Sidera cannot enable only one direction of Xray user statistics")
		}
		levels[uint32(parsedLevel)] = level
	}
	users, err := collectXrayStatsUsers(ctx, source, exclusions)
	if err != nil {
		return nil, err
	}
	var trafficUsers, onlineUsers []string
	seenTraffic := make(map[string]bool)
	seenOnline := make(map[string]bool)
	for _, user := range users {
		if user.name == "" {
			continue
		}
		level := levels[user.level]
		trafficEnabled := level != nil && level.StatsUserUplink
		onlineEnabled := level != nil && level.StatsUserOnline
		if previous, loaded := seenTraffic[user.name]; loaded && previous != trafficEnabled {
			return nil, E.New("Xray user ", user.name, " appears at levels with incompatible traffic policies")
		}
		if previous, loaded := seenOnline[user.name]; loaded && previous != onlineEnabled {
			return nil, E.New("Xray user ", user.name, " appears at levels with incompatible online policies")
		}
		if _, loaded := seenTraffic[user.name]; !loaded && trafficEnabled {
			trafficUsers = append(trafficUsers, user.name)
		}
		if _, loaded := seenOnline[user.name]; !loaded && onlineEnabled {
			onlineUsers = append(onlineUsers, user.name)
		}
		seenTraffic[user.name] = trafficEnabled
		seenOnline[user.name] = onlineEnabled
	}
	if len(trafficUsers) > 0 {
		result["users"] = trafficUsers
	}
	if len(onlineUsers) > 0 {
		result["users_online"] = onlineUsers
	}
	if (len(trafficUsers) > 0 || len(onlineUsers) > 0 || len(result) > 0) && !hasJSONValue(source.Stats) {
		return nil, E.New("Xray policy statistics require top-level stats configuration")
	}
	return result, nil
}

func collectXrayStatsUsers(ctx context.Context, source xrayRoot, exclusions xrayTranslationExclusions) ([]xrayStatsUser, error) {
	var result []xrayStatsUser
	for index, inbound := range source.Inbounds {
		if exclusions.inbounds[index] {
			continue
		}
		switch strings.ToLower(inbound.Protocol) {
		case "vless":
			settings, err := decodeXrayObject[xrayVLESSInboundSettings](ctx, inbound.Settings, "VLESS settings")
			if err != nil {
				return nil, err
			}
			users := settings.Users
			if settings.Clients != nil {
				users = *settings.Clients
			}
			for _, user := range users {
				result = append(result, xrayStatsUser{name: user.Email, level: user.Level})
			}
		case "hysteria":
			settings, err := decodeXrayObject[xrayHysteriaInboundSettings](ctx, inbound.Settings, "Hysteria settings")
			if err != nil {
				return nil, err
			}
			users := settings.Users
			if settings.Clients != nil {
				users = *settings.Clients
			}
			for _, user := range users {
				result = append(result, xrayStatsUser{name: user.Email, level: user.Level})
			}
		case "socks", "mixed":
			settings, err := decodeXrayObject[xraySocksSettings](ctx, inbound.Settings, "SOCKS settings")
			if err != nil {
				return nil, err
			}
			accounts := settings.Users
			if settings.Accounts != nil {
				accounts = settings.Accounts
			}
			for _, account := range accounts {
				result = append(result, xrayStatsUser{name: account.User, level: settings.UserLevel})
			}
		case "http":
			settings, err := decodeXrayObject[xrayHTTPSettings](ctx, inbound.Settings, "HTTP settings")
			if err != nil {
				return nil, err
			}
			accounts := settings.Users
			if settings.Accounts != nil {
				accounts = settings.Accounts
			}
			for _, account := range accounts {
				result = append(result, xrayStatsUser{name: account.User, level: settings.UserLevel})
			}
		}
	}
	return result, nil
}

func xrayRoutingUsesOutboundTag(ctx context.Context, routing *xrayRouting, tag string) bool {
	if routing == nil {
		return false
	}
	for index, rawRule := range routing.Rules {
		rule, err := decodeXrayObject[xrayRoutingRule](ctx, rawRule, "routing.rules["+strconv.Itoa(index)+"]")
		if err == nil && rule.OutboundTag == tag {
			return true
		}
	}
	return false
}

func isLoopbackAddress(address string) bool {
	if strings.EqualFold(address, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(strings.Trim(address, "[]"))
	return err == nil && ip.Unmap().IsLoopback()
}
