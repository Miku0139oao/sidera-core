package config

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Miku0139oao/sidera-core/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
)

type xrayRoot struct {
	Log              *xrayLog           `json:"log"`
	Routing          *xrayRouting       `json:"routing"`
	Inbounds         []xrayInbound      `json:"inbounds"`
	Outbounds        []xrayOutbound     `json:"outbounds"`
	Transport        stdjson.RawMessage `json:"transport"`
	Env              stdjson.RawMessage `json:"env"`
	DNS              stdjson.RawMessage `json:"dns"`
	Policy           stdjson.RawMessage `json:"policy"`
	API              stdjson.RawMessage `json:"api"`
	Metrics          stdjson.RawMessage `json:"metrics"`
	Stats            stdjson.RawMessage `json:"stats"`
	Reverse          stdjson.RawMessage `json:"reverse"`
	FakeDNS          stdjson.RawMessage `json:"fakeDns"`
	Observatory      stdjson.RawMessage `json:"observatory"`
	BurstObservatory stdjson.RawMessage `json:"burstObservatory"`
	Version          stdjson.RawMessage `json:"version"`
	Geodata          stdjson.RawMessage `json:"geodata"`
}

type xrayLog struct {
	AccessLog   string `json:"access"`
	ErrorLog    string `json:"error"`
	LogLevel    string `json:"loglevel"`
	DNSLog      bool   `json:"dnsLog"`
	MaskAddress string `json:"maskAddress"`
}

type xrayInbound struct {
	Protocol       string             `json:"protocol"`
	Port           stdjson.RawMessage `json:"port"`
	Listen         stdjson.RawMessage `json:"listen"`
	Settings       stdjson.RawMessage `json:"settings"`
	Tag            string             `json:"tag"`
	StreamSettings *xrayStream        `json:"streamSettings"`
	Sniffing       *xraySniffing      `json:"sniffing"`
}

type xrayOutbound struct {
	Protocol       string             `json:"protocol"`
	SendThrough    string             `json:"sendThrough"`
	Tag            string             `json:"tag"`
	Settings       stdjson.RawMessage `json:"settings"`
	StreamSettings *xrayStream        `json:"streamSettings"`
	ProxySettings  stdjson.RawMessage `json:"proxySettings"`
	Mux            *xrayMux           `json:"mux"`
	TargetStrategy string             `json:"targetStrategy"`
}

type xrayWireGuardSettings struct {
	SecretKey      string              `json:"secretKey"`
	Address        xrayStringList      `json:"address"`
	Peers          []xrayWireGuardPeer `json:"peers"`
	Reserved       []uint8             `json:"reserved"`
	NoKernelTun    bool                `json:"noKernelTun"`
	MTU            uint32              `json:"mtu"`
	DomainStrategy string              `json:"domainStrategy"`
}

type xrayWireGuardPeer struct {
	PublicKey  string         `json:"publicKey"`
	PreShared  string         `json:"preSharedKey"`
	AllowedIPs xrayStringList `json:"allowedIPs"`
	Endpoint   string         `json:"endpoint"`
	KeepAlive  uint16         `json:"keepAlive"`
}

type xraySniffing struct {
	Enabled         bool           `json:"enabled"`
	DestOverride    xrayStringList `json:"destOverride"`
	DomainsExcluded xrayStringList `json:"domainsExcluded"`
	IPsExcluded     xrayStringList `json:"ipsExcluded"`
	MetadataOnly    bool           `json:"metadataOnly"`
	RouteOnly       bool           `json:"routeOnly"`
}

type xrayMux struct {
	Enabled         bool   `json:"enabled"`
	Concurrency     int16  `json:"concurrency"`
	XUDPConcurrency int16  `json:"xudpConcurrency"`
	XUDPProxyUDP443 string `json:"xudpProxyUDP443"`
}

type xrayStream struct {
	Address             *string            `json:"address"`
	Port                uint16             `json:"port"`
	Method              *string            `json:"method"`
	Network             *string            `json:"network"`
	Security            string             `json:"security"`
	FinalMask           stdjson.RawMessage `json:"finalmask"`
	TLSSettings         *xrayTLS           `json:"tlsSettings"`
	RealitySettings     *xrayReality       `json:"realitySettings"`
	RawSettings         *xrayTCP           `json:"rawSettings"`
	TCPSettings         *xrayTCP           `json:"tcpSettings"`
	XHTTPSettings       stdjson.RawMessage `json:"xhttpSettings"`
	SplitHTTPSettings   stdjson.RawMessage `json:"splithttpSettings"`
	KCPSettings         stdjson.RawMessage `json:"kcpSettings"`
	GRPCSettings        *xrayGRPC          `json:"grpcSettings"`
	WSSettings          *xrayWebSocket     `json:"wsSettings"`
	HTTPUpgradeSettings *xrayHTTPUpgrade   `json:"httpupgradeSettings"`
	HysteriaSettings    stdjson.RawMessage `json:"hysteriaSettings"`
	SocketSettings      stdjson.RawMessage `json:"sockopt"`
}

type xrayTCP struct {
	Header              stdjson.RawMessage `json:"header"`
	AcceptProxyProtocol bool               `json:"acceptProxyProtocol"`
}

type xrayWebSocket struct {
	Host                string            `json:"host"`
	Path                string            `json:"path"`
	Headers             map[string]string `json:"headers"`
	AcceptProxyProtocol bool              `json:"acceptProxyProtocol"`
	HeartbeatPeriod     uint32            `json:"heartbeatPeriod"`
}

type xrayGRPC struct {
	Authority           string `json:"authority"`
	ServiceName         string `json:"serviceName"`
	MultiMode           bool   `json:"multiMode"`
	IdleTimeout         int32  `json:"idle_timeout"`
	HealthCheckTimeout  int32  `json:"health_check_timeout"`
	PermitWithoutStream bool   `json:"permit_without_stream"`
	InitialWindowsSize  int32  `json:"initial_windows_size"`
	UserAgent           string `json:"user_agent"`
}

type xrayHTTPUpgrade struct {
	Host                string            `json:"host"`
	Path                string            `json:"path"`
	Headers             map[string]string `json:"headers"`
	AcceptProxyProtocol bool              `json:"acceptProxyProtocol"`
}

type xrayTLS struct {
	AllowInsecure           bool               `json:"allowInsecure"`
	Certificates            []xrayTLSCert      `json:"certificates"`
	ServerName              string             `json:"serverName"`
	ALPN                    xrayStringList     `json:"alpn"`
	EnableSessionResumption bool               `json:"enableSessionResumption"`
	DisableSystemRoot       bool               `json:"disableSystemRoot"`
	MinVersion              string             `json:"minVersion"`
	MaxVersion              string             `json:"maxVersion"`
	CipherSuites            string             `json:"cipherSuites"`
	Fingerprint             string             `json:"fingerprint"`
	RejectUnknownSNI        bool               `json:"rejectUnknownSni"`
	CurvePreferences        xrayStringList     `json:"curvePreferences"`
	MasterKeyLog            string             `json:"masterKeyLog"`
	PinnedPeerCertSHA256    string             `json:"pinnedPeerCertSha256"`
	VerifyPeerCertByName    string             `json:"verifyPeerCertByName"`
	ECHServerKeys           string             `json:"echServerKeys"`
	ECHConfigList           string             `json:"echConfigList"`
	ECHSocketSettings       stdjson.RawMessage `json:"echSockopt"`
}

type xrayTLSCert struct {
	CertificateFile string   `json:"certificateFile"`
	Certificate     []string `json:"certificate"`
	KeyFile         string   `json:"keyFile"`
	Key             []string `json:"key"`
	Usage           string   `json:"usage"`
	OCSPStapling    uint64   `json:"ocspStapling"`
	OneTimeLoading  bool     `json:"oneTimeLoading"`
	BuildChain      bool     `json:"buildChain"`
}

type xrayReality struct {
	MasterKeyLog string             `json:"masterKeyLog"`
	Show         bool               `json:"show"`
	Target       stdjson.RawMessage `json:"target"`
	Dest         stdjson.RawMessage `json:"dest"`
	Type         string             `json:"type"`
	XVer         uint64             `json:"xver"`
	ServerNames  []string           `json:"serverNames"`
	PrivateKey   string             `json:"privateKey"`
	MinClientVer string             `json:"minClientVer"`
	MaxClientVer string             `json:"maxClientVer"`
	MaxTimeDiff  uint64             `json:"maxTimeDiff"`
	ShortIDs     []string           `json:"shortIds"`
	MLDSA65Seed  string             `json:"mldsa65Seed"`

	LimitFallbackUpload   xrayLimitFallback `json:"limitFallbackUpload"`
	LimitFallbackDownload xrayLimitFallback `json:"limitFallbackDownload"`

	Fingerprint   string `json:"fingerprint"`
	ServerName    string `json:"serverName"`
	Password      string `json:"password"`
	PublicKey     string `json:"publicKey"`
	ShortID       string `json:"shortId"`
	MLDSA65Verify string `json:"mldsa65Verify"`
	SpiderX       string `json:"spiderX"`
}

type xrayLimitFallback struct {
	AfterBytes       uint64 `json:"AfterBytes"`
	BytesPerSec      uint64 `json:"BytesPerSec"`
	BurstBytesPerSec uint64 `json:"BurstBytesPerSec"`
}

type xraySocksSettings struct {
	Auth      string        `json:"auth"`
	Users     []xrayAccount `json:"users"`
	Accounts  []xrayAccount `json:"accounts"`
	UDP       bool          `json:"udp"`
	IP        *string       `json:"ip"`
	UserLevel uint32        `json:"userLevel"`
}

type xrayHTTPSettings struct {
	Users            []xrayAccount `json:"users"`
	Accounts         []xrayAccount `json:"accounts"`
	AllowTransparent bool          `json:"allowTransparent"`
	UserLevel        uint32        `json:"userLevel"`
}

type xrayAccount struct {
	User string `json:"user"`
	Pass string `json:"pass"`
}

type xrayVLESSSettings struct {
	Address    *string            `json:"address"`
	Port       uint16             `json:"port"`
	Level      uint32             `json:"level"`
	Email      string             `json:"email"`
	ID         string             `json:"id"`
	Flow       string             `json:"flow"`
	Seed       string             `json:"seed"`
	Encryption string             `json:"encryption"`
	Reverse    stdjson.RawMessage `json:"reverse"`
	TestPre    uint32             `json:"testpre"`
	TestSeed   []uint32           `json:"testseed"`
	VNext      []xrayVLESSNext    `json:"vnext"`
}

type xrayVLESSInboundSettings struct {
	Users      []xrayVLESSUser      `json:"users"`
	Clients    *[]xrayVLESSUser     `json:"clients"`
	Decryption string               `json:"decryption"`
	Fallbacks  []stdjson.RawMessage `json:"fallbacks"`
	Flow       string               `json:"flow"`
	TestSeed   []uint32             `json:"testseed"`
}

type xrayVLESSNext struct {
	Address *string         `json:"address"`
	Port    uint16          `json:"port"`
	Users   []xrayVLESSUser `json:"users"`
}

type xrayVLESSUser struct {
	Level      uint32             `json:"level"`
	Email      string             `json:"email"`
	ID         string             `json:"id"`
	Flow       string             `json:"flow"`
	Encryption string             `json:"encryption"`
	Reverse    stdjson.RawMessage `json:"reverse"`
	TestPre    uint32             `json:"testpre"`
	TestSeed   []uint32           `json:"testseed"`
}

type xrayFreedomSettings struct {
	TargetStrategy string             `json:"targetStrategy"`
	DomainStrategy string             `json:"domainStrategy"`
	Redirect       string             `json:"redirect"`
	UserLevel      uint32             `json:"userLevel"`
	Fragment       stdjson.RawMessage `json:"fragment"`
	Noise          stdjson.RawMessage `json:"noise"`
	Noises         stdjson.RawMessage `json:"noises"`
	ProxyProtocol  uint32             `json:"proxyProtocol"`
	IPsBlocked     stdjson.RawMessage `json:"ipsBlocked"`
	FinalRules     stdjson.RawMessage `json:"finalRules"`
}

type xrayBlackholeSettings struct {
	Response stdjson.RawMessage `json:"response"`
}

type xrayRouting struct {
	Rules          []stdjson.RawMessage `json:"rules"`
	DomainStrategy string               `json:"domainStrategy"`
	Balancers      stdjson.RawMessage   `json:"balancers"`
}

type xrayRoutingRule struct {
	Type        string             `json:"type"`
	RuleTag     string             `json:"ruleTag"`
	OutboundTag string             `json:"outboundTag"`
	BalancerTag string             `json:"balancerTag"`
	Domain      xrayStringList     `json:"domain"`
	Domains     *xrayStringList    `json:"domains"`
	IP          xrayStringList     `json:"ip"`
	Port        stdjson.RawMessage `json:"port"`
	Network     xrayStringList     `json:"network"`
	SourceIP    *xrayStringList    `json:"sourceIP"`
	Source      xrayStringList     `json:"source"`
	SourcePort  stdjson.RawMessage `json:"sourcePort"`
	User        xrayStringList     `json:"user"`
	VLESSRoute  stdjson.RawMessage `json:"vlessRoute"`
	InboundTag  xrayStringList     `json:"inboundTag"`
	Protocols   xrayStringList     `json:"protocol"`
	Attributes  map[string]string  `json:"attrs"`
	LocalIP     xrayStringList     `json:"localIP"`
	LocalPort   stdjson.RawMessage `json:"localPort"`
	Process     xrayStringList     `json:"process"`
	Webhook     stdjson.RawMessage `json:"webhook"`
}

type xrayStringList []string

func (l *xrayStringList) UnmarshalJSON(content []byte) error {
	if bytes.Equal(bytes.TrimSpace(content), []byte("null")) {
		*l = nil
		return nil
	}
	var single string
	if err := json.Unmarshal(content, &single); err == nil {
		*l = strings.Split(single, ",")
		return nil
	}
	return json.Unmarshal(content, (*[]string)(l))
}

func translateXray(ctx context.Context, content []byte) (option.Options, error) {
	source, err := decodeXrayObject[xrayRoot](ctx, content, "config")
	if err != nil {
		return option.Options{}, err
	}
	for _, field := range []struct {
		name string
		raw  stdjson.RawMessage
	}{
		{"transport", source.Transport},
		{"env", source.Env},
		{"dns", source.DNS},
		{"reverse", source.Reverse},
		{"fakeDns", source.FakeDNS},
		{"observatory", source.Observatory},
		{"burstObservatory", source.BurstObservatory},
		{"version", source.Version},
		{"geodata", source.Geodata},
	} {
		if hasJSONValue(field.raw) {
			return option.Options{}, unsupportedXrayField(field.name)
		}
	}
	experimentalOptions, exclusions, err := translateXrayManagement(ctx, source)
	if err != nil {
		return option.Options{}, err
	}
	canonical := make(map[string]any)
	if experimentalOptions != nil {
		canonical["experimental"] = experimentalOptions
	}
	if source.Log == nil {
		canonical["log"] = map[string]any{"level": "warn"}
	} else {
		convertedLog, convertErr := translateXrayLog(source.Log)
		if convertErr != nil {
			return option.Options{}, convertErr
		}
		if convertedLog != nil {
			canonical["log"] = convertedLog
		}
	}
	var routeRules []map[string]any
	if len(source.Inbounds) > 0 {
		inbounds := make([]map[string]any, 0, len(source.Inbounds))
		for index, inbound := range source.Inbounds {
			if exclusions.inbounds[index] {
				continue
			}
			converted, generatedRules, convertErr := translateXrayInbound(ctx, inbound)
			if convertErr != nil {
				return option.Options{}, E.Cause(convertErr, "inbounds[", index, "]")
			}
			inbounds = append(inbounds, converted)
			routeRules = append(routeRules, generatedRules...)
		}
		canonical["inbounds"] = inbounds
	}
	if len(source.Outbounds) > 0 {
		outbounds := make([]map[string]any, 0, len(source.Outbounds))
		endpoints := make([]map[string]any, 0)
		for index, outbound := range source.Outbounds {
			if strings.EqualFold(outbound.Protocol, "wireguard") {
				converted, convertErr := translateXrayWireGuardEndpoint(ctx, outbound)
				if convertErr != nil {
					return option.Options{}, E.Cause(convertErr, "outbounds[", index, "]")
				}
				endpoints = append(endpoints, converted)
				continue
			}
			converted, convertErr := translateXrayOutbound(ctx, outbound)
			if convertErr != nil {
				return option.Options{}, E.Cause(convertErr, "outbounds[", index, "]")
			}
			outbounds = append(outbounds, converted)
		}
		canonical["outbounds"] = outbounds
		if len(endpoints) > 0 {
			canonical["endpoints"] = endpoints
		}
	}
	convertedRoute, convertErr := translateXrayRouting(ctx, source.Routing, exclusions.routingRules)
	if convertErr != nil {
		return option.Options{}, convertErr
	}
	if convertedRoute != nil {
		routeRules = append(routeRules, convertedRoute...)
	}
	if len(routeRules) > 0 {
		canonical["route"] = map[string]any{"rules": routeRules}
	}
	canonicalContent, err := json.MarshalContext(ctx, canonical)
	if err != nil {
		return option.Options{}, E.Cause(err, "encode canonical config")
	}
	options, err := json.UnmarshalExtendedContext[option.Options](ctx, canonicalContent)
	if err != nil {
		return option.Options{}, E.Cause(err, "decode canonical config")
	}
	nativeOutboundIndex := 0
	for _, outbound := range source.Outbounds {
		if strings.EqualFold(outbound.Protocol, "wireguard") {
			continue
		}
		if !strings.EqualFold(outbound.Protocol, "vless") {
			nativeOutboundIndex++
			continue
		}
		vlessOptions, loaded := options.Outbounds[nativeOutboundIndex].Options.(*option.VLESSOutboundOptions)
		if loaded && vlessOptions.Flow == "" {
			vlessOptions.XrayPacketEncoding = true
		}
		nativeOutboundIndex++
	}
	return options, nil
}

func translateXrayLog(source *xrayLog) (map[string]any, error) {
	if source.DNSLog {
		return nil, unsupportedXrayField("log.dnsLog")
	}
	if source.MaskAddress != "" {
		return nil, unsupportedXrayField("log.maskAddress")
	}
	if source.AccessLog == "" && !strings.EqualFold(source.LogLevel, "none") {
		return nil, E.New("Xray console access logging has no current Sidera equivalent; set log.access to none")
	}
	if source.AccessLog != "" && source.AccessLog != "none" {
		return nil, unsupportedXrayField("log.access")
	}
	result := make(map[string]any)
	switch strings.ToLower(source.LogLevel) {
	case "", "warning", "warn":
		result["level"] = "warn"
	case "debug", "info", "error":
		result["level"] = strings.ToLower(source.LogLevel)
	case "none":
		result["disabled"] = true
	default:
		return nil, E.New("unsupported Xray log level: ", source.LogLevel)
	}
	if source.ErrorLog == "none" {
		if source.AccessLog != "none" && source.LogLevel != "none" {
			return nil, E.New("Xray log.error=none without log.access=none has no sing-box equivalent")
		}
		result["disabled"] = true
	} else if source.ErrorLog != "" {
		if source.AccessLog != "none" {
			return nil, E.New("Xray file logging requires log.access=none for lossless translation")
		}
		result["output"] = source.ErrorLog
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

func translateXrayInbound(ctx context.Context, source xrayInbound) (map[string]any, []map[string]any, error) {
	if source.Protocol == "" {
		return nil, nil, E.New("missing protocol")
	}
	port, err := parseSinglePort(source.Port)
	if err != nil {
		return nil, nil, E.Cause(err, "port")
	}
	result := map[string]any{
		"type":        strings.ToLower(source.Protocol),
		"listen_port": port,
	}
	if source.Tag != "" {
		result["tag"] = source.Tag
	}
	if hasJSONValue(source.Listen) {
		var listen string
		if err = json.Unmarshal(source.Listen, &listen); err != nil {
			return nil, nil, E.Cause(err, "listen must be an IP address or local socket string")
		}
		result["listen"] = listen
	} else {
		result["listen"] = "0.0.0.0"
	}
	var generatedRules []map[string]any
	switch strings.ToLower(source.Protocol) {
	case "socks", "mixed":
		if source.StreamSettings != nil {
			return nil, nil, unsupportedXrayField("streamSettings for inbound protocol " + source.Protocol)
		}
		result["type"] = "mixed"
		settings, decodeErr := decodeXrayObject[xraySocksSettings](ctx, source.Settings, "settings")
		if decodeErr != nil {
			return nil, nil, decodeErr
		}
		if settings.UserLevel != 0 {
			return nil, nil, unsupportedXrayField("settings.userLevel")
		}
		accounts := settings.Users
		if settings.Accounts != nil {
			accounts = settings.Accounts
		}
		switch strings.ToLower(settings.Auth) {
		case "", "noauth":
		case "password":
			if len(accounts) == 0 {
				return nil, nil, E.New("settings.auth=password requires at least one account")
			}
			result["users"] = translateAccounts(accounts)
		default:
			return nil, nil, E.New("unsupported SOCKS authentication method: ", settings.Auth)
		}
		if !settings.UDP {
			return nil, nil, E.New("Xray SOCKS udp=false has no exact Sidera inbound equivalent")
		} else if settings.IP != nil && *settings.IP != "" {
			return nil, nil, unsupportedXrayField("settings.ip")
		}
	case "http":
		if source.StreamSettings != nil {
			return nil, nil, unsupportedXrayField("streamSettings for inbound protocol " + source.Protocol)
		}
		settings, decodeErr := decodeXrayObject[xrayHTTPSettings](ctx, source.Settings, "settings")
		if decodeErr != nil {
			return nil, nil, decodeErr
		}
		if settings.AllowTransparent {
			return nil, nil, unsupportedXrayField("settings.allowTransparent")
		}
		if settings.UserLevel != 0 {
			return nil, nil, unsupportedXrayField("settings.userLevel")
		}
		accounts := settings.Users
		if settings.Accounts != nil {
			accounts = settings.Accounts
		}
		if len(accounts) > 0 {
			result["users"] = translateAccounts(accounts)
		}
	case "vless":
		settings, decodeErr := decodeXrayObject[xrayVLESSInboundSettings](ctx, source.Settings, "settings")
		if decodeErr != nil {
			return nil, nil, decodeErr
		}
		converted, convertErr := translateXrayVLESSInbound(settings)
		if convertErr != nil {
			return nil, nil, convertErr
		}
		for key, value := range converted {
			result[key] = value
		}
		stream, convertErr := translateXrayVLESSInboundStream(source.StreamSettings)
		if convertErr != nil {
			return nil, nil, convertErr
		}
		for key, value := range stream {
			result[key] = value
		}
	case "hysteria":
		converted, convertErr := translateXrayHysteria2Inbound(ctx, source)
		if convertErr != nil {
			return nil, nil, convertErr
		}
		for key, value := range converted {
			result[key] = value
		}
	default:
		return nil, nil, E.New("unsupported Xray inbound protocol: ", source.Protocol)
	}
	if source.Sniffing != nil && source.Sniffing.Enabled {
		if source.Sniffing.MetadataOnly {
			return nil, nil, unsupportedXrayField("sniffing.metadataOnly")
		}
		if !source.Sniffing.RouteOnly {
			return nil, nil, E.New("Xray sniffing.routeOnly=false has no current Sidera equivalent")
		}
		if len(source.Sniffing.DomainsExcluded) > 0 || len(source.Sniffing.IPsExcluded) > 0 {
			return nil, nil, E.New("Xray sniffing exclusions have no current Sidera equivalent")
		}
		if source.Tag == "" {
			return nil, nil, E.New("Xray sniffing requires an explicit inbound tag for translation")
		}
		sniffRule := map[string]any{
			"inbound": source.Tag,
			"action":  "sniff",
		}
		if len(source.Sniffing.DestOverride) > 0 {
			var sniffers []string
			for _, sniffer := range source.Sniffing.DestOverride {
				switch strings.ToLower(sniffer) {
				case "http", "tls", "quic":
					sniffers = append(sniffers, strings.ToLower(sniffer))
				default:
					return nil, nil, E.New("unsupported Xray sniffer: ", sniffer)
				}
			}
			sniffRule["sniffer"] = sniffers
		}
		generatedRules = append(generatedRules, sniffRule)
	}
	return result, generatedRules, nil
}

func translateXrayVLESSInbound(source xrayVLESSInboundSettings) (map[string]any, error) {
	if source.Decryption == "" {
		return nil, E.New("VLESS settings.decryption must be explicitly configured")
	}
	if len(source.Fallbacks) > 0 {
		return nil, E.New("VLESS fallbacks are not implemented")
	}
	if source.Flow != "" && source.Flow != "xtls-rprx-vision" {
		return nil, E.New("unsupported VLESS flow: ", source.Flow)
	}
	if !isDefaultXrayVisionSeed(source.TestSeed) {
		return nil, E.New("custom VLESS testseed is not implemented")
	}
	users := source.Users
	if source.Clients != nil {
		users = *source.Clients
	}
	convertedUsers := make([]map[string]any, len(users))
	for index, user := range users {
		if user.ID == "" {
			return nil, E.New("VLESS user ", index, " id is required")
		}
		if user.Level != 0 {
			return nil, E.New("VLESS user level has no current Sidera equivalent")
		}
		if user.Encryption != "" {
			return nil, E.New("VLESS inbound users cannot configure encryption")
		}
		if hasJSONValue(user.Reverse) || user.TestPre != 0 {
			return nil, E.New("VLESS reverse/testpre fields are not supported")
		}
		if !isDefaultXrayVisionSeed(user.TestSeed) {
			return nil, E.New("custom VLESS user testseed is not implemented")
		}
		flow := user.Flow
		if flow == "" {
			flow = source.Flow
		}
		if flow != "" && flow != "xtls-rprx-vision" {
			return nil, E.New("unsupported VLESS user flow: ", flow)
		}
		convertedUser := map[string]any{
			"name": user.Email,
			"uuid": user.ID,
		}
		if flow != "" {
			convertedUser["flow"] = flow
		}
		convertedUsers[index] = convertedUser
	}
	return map[string]any{
		"users":      convertedUsers,
		"decryption": source.Decryption,
	}, nil
}

func isDefaultXrayVisionSeed(seed []uint32) bool {
	if len(seed) < 4 {
		return true
	}
	defaultSeed := [...]uint32{900, 500, 900, 256}
	for index, value := range defaultSeed {
		if seed[index] != value {
			return false
		}
	}
	return true
}

func translateAccounts(accounts []xrayAccount) []map[string]any {
	result := make([]map[string]any, 0, len(accounts))
	for _, account := range accounts {
		result = append(result, map[string]any{
			"Username": account.User,
			"Password": account.Pass,
		})
	}
	return result
}

func translateXrayOutbound(ctx context.Context, source xrayOutbound) (map[string]any, error) {
	protocol := strings.ToLower(source.Protocol)
	if protocol == "" {
		return nil, E.New("missing protocol")
	}
	if source.SendThrough != "" {
		return nil, unsupportedXrayField("sendThrough")
	}
	if hasJSONValue(source.ProxySettings) {
		return nil, unsupportedXrayField("proxySettings")
	}
	if source.Mux != nil && source.Mux.Enabled {
		return nil, E.New("Xray Mux.Cool cannot be translated to sing-mux")
	}
	switch strings.ToLower(source.TargetStrategy) {
	case "", "asis":
	default:
		return nil, unsupportedXrayField("targetStrategy=" + source.TargetStrategy)
	}
	result := map[string]any{"type": protocol}
	if source.Tag != "" {
		result["tag"] = source.Tag
	}
	switch protocol {
	case "vless":
		settings, err := decodeXrayObject[xrayVLESSSettings](ctx, source.Settings, "settings")
		if err != nil {
			return nil, err
		}
		converted, err := translateXrayVLESS(settings)
		if err != nil {
			return nil, err
		}
		for key, value := range converted {
			result[key] = value
		}
		if source.StreamSettings != nil {
			stream, err := translateXrayStream(source.StreamSettings)
			if err != nil {
				return nil, err
			}
			for key, value := range stream {
				result[key] = value
			}
		}
		_, secure := result["tls"]
		encryption, _ := converted["encryption"].(string)
		if !secure && encryption == "none" && !isXrayPrivateAddress(converted["server"].(string)) {
			return nil, E.New("VLESS without TLS or other encryption is prohibited for a public server address")
		}
	case "freedom", "direct":
		result["type"] = "direct"
		if source.StreamSettings != nil {
			return nil, unsupportedXrayField("streamSettings for direct outbound")
		}
		settings, err := decodeXrayObject[xrayFreedomSettings](ctx, source.Settings, "settings")
		if err != nil {
			return nil, err
		}
		if err = validateFreedomSettings(ctx, settings); err != nil {
			return nil, err
		}
	case "blackhole", "block":
		result["type"] = "block"
		if source.StreamSettings != nil {
			return nil, unsupportedXrayField("streamSettings for block outbound")
		}
		settings, err := decodeXrayObject[xrayBlackholeSettings](ctx, source.Settings, "settings")
		if err != nil {
			return nil, err
		}
		if hasJSONValue(settings.Response) {
			var response struct {
				Type string `json:"type"`
			}
			if err = decodeXrayInto(ctx, settings.Response, "settings.response", &response); err != nil {
				return nil, err
			}
			if response.Type != "" && strings.ToLower(response.Type) != "none" {
				return nil, unsupportedXrayField("settings.response.type=" + response.Type)
			}
		}
	default:
		return nil, E.New("unsupported Xray outbound protocol: ", source.Protocol)
	}
	return result, nil
}

func translateXrayWireGuardEndpoint(ctx context.Context, source xrayOutbound) (map[string]any, error) {
	if source.Tag == "" {
		return nil, E.New("WireGuard outbound requires a tag")
	}
	if source.SendThrough != "" || hasJSONValue(source.ProxySettings) || source.StreamSettings != nil || source.Mux != nil {
		return nil, E.New("unsupported Xray WireGuard outbound options")
	}
	settings, err := decodeXrayObject[xrayWireGuardSettings](ctx, source.Settings, "settings")
	if err != nil {
		return nil, err
	}
	if !settings.NoKernelTun {
		return nil, E.New("Xray WireGuard kernel TUN mode is not supported")
	}
	if settings.SecretKey == "" || len(settings.Address) == 0 || len(settings.Peers) == 0 {
		return nil, E.New("incomplete Xray WireGuard settings")
	}
	switch strings.ToLower(settings.DomainStrategy) {
	case "", "forceip", "forceipv4", "forceipv6":
	default:
		return nil, unsupportedXrayField("settings.domainStrategy=" + settings.DomainStrategy)
	}
	peers := make([]map[string]any, 0, len(settings.Peers))
	for index, peer := range settings.Peers {
		host, portText, splitErr := net.SplitHostPort(peer.Endpoint)
		if splitErr != nil || host == "" {
			return nil, E.New("settings.peers[", index, "].endpoint must be host:port")
		}
		port, parseErr := strconv.ParseUint(portText, 10, 16)
		if parseErr != nil || port == 0 || peer.PublicKey == "" || len(peer.AllowedIPs) == 0 {
			return nil, E.New("incomplete settings.peers[", index, "]")
		}
		convertedPeer := map[string]any{
			"address": peerAddress(host), "port": port, "public_key": peer.PublicKey,
			"allowed_ips": peer.AllowedIPs,
		}
		if peer.PreShared != "" {
			convertedPeer["pre_shared_key"] = peer.PreShared
		}
		if peer.KeepAlive > 0 {
			convertedPeer["persistent_keepalive_interval"] = peer.KeepAlive
		}
		if len(settings.Reserved) > 0 {
			convertedPeer["reserved"] = settings.Reserved
		}
		peers = append(peers, convertedPeer)
	}
	result := map[string]any{
		"type": "wireguard", "tag": source.Tag, "address": settings.Address,
		"private_key": settings.SecretKey, "peers": peers,
	}
	if settings.MTU > 0 {
		result["mtu"] = settings.MTU
	}
	return result, nil
}

func peerAddress(host string) string {
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

func translateXrayVLESS(source xrayVLESSSettings) (map[string]any, error) {
	var address string
	var port uint16
	var user xrayVLESSUser
	if source.Address != nil {
		address = *source.Address
		port = source.Port
		user = xrayVLESSUser{
			Level:      source.Level,
			Email:      source.Email,
			ID:         source.ID,
			Flow:       source.Flow,
			Encryption: source.Encryption,
			Reverse:    source.Reverse,
			TestPre:    source.TestPre,
			TestSeed:   source.TestSeed,
		}
	} else {
		if len(source.VNext) != 1 {
			return nil, E.New("VLESS settings.vnext must contain exactly one server")
		}
		next := source.VNext[0]
		if next.Address == nil {
			return nil, E.New("VLESS settings.vnext[0].address is required")
		}
		if len(next.Users) != 1 {
			return nil, E.New("VLESS settings.vnext[0].users must contain exactly one user")
		}
		address = *next.Address
		port = next.Port
		user = next.Users[0]
	}
	if address == "" || port == 0 || user.ID == "" {
		return nil, E.New("VLESS server address, port, and user id are required")
	}
	if user.Level != 0 || user.Email != "" {
		return nil, E.New("VLESS user level/email metadata has no current Sidera equivalent")
	}
	if hasJSONValue(user.Reverse) || user.TestPre != 0 || len(user.TestSeed) > 0 {
		return nil, E.New("VLESS reverse/test fields are not supported")
	}
	if user.Encryption == "" {
		return nil, E.New("VLESS user encryption must be explicitly configured")
	}
	switch user.Flow {
	case "", "xtls-rprx-vision":
	case "xtls-rprx-vision-udp443":
		return nil, E.New("VLESS flow xtls-rprx-vision-udp443 cannot be preserved by the current data plane")
	default:
		return nil, E.New("unsupported VLESS flow: ", user.Flow)
	}
	result := map[string]any{
		"server":      address,
		"server_port": port,
		"uuid":        user.ID,
		"encryption":  user.Encryption,
	}
	if user.Flow != "" {
		result["flow"] = user.Flow
	}
	return result, nil
}

func translateXrayVLESSInboundStream(source *xrayStream) (map[string]any, error) {
	if source == nil {
		source = new(xrayStream)
	}
	if source.Address != nil || source.Port != 0 {
		return nil, E.New("streamSettings address/port overrides are not supported")
	}
	if err := rejectNonEmptyObject(source.FinalMask, "streamSettings.finalmask"); err != nil {
		return nil, err
	}
	if err := rejectNonEmptyObject(source.SocketSettings, "streamSettings.sockopt"); err != nil {
		return nil, err
	}
	network, err := xrayStreamNetwork(source)
	if err != nil {
		return nil, err
	}
	if network != "" && network != "raw" && network != "tcp" {
		return nil, E.New("Xray VLESS inbound transport is not implemented: ", network)
	}
	tcpSettings := source.TCPSettings
	if source.RawSettings != nil {
		tcpSettings = source.RawSettings
	}
	result := make(map[string]any)
	if tcpSettings != nil {
		if tcpSettings.AcceptProxyProtocol {
			result["proxy_protocol"] = true
			result["proxy_protocol_trusted_upstream"] = []string{"0.0.0.0/0", "::/0"}
		}
		if err = validateXrayTCPHeader(tcpSettings.Header); err != nil {
			return nil, err
		}
	}
	switch strings.ToLower(source.Security) {
	case "", "none":
	case "reality":
		tlsOptions, translateErr := translateXrayInboundReality(source.RealitySettings)
		if translateErr != nil {
			return nil, translateErr
		}
		result["tls"] = tlsOptions
	default:
		return nil, E.New("unsupported Xray VLESS inbound stream security: ", source.Security)
	}
	return result, nil
}

func xrayStreamNetwork(source *xrayStream) (string, error) {
	var network string
	if source.Network != nil {
		network = strings.ToLower(*source.Network)
		if network == "" {
			return "", E.New("streamSettings.network cannot be empty")
		}
	}
	if source.Method != nil {
		network = strings.ToLower(*source.Method)
		if network == "" {
			return "", E.New("streamSettings.method cannot be empty")
		}
	}
	return network, nil
}

func validateXrayTCPHeader(raw stdjson.RawMessage) error {
	if !hasJSONValue(raw) {
		return nil
	}
	var header map[string]stdjson.RawMessage
	if err := json.Unmarshal(raw, &header); err != nil {
		return E.Cause(err, "decode streamSettings raw header")
	}
	var headerType string
	if rawType, loaded := header["type"]; loaded {
		if err := json.Unmarshal(rawType, &headerType); err != nil {
			return E.Cause(err, "decode streamSettings raw header type")
		}
	}
	if headerType != "" && !strings.EqualFold(headerType, "none") {
		return unsupportedXrayField("streamSettings raw header type=" + headerType)
	}
	return nil
}

func translateXrayInboundReality(source *xrayReality) (map[string]any, error) {
	if source == nil {
		return nil, E.New("streamSettings.realitySettings is required")
	}
	if source.MasterKeyLog != "" || source.Type != "" && !strings.EqualFold(source.Type, "tcp") || source.XVer != 0 || source.MLDSA65Seed != "" || source.LimitFallbackUpload != (xrayLimitFallback{}) || source.LimitFallbackDownload != (xrayLimitFallback{}) {
		return nil, E.New("Xray REALITY server settings contain fields without a current Sidera equivalent")
	}
	if source.Fingerprint != "" || source.ServerName != "" || source.Password != "" || source.PublicKey != "" || source.ShortID != "" || source.MLDSA65Verify != "" || source.SpiderX != "" {
		return nil, E.New("Xray REALITY client settings cannot be used on an inbound")
	}
	if len(source.ServerNames) != 1 {
		return nil, E.New("Xray REALITY inbound currently requires exactly one serverName")
	}
	if source.PrivateKey == "" || len(source.ShortIDs) == 0 {
		return nil, E.New("Xray REALITY privateKey and shortIds are required")
	}
	target := source.Target
	if !hasJSONValue(target) {
		target = source.Dest
	}
	server, port, err := parseXrayRealityTarget(target)
	if err != nil {
		return nil, err
	}
	reality := map[string]any{
		"enabled": true,
		"handshake": map[string]any{
			"server":      server,
			"server_port": port,
		},
		"private_key": source.PrivateKey,
		"short_id":    []string(source.ShortIDs),
	}
	if source.MinClientVer != "" {
		reality["min_client_version"] = source.MinClientVer
	}
	if source.MaxClientVer != "" {
		reality["max_client_version"] = source.MaxClientVer
	}
	if source.MaxTimeDiff != 0 {
		reality["max_time_difference"] = strconv.FormatUint(source.MaxTimeDiff, 10) + "ms"
	}
	return map[string]any{
		"enabled":     true,
		"server_name": source.ServerNames[0],
		"reality":     reality,
	}, nil
}

func parseXrayRealityTarget(raw stdjson.RawMessage) (string, uint16, error) {
	if !hasJSONValue(raw) {
		return "", 0, E.New("Xray REALITY target is required")
	}
	var numericPort uint16
	if err := json.Unmarshal(raw, &numericPort); err == nil {
		if numericPort == 0 {
			return "", 0, E.New("Xray REALITY target port must be greater than zero")
		}
		return "localhost", numericPort, nil
	}
	var target string
	if err := json.Unmarshal(raw, &target); err != nil || target == "" {
		return "", 0, E.New("Xray REALITY target must be a TCP host and port")
	}
	if port, err := strconv.ParseUint(target, 10, 16); err == nil && port > 0 {
		return "localhost", uint16(port), nil
	}
	host, portString, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		return "", 0, E.New("Xray REALITY target must be a TCP host and port: ", target)
	}
	port, err := strconv.ParseUint(portString, 10, 16)
	if err != nil || port == 0 {
		return "", 0, E.New("invalid Xray REALITY target port: ", portString)
	}
	return host, uint16(port), nil
}

func validateFreedomSettings(ctx context.Context, source xrayFreedomSettings) error {
	strategy := source.TargetStrategy
	if strategy == "" {
		strategy = source.DomainStrategy
	}
	switch strings.ToLower(strategy) {
	case "", "asis":
	default:
		return unsupportedXrayField("settings.targetStrategy=" + strategy)
	}
	if source.Redirect != "" || source.UserLevel != 0 || source.ProxyProtocol != 0 || hasJSONValue(source.Fragment) || hasJSONValue(source.Noise) || hasJSONValue(source.Noises) || hasJSONValue(source.IPsBlocked) {
		return E.New("Xray direct outbound contains fields without a current Sidera equivalent")
	}
	if hasJSONValue(source.FinalRules) {
		var rules []struct {
			Action     string             `json:"action"`
			Network    stdjson.RawMessage `json:"network"`
			Port       stdjson.RawMessage `json:"port"`
			IP         stdjson.RawMessage `json:"ip"`
			BlockDelay stdjson.RawMessage `json:"blockDelay"`
		}
		if err := decodeXrayInto(ctx, source.FinalRules, "settings.finalRules", &rules); err != nil {
			return err
		}
		if len(rules) != 1 || !strings.EqualFold(rules[0].Action, "allow") || hasJSONValue(rules[0].Network) || hasJSONValue(rules[0].Port) || hasJSONValue(rules[0].IP) || hasJSONValue(rules[0].BlockDelay) {
			return E.New("only one unconditional Xray freedom finalRules allow rule is supported")
		}
	}
	return nil
}

func translateXrayStream(source *xrayStream) (map[string]any, error) {
	if source.Address != nil || source.Port != 0 {
		return nil, E.New("streamSettings address/port overrides are not supported")
	}
	if err := rejectNonEmptyObject(source.FinalMask, "streamSettings.finalmask"); err != nil {
		return nil, err
	}
	if err := rejectNonEmptyObject(source.SocketSettings, "streamSettings.sockopt"); err != nil {
		return nil, err
	}
	var network string
	if source.Network != nil {
		network = *source.Network
		if network == "" {
			return nil, E.New("streamSettings.network cannot be empty")
		}
	}
	if source.Method != nil {
		network = *source.Method
		if network == "" {
			return nil, E.New("streamSettings.method cannot be empty")
		}
	}
	result := make(map[string]any)
	normalizedNetwork := strings.ToLower(network)
	switch normalizedNetwork {
	case "", "raw", "tcp":
		tcpSettings := source.TCPSettings
		if source.RawSettings != nil {
			tcpSettings = source.RawSettings
		}
		if tcpSettings != nil {
			if tcpSettings.AcceptProxyProtocol {
				return nil, unsupportedXrayField("streamSettings.rawSettings.acceptProxyProtocol")
			}
			if hasJSONValue(tcpSettings.Header) {
				var header map[string]stdjson.RawMessage
				if err := json.Unmarshal(tcpSettings.Header, &header); err != nil {
					return nil, E.Cause(err, "decode streamSettings raw header")
				}
				var headerType string
				if rawType, loaded := header["type"]; loaded {
					if err := json.Unmarshal(rawType, &headerType); err != nil {
						return nil, E.Cause(err, "decode streamSettings raw header type")
					}
				}
				if headerType != "" && strings.ToLower(headerType) != "none" {
					return nil, unsupportedXrayField("streamSettings raw header type=" + headerType)
				}
			}
		}
	case "ws", "websocket":
		transport, err := translateXrayWebSocket(source.WSSettings)
		if err != nil {
			return nil, err
		}
		result["transport"] = transport
	case "grpc":
		transport, err := translateXrayGRPC(source.GRPCSettings)
		if err != nil {
			return nil, err
		}
		result["transport"] = transport
	case "httpupgrade":
		transport, err := translateXrayHTTPUpgrade(source.HTTPUpgradeSettings)
		if err != nil {
			return nil, err
		}
		result["transport"] = transport
	case "xhttp", "splithttp", "kcp", "mkcp", "quic", "hysteria", "h2", "h3", "http":
		return nil, E.New("Xray transport is not implemented: ", network)
	default:
		return nil, E.New("unknown Xray transport: ", network)
	}
	switch strings.ToLower(source.Security) {
	case "", "none":
	case "tls":
		tlsOptions, err := translateXrayTLS(source.TLSSettings)
		if err != nil {
			return nil, err
		}
		result["tls"] = tlsOptions
	case "reality":
		tlsOptions, err := translateXrayReality(source.RealitySettings)
		if err != nil {
			return nil, err
		}
		result["tls"] = tlsOptions
	default:
		return nil, E.New("unsupported Xray stream security: ", source.Security)
	}
	return result, nil
}

func translateXrayWebSocket(source *xrayWebSocket) (map[string]any, error) {
	if source == nil {
		source = new(xrayWebSocket)
	}
	if source.AcceptProxyProtocol {
		return nil, unsupportedXrayField("streamSettings.wsSettings.acceptProxyProtocol")
	}
	if source.HeartbeatPeriod != 0 {
		return nil, unsupportedXrayField("streamSettings.wsSettings.heartbeatPeriod")
	}
	path := source.Path
	var earlyData uint32
	if parsed, err := url.Parse(path); err == nil {
		query := parsed.Query()
		if value := query.Get("ed"); value != "" {
			parsedEarlyData, _ := strconv.Atoi(value)
			earlyData = uint32(parsedEarlyData)
			query.Del("ed")
			parsed.RawQuery = query.Encode()
			path = parsed.String()
		}
	}
	result := map[string]any{"type": "ws"}
	if path != "" {
		result["path"] = path
	}
	headers := cloneHeaders(source.Headers)
	var host string
	for name, value := range headers {
		if strings.EqualFold(name, "host") {
			host = value
			delete(headers, name)
		}
	}
	if source.Host != "" {
		host = source.Host
	}
	if host != "" {
		headers["Host"] = host
	}
	if len(headers) > 0 {
		result["headers"] = headers
	}
	if earlyData > 0 {
		result["max_early_data"] = earlyData
		result["early_data_header_name"] = "Sec-WebSocket-Protocol"
	}
	return result, nil
}

func translateXrayGRPC(source *xrayGRPC) (map[string]any, error) {
	if source == nil {
		source = new(xrayGRPC)
	}
	if source.Authority != "" || source.MultiMode || source.HealthCheckTimeout != 0 || source.InitialWindowsSize != 0 || source.UserAgent != "" {
		return nil, E.New("Xray gRPC settings contain fields without a current Sidera equivalent")
	}
	if strings.HasPrefix(source.ServiceName, "/") {
		return nil, E.New("Xray custom gRPC service paths have no current Sidera equivalent")
	}
	result := map[string]any{"type": "grpc"}
	if source.ServiceName != "" {
		result["service_name"] = source.ServiceName
	}
	if source.IdleTimeout > 0 {
		result["idle_timeout"] = strconv.FormatInt(int64(source.IdleTimeout), 10) + "s"
	}
	if source.PermitWithoutStream {
		result["permit_without_stream"] = true
	}
	return result, nil
}

func translateXrayHTTPUpgrade(source *xrayHTTPUpgrade) (map[string]any, error) {
	if source == nil {
		source = new(xrayHTTPUpgrade)
	}
	if source.AcceptProxyProtocol {
		return nil, unsupportedXrayField("streamSettings.httpupgradeSettings.acceptProxyProtocol")
	}
	if parsed, err := url.Parse(source.Path); err == nil && parsed.Query().Get("ed") != "" {
		return nil, E.New("HTTPUpgrade early data has no current Sidera equivalent")
	}
	result := map[string]any{"type": "httpupgrade"}
	if source.Host != "" {
		result["host"] = source.Host
	}
	if source.Path != "" {
		result["path"] = source.Path
	}
	if len(source.Headers) > 0 {
		for name := range source.Headers {
			if strings.EqualFold(name, "host") {
				return nil, E.New("httpupgradeSettings.headers cannot contain Host")
			}
		}
		result["headers"] = source.Headers
	}
	return result, nil
}

func translateXrayTLS(source *xrayTLS) (map[string]any, error) {
	if source == nil {
		source = new(xrayTLS)
	}
	if len(source.Certificates) > 0 || source.EnableSessionResumption || source.DisableSystemRoot || source.CipherSuites != "" || source.RejectUnknownSNI || len(source.CurvePreferences) > 0 || source.MasterKeyLog != "" || source.PinnedPeerCertSHA256 != "" || source.VerifyPeerCertByName != "" || source.ECHServerKeys != "" || source.ECHConfigList != "" || hasJSONValue(source.ECHSocketSettings) {
		return nil, E.New("Xray TLS settings contain fields without a current Sidera equivalent")
	}
	if source.AllowInsecure {
		return nil, E.New("Xray allowInsecure has been removed and cannot be translated")
	}
	result := map[string]any{"enabled": true}
	if source.ServerName != "" {
		result["server_name"] = source.ServerName
	}
	if len(source.ALPN) > 0 {
		result["alpn"] = []string(source.ALPN)
	}
	if isXrayTLSVersion(source.MinVersion) {
		result["min_version"] = source.MinVersion
	}
	if isXrayTLSVersion(source.MaxVersion) {
		result["max_version"] = source.MaxVersion
	}
	utlsOptions, err := translateXrayFingerprint(source.Fingerprint, true)
	if err != nil {
		return nil, err
	}
	if utlsOptions != nil {
		result["utls"] = utlsOptions
	}
	return result, nil
}

func translateXrayReality(source *xrayReality) (map[string]any, error) {
	if source == nil {
		return nil, E.New("streamSettings.realitySettings is required")
	}
	if source.MasterKeyLog != "" || source.Show || hasJSONValue(source.Target) || hasJSONValue(source.Dest) || source.Type != "" || source.XVer != 0 || len(source.ServerNames) > 0 || source.PrivateKey != "" || source.MinClientVer != "" || source.MaxClientVer != "" || source.MaxTimeDiff != 0 || len(source.ShortIDs) > 0 || source.MLDSA65Seed != "" || source.LimitFallbackUpload != (xrayLimitFallback{}) || source.LimitFallbackDownload != (xrayLimitFallback{}) {
		return nil, E.New("Xray REALITY server settings cannot be used on an outbound")
	}
	if source.MLDSA65Verify != "" {
		return nil, E.New("REALITY ML-DSA verification is not implemented")
	}
	if source.SpiderX != "" && source.SpiderX != "/" {
		return nil, E.New("REALITY spiderX has no current Sidera equivalent")
	}
	publicKey := source.PublicKey
	if source.Password != "" {
		publicKey = source.Password
	}
	if publicKey == "" {
		return nil, E.New("REALITY password/publicKey is required")
	}
	result := map[string]any{
		"enabled": true,
		"reality": map[string]any{
			"enabled":    true,
			"public_key": publicKey,
			"short_id":   source.ShortID,
		},
	}
	if source.ServerName != "" {
		result["server_name"] = source.ServerName
	}
	utlsOptions, err := translateXrayFingerprint(source.Fingerprint, false)
	if err != nil {
		return nil, err
	}
	if utlsOptions != nil {
		result["utls"] = utlsOptions
	}
	return result, nil
}

func isXrayTLSVersion(version string) bool {
	switch version {
	case "1.0", "1.1", "1.2", "1.3":
		return true
	default:
		return false
	}
}

func translateXrayFingerprint(fingerprint string, allowStandardTLS bool) (map[string]any, error) {
	fingerprint = strings.ToLower(fingerprint)
	if fingerprint == "unsafe" {
		if allowStandardTLS {
			return nil, nil
		}
		return nil, E.New("Xray REALITY does not support fingerprint unsafe")
	}
	if fingerprint == "" {
		fingerprint = "chrome"
	}
	switch fingerprint {
	case "chrome", "firefox", "edge", "safari", "360", "qq", "ios", "android", "random", "randomized":
	default:
		return nil, E.New("Xray fingerprint has no current Sidera equivalent: ", fingerprint)
	}
	return map[string]any{
		"enabled":     true,
		"fingerprint": fingerprint,
	}, nil
}

func translateXrayRouting(ctx context.Context, source *xrayRouting, excluded map[int]bool) ([]map[string]any, error) {
	if source == nil {
		return nil, nil
	}
	switch strings.ToLower(source.DomainStrategy) {
	case "", "asis":
	default:
		return nil, E.New("Xray routing.domainStrategy is not implemented: ", source.DomainStrategy)
	}
	if hasJSONValue(source.Balancers) {
		return nil, unsupportedXrayField("routing.balancers")
	}
	rules := make([]map[string]any, 0, len(source.Rules))
	for index, rawRule := range source.Rules {
		if excluded[index] {
			continue
		}
		rule, err := decodeXrayObject[xrayRoutingRule](ctx, rawRule, "routing.rules["+strconv.Itoa(index)+"]")
		if err != nil {
			return nil, err
		}
		converted, err := translateXrayRoutingRule(rule)
		if err != nil {
			return nil, E.Cause(err, "routing.rules[", index, "]")
		}
		rules = append(rules, converted)
	}
	return rules, nil
}

func translateXrayRoutingRule(source xrayRoutingRule) (map[string]any, error) {
	if source.Type != "" && strings.ToLower(source.Type) != "field" {
		return nil, E.New("unsupported rule type: ", source.Type)
	}
	if source.RuleTag != "" || source.BalancerTag != "" || hasJSONValue(source.VLESSRoute) || len(source.Attributes) > 0 || len(source.LocalIP) > 0 || hasJSONValue(source.LocalPort) || len(source.Process) > 0 || hasJSONValue(source.Webhook) {
		return nil, E.New("rule contains fields without a current Sidera equivalent")
	}
	if source.OutboundTag == "" {
		return nil, E.New("outboundTag is required")
	}
	result := map[string]any{"outbound": source.OutboundTag}
	if len(source.InboundTag) > 0 {
		result["inbound"] = []string(source.InboundTag)
	}
	if len(source.Network) > 0 {
		var networks []string
		for _, item := range source.Network {
			network := strings.ToLower(item)
			if network != "tcp" && network != "udp" {
				return nil, E.New("unsupported network: ", network)
			}
			networks = append(networks, network)
		}
		result["network"] = networks
	}
	if len(source.Protocols) > 0 {
		result["protocol"] = []string(source.Protocols)
	}
	if len(source.User) > 0 {
		var users []string
		for _, user := range source.User {
			if user == "" {
				continue
			}
			if strings.HasPrefix(user, "regexp:") {
				if _, err := regexp.Compile(strings.TrimPrefix(user, "regexp:")); err == nil {
					return nil, E.New("Xray regexp user rules have no current Sidera equivalent")
				}
				continue
			}
			users = append(users, user)
		}
		if len(users) == 0 {
			return nil, E.New("Xray user rule has no translatable matchers")
		}
		result["auth_user"] = users
	}
	if err := appendPortMatch(result, "port", "port_range", source.Port); err != nil {
		return nil, E.Cause(err, "port")
	}
	if err := appendPortMatch(result, "source_port", "source_port_range", source.SourcePort); err != nil {
		return nil, E.Cause(err, "sourcePort")
	}
	domains := source.Domain
	if source.Domains != nil {
		domains = *source.Domains
	}
	if err := appendDomainMatch(result, domains); err != nil {
		return nil, err
	}
	if err := appendIPMatch(result, "ip_cidr", source.IP); err != nil {
		return nil, E.Cause(err, "ip")
	}
	sourceIPs := source.Source
	if source.SourceIP != nil {
		sourceIPs = *source.SourceIP
	}
	if err := appendIPMatch(result, "source_ip_cidr", sourceIPs); err != nil {
		return nil, E.Cause(err, "sourceIP")
	}
	if len(result) == 1 {
		return nil, E.New("this rule has no effective fields")
	}
	return result, nil
}

func appendDomainMatch(result map[string]any, domains []string) error {
	var full, suffix, keyword, regex []string
	for _, domain := range domains {
		if domain == "" {
			return E.New("empty domain rule")
		}
		switch {
		case strings.HasPrefix(domain, "full:"):
			full = append(full, strings.TrimPrefix(domain, "full:"))
		case strings.HasPrefix(domain, "domain:"):
			suffix = append(suffix, strings.TrimPrefix(domain, "domain:"))
		case strings.HasPrefix(domain, "keyword:"):
			keyword = append(keyword, strings.TrimPrefix(domain, "keyword:"))
		case strings.HasPrefix(domain, "regexp:"):
			regex = append(regex, strings.TrimPrefix(domain, "regexp:"))
		case strings.HasPrefix(domain, "dotless:"):
			substring := strings.TrimPrefix(domain, "dotless:")
			if strings.Contains(substring, ".") {
				return E.New("substring in dotless rule must not contain a dot")
			}
			if substring == "" {
				regex = append(regex, "^[^.]*$")
			} else {
				regex = append(regex, "^[^.]*"+substring+"[^.]*$")
			}
		case strings.HasPrefix(domain, "geosite:") || strings.HasPrefix(domain, "ext:"):
			return E.New("geodata domain rule is not implemented: ", domain)
		default:
			keyword = append(keyword, domain)
		}
	}
	if len(full) > 0 {
		result["domain"] = full
	}
	if len(suffix) > 0 {
		result["domain_suffix"] = suffix
	}
	if len(keyword) > 0 {
		result["domain_keyword"] = keyword
	}
	if len(regex) > 0 {
		result["domain_regex"] = regex
	}
	return nil
}

func appendIPMatch(result map[string]any, field string, values []string) error {
	var translated []string
	for _, value := range values {
		lowerValue := strings.ToLower(value)
		if strings.HasPrefix(value, "!") {
			return E.New("negated IP rules have no current Sidera equivalent: ", value)
		}
		if lowerValue == "geoip:private" {
			for _, prefix := range xrayPrivatePrefixes {
				translated = append(translated, prefix.String())
			}
			continue
		}
		if strings.HasPrefix(lowerValue, "geoip:") || strings.HasPrefix(lowerValue, "ext:") {
			return E.New("geodata IP rule is not implemented: ", value)
		}
		translated = append(translated, value)
	}
	if len(translated) > 0 {
		result[field] = translated
	}
	return nil
}

func appendPortMatch(result map[string]any, portField, rangeField string, raw stdjson.RawMessage) error {
	if !hasJSONValue(raw) {
		return nil
	}
	var number uint16
	if err := json.Unmarshal(raw, &number); err == nil {
		result[portField] = number
		return nil
	}
	var expression string
	if err := json.Unmarshal(raw, &expression); err != nil {
		return E.New("port list must be a number or string")
	}
	var ports []uint16
	var ranges []string
	for _, item := range strings.Split(expression, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.Contains(item, "-") {
			parts := strings.Split(item, "-")
			if len(parts) != 2 {
				return E.New("invalid port range: ", item)
			}
			from, fromErr := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 16)
			to, toErr := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 16)
			if fromErr != nil || toErr != nil || from == 0 || from > to {
				return E.New("invalid port range: ", item)
			}
			ranges = append(ranges, strconv.FormatUint(from, 10)+":"+strconv.FormatUint(to, 10))
			continue
		}
		value, err := strconv.ParseUint(item, 10, 16)
		if err != nil || value == 0 {
			return E.New("invalid port: ", item)
		}
		ports = append(ports, uint16(value))
	}
	if len(ports) > 0 {
		result[portField] = ports
	}
	if len(ranges) > 0 {
		result[rangeField] = ranges
	}
	if len(ports) == 0 && len(ranges) == 0 {
		return E.New("empty port expression has no exact Sidera equivalent")
	}
	return nil
}

func parseSinglePort(raw stdjson.RawMessage) (uint16, error) {
	if !hasJSONValue(raw) {
		return 0, E.New("missing port")
	}
	var port uint16
	if err := json.Unmarshal(raw, &port); err == nil {
		if port == 0 {
			return 0, E.New("port must be greater than zero")
		}
		return port, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, E.New("port must be a single number")
	}
	parsed, err := strconv.ParseUint(value, 10, 16)
	if err != nil || parsed == 0 {
		return 0, E.New("port ranges cannot be represented by one Sidera inbound")
	}
	return uint16(parsed), nil
}

func cloneHeaders(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	return result
}

var xrayPrivatePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/3"),
	netip.MustParsePrefix("::/127"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func isXrayPrivateAddress(address string) bool {
	if ip, err := netip.ParseAddr(strings.Trim(address, "[]")); err == nil {
		ip = ip.Unmap().WithZone("")
		for _, prefix := range xrayPrivatePrefixes {
			if prefix.Contains(ip) {
				return true
			}
		}
		return false
	}
	domain := strings.TrimSuffix(strings.ToLower(address), ".")
	if !strings.Contains(domain, ".") {
		return true
	}
	for _, suffix := range []string{"lan", "localdomain", "example", "invalid", "localhost", "test", "local", "home.arpa", "internal"} {
		if domain == suffix || strings.HasSuffix(domain, "."+suffix) {
			return true
		}
	}
	return false
}

func rejectNonEmptyObject(raw stdjson.RawMessage, field string) error {
	if !hasJSONValue(raw) {
		return nil
	}
	var object map[string]stdjson.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return E.Cause(err, "decode ", field)
	}
	if len(object) > 0 {
		return unsupportedXrayField(field)
	}
	return nil
}

func decodeXrayObject[T any](ctx context.Context, content []byte, path string) (T, error) {
	var value T
	if !hasJSONValue(content) {
		content = []byte("{}")
	}
	err := decodeXrayInto(ctx, content, path, &value)
	return value, err
}

func decodeXrayInto(ctx context.Context, content []byte, path string, value any) error {
	decoder := json.NewDecoderContext(ctx, bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return E.Cause(err, "decode Xray ", path)
	}
	return nil
}

func unsupportedXrayField(field string) error {
	return E.New("Xray field is not supported: ", field)
}
