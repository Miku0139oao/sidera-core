package config

import (
	"context"
	stdjson "encoding/json"
	"strconv"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

type xrayHysteriaInboundSettings struct {
	Version int32               `json:"version"`
	Users   []xrayHysteriaUser  `json:"users"`
	Clients *[]xrayHysteriaUser `json:"clients"`
}

type xrayHysteriaUser struct {
	Auth  string `json:"auth"`
	Level uint32 `json:"level"`
	Email string `json:"email"`
}

type xrayHysteriaStreamSettings struct {
	Version        int32                  `json:"version"`
	Auth           string                 `json:"auth"`
	Congestion     *string                `json:"congestion"`
	Up             *string                `json:"up"`
	Down           *string                `json:"down"`
	UDPHop         stdjson.RawMessage     `json:"udphop"`
	UDPIdleTimeout int64                  `json:"udpIdleTimeout"`
	Masquerade     xrayHysteriaMasquerade `json:"masquerade"`
}

type xrayHysteriaMasquerade struct {
	Type        string            `json:"type"`
	Directory   string            `json:"dir"`
	URL         string            `json:"url"`
	RewriteHost bool              `json:"rewriteHost"`
	Insecure    bool              `json:"insecure"`
	Content     string            `json:"content"`
	Headers     map[string]string `json:"headers"`
	StatusCode  int32             `json:"statusCode"`
}

type xrayFinalMask struct {
	TCP        []stdjson.RawMessage `json:"tcp"`
	UDP        []stdjson.RawMessage `json:"udp"`
	QUICParams *xrayQUICParams      `json:"quicParams"`
}

type xrayQUICParams struct {
	Congestion                     string         `json:"congestion"`
	Debug                          bool           `json:"debug"`
	BBRProfile                     string         `json:"bbrProfile"`
	BrutalUp                       string         `json:"brutalUp"`
	BrutalDown                     string         `json:"brutalDown"`
	UDPHop                         xrayQUICUDPHop `json:"udpHop"`
	InitialStreamReceiveWindow     uint64         `json:"initStreamReceiveWindow"`
	MaxStreamReceiveWindow         uint64         `json:"maxStreamReceiveWindow"`
	InitialConnectionReceiveWindow uint64         `json:"initConnectionReceiveWindow"`
	MaxConnectionReceiveWindow     uint64         `json:"maxConnectionReceiveWindow"`
	MaxIdleTimeout                 int64          `json:"maxIdleTimeout"`
	KeepAlivePeriod                int64          `json:"keepAlivePeriod"`
	DisablePathMTUDiscovery        bool           `json:"disablePathMTUDiscovery"`
	MaxIncomingStreams             int64          `json:"maxIncomingStreams"`
}

type xrayQUICUDPHop struct {
	Ports    stdjson.RawMessage `json:"ports"`
	Interval stdjson.RawMessage `json:"interval"`
}

func translateXrayHysteria2Inbound(ctx context.Context, source xrayInbound) (map[string]any, error) {
	settings, err := decodeXrayObject[xrayHysteriaInboundSettings](ctx, source.Settings, "settings")
	if err != nil {
		return nil, err
	}
	if settings.Version != 2 {
		return nil, E.New("Xray Hysteria inbound settings.version must be 2")
	}
	if source.StreamSettings == nil {
		return nil, E.New("Xray Hysteria inbound streamSettings is required")
	}
	stream := source.StreamSettings
	if stream.Address != nil || stream.Port != 0 {
		return nil, E.New("streamSettings address/port overrides are not supported")
	}
	if err = rejectNonEmptyObject(stream.SocketSettings, "streamSettings.sockopt"); err != nil {
		return nil, err
	}
	network, err := xrayStreamNetwork(stream)
	if err != nil {
		return nil, err
	}
	if network != "hysteria" {
		return nil, E.New("Xray Hysteria inbound requires streamSettings.network or method hysteria")
	}
	if stream.RawSettings != nil || stream.TCPSettings != nil || hasJSONValue(stream.XHTTPSettings) || hasJSONValue(stream.SplitHTTPSettings) || hasJSONValue(stream.KCPSettings) || stream.GRPCSettings != nil || stream.WSSettings != nil || stream.HTTPUpgradeSettings != nil || stream.RealitySettings != nil {
		return nil, E.New("Xray Hysteria inbound contains unrelated stream settings")
	}
	if !strings.EqualFold(stream.Security, "tls") || stream.TLSSettings == nil {
		return nil, E.New("Xray Hysteria2 inbound requires TLS")
	}
	hysteriaSettings, err := decodeXrayObject[xrayHysteriaStreamSettings](ctx, stream.HysteriaSettings, "streamSettings.hysteriaSettings")
	if err != nil {
		return nil, err
	}
	if hysteriaSettings.Version != 2 {
		return nil, E.New("streamSettings.hysteriaSettings.version must be 2")
	}
	if hysteriaSettings.Congestion != nil || hysteriaSettings.Up != nil || hysteriaSettings.Down != nil || hasJSONValue(hysteriaSettings.UDPHop) {
		return nil, E.New("legacy Hysteria congestion/up/down/udphop fields cannot be translated")
	}
	if hysteriaSettings.Masquerade.Type != "" || hysteriaSettings.Masquerade.Directory != "" || hysteriaSettings.Masquerade.URL != "" || hysteriaSettings.Masquerade.RewriteHost || hysteriaSettings.Masquerade.Insecure || hysteriaSettings.Masquerade.Content != "" || len(hysteriaSettings.Masquerade.Headers) > 0 || hysteriaSettings.Masquerade.StatusCode != 0 {
		return nil, E.New("Xray Hysteria masquerade is not implemented")
	}

	users := settings.Users
	if settings.Clients != nil {
		users = *settings.Clients
	}
	convertedUsers := make([]map[string]any, 0, len(users)+1)
	for index, user := range users {
		if user.Level != 0 {
			return nil, E.New("Hysteria user ", index, " level has no current Sidera equivalent")
		}
		if user.Auth == "" {
			return nil, E.New("Hysteria user ", index, " auth is required")
		}
		convertedUsers = append(convertedUsers, map[string]any{
			"name":     user.Email,
			"password": user.Auth,
		})
	}
	if len(convertedUsers) == 0 {
		if hysteriaSettings.Auth == "" {
			return nil, E.New("Xray Hysteria inbound requires users or streamSettings.hysteriaSettings.auth")
		}
		convertedUsers = append(convertedUsers, map[string]any{"password": hysteriaSettings.Auth})
	}

	finalMask, err := decodeXrayObject[xrayFinalMask](ctx, stream.FinalMask, "streamSettings.finalmask")
	if err != nil {
		return nil, err
	}
	if len(finalMask.TCP) > 0 || len(finalMask.UDP) > 0 {
		return nil, E.New("Xray finalmask TCP/UDP masks are not implemented")
	}
	quicOptions, err := translateXrayHysteriaQUIC(finalMask.QUICParams)
	if err != nil {
		return nil, err
	}
	tlsOptions, err := translateXrayInboundTLS(stream.TLSSettings)
	if err != nil {
		return nil, err
	}
	udpIdleTimeout := hysteriaSettings.UDPIdleTimeout
	if udpIdleTimeout == 0 {
		udpIdleTimeout = 60
	}
	if udpIdleTimeout < 2 || udpIdleTimeout > 600 {
		return nil, E.New("Hysteria udpIdleTimeout must be between 2 and 600 seconds")
	}
	result := map[string]any{
		"type":        "hysteria2",
		"users":       convertedUsers,
		"udp_timeout": strconv.FormatInt(udpIdleTimeout, 10) + "s",
		"tls":         tlsOptions,
	}
	for key, value := range quicOptions {
		result[key] = value
	}
	return result, nil
}

func translateXrayHysteriaQUIC(source *xrayQUICParams) (map[string]any, error) {
	if source == nil {
		source = new(xrayQUICParams)
	}
	if source.Debug {
		return nil, E.New("Xray global Hysteria debug mode has no exact Sidera equivalent")
	}
	if hasJSONValue(source.UDPHop.Ports) || hasJSONValue(source.UDPHop.Interval) {
		return nil, E.New("Xray Hysteria UDP port hopping is not implemented")
	}
	if source.Congestion != "" && !strings.EqualFold(source.Congestion, "bbr") {
		return nil, E.New("only Xray Hysteria congestion=bbr is currently supported")
	}
	if source.BrutalUp != "" || source.BrutalDown != "" {
		return nil, E.New("Xray Hysteria brutal bandwidth is incompatible with congestion=bbr")
	}
	profile := strings.ToLower(source.BBRProfile)
	if profile == "" {
		profile = "standard"
	}
	switch profile {
	case "standard", "conservative", "aggressive":
	default:
		return nil, E.New("unknown Xray Hysteria BBR profile: ", source.BBRProfile)
	}
	initialStreamWindow := source.InitialStreamReceiveWindow
	if initialStreamWindow == 0 {
		initialStreamWindow = 8388608
	}
	maxStreamWindow := source.MaxStreamReceiveWindow
	if maxStreamWindow == 0 {
		maxStreamWindow = 8388608
	}
	if initialStreamWindow != maxStreamWindow {
		return nil, E.New("Xray initial and maximum stream receive windows cannot be represented separately")
	}
	initialConnectionWindow := source.InitialConnectionReceiveWindow
	if initialConnectionWindow == 0 {
		initialConnectionWindow = 20971520
	}
	maxConnectionWindow := source.MaxConnectionReceiveWindow
	if maxConnectionWindow == 0 {
		maxConnectionWindow = 20971520
	}
	if initialConnectionWindow != maxConnectionWindow {
		return nil, E.New("Xray initial and maximum connection receive windows cannot be represented separately")
	}
	idleTimeout := source.MaxIdleTimeout
	if idleTimeout == 0 {
		idleTimeout = 30
	}
	if idleTimeout < 4 || idleTimeout > 120 {
		return nil, E.New("Xray Hysteria maxIdleTimeout must be between 4 and 120 seconds")
	}
	// Xray validates but does not apply this field on inbound listeners. Sidera's
	// transport currently has a fixed 10-second default, so accept only that value.
	if source.KeepAlivePeriod != 0 && source.KeepAlivePeriod != 10 {
		return nil, E.New("Xray Hysteria keepAlivePeriod cannot be preserved unless it is 10 seconds")
	}
	keepAlivePeriod := int64(10)
	maxIncomingStreams := source.MaxIncomingStreams
	if maxIncomingStreams == 0 {
		maxIncomingStreams = 1024
	}
	if maxIncomingStreams < 8 || strconv.IntSize == 32 && maxIncomingStreams > 1<<31-1 {
		return nil, E.New("Xray Hysteria maxIncomingStreams is out of range")
	}
	return map[string]any{
		"ignore_client_bandwidth":    true,
		"bbr_profile":                profile,
		"stream_receive_window":      initialStreamWindow,
		"connection_receive_window":  initialConnectionWindow,
		"idle_timeout":               strconv.FormatInt(idleTimeout, 10) + "s",
		"keep_alive_period":          strconv.FormatInt(keepAlivePeriod, 10) + "s",
		"max_concurrent_streams":     int(maxIncomingStreams),
		"disable_path_mtu_discovery": source.DisablePathMTUDiscovery,
	}, nil
}

func translateXrayInboundTLS(source *xrayTLS) (map[string]any, error) {
	if source == nil {
		return nil, E.New("tlsSettings is required")
	}
	if source.AllowInsecure || source.DisableSystemRoot || source.CipherSuites != "" || source.Fingerprint != "" || source.RejectUnknownSNI || len(source.CurvePreferences) > 0 || source.MasterKeyLog != "" || source.PinnedPeerCertSHA256 != "" || source.VerifyPeerCertByName != "" || source.ECHServerKeys != "" || source.ECHConfigList != "" || hasJSONValue(source.ECHSocketSettings) {
		return nil, E.New("Xray inbound TLS settings contain fields without a current Sidera equivalent")
	}
	if len(source.Certificates) != 1 {
		return nil, E.New("Xray inbound TLS currently requires exactly one certificate")
	}
	certificate := source.Certificates[0]
	if certificate.Usage != "" && !strings.EqualFold(certificate.Usage, "encipherment") || certificate.OneTimeLoading || certificate.BuildChain {
		return nil, E.New("unsupported Xray inbound TLS certificate mode")
	}
	result := map[string]any{
		"enabled":                 true,
		"disable_session_tickets": !source.EnableSessionResumption,
	}
	if certificate.CertificateFile != "" {
		result["certificate_path"] = certificate.CertificateFile
	} else if len(certificate.Certificate) > 0 {
		result["certificate"] = certificate.Certificate
	} else {
		return nil, E.New("Xray inbound TLS certificate is required")
	}
	if certificate.KeyFile != "" {
		result["key_path"] = certificate.KeyFile
	} else if len(certificate.Key) > 0 {
		result["key"] = certificate.Key
	} else {
		return nil, E.New("Xray inbound TLS private key is required")
	}
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
	return result, nil
}
