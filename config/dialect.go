package config

import (
	stdjson "encoding/json"
	"sort"
	"strconv"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
)

type Dialect string

const (
	DialectSingBox Dialect = "sing-box"
	DialectXray    Dialect = "xray"
)

var (
	singBoxRootFields = []string{
		"$schema",
		"ntp",
		"certificate",
		"certificate_providers",
		"http_clients",
		"network_namespaces",
		"endpoints",
		"route",
		"services",
		"experimental",
	}
	xrayRootFields = []string{
		"routing",
		"policy",
		"api",
		"metrics",
		"stats",
		"reverse",
		"fakeDns",
		"observatory",
		"burstObservatory",
		"version",
		"transport",
		"env",
		"geodata",
	}
)

func Detect(content []byte) (Dialect, error) {
	root, err := json.UnmarshalExtended[map[string]stdjson.RawMessage](content)
	if err != nil {
		return "", E.Cause(err, "detect config dialect")
	}
	var singBoxEvidence []string
	var xrayEvidence []string
	for _, field := range singBoxRootFields {
		if hasJSONField(root, field) {
			singBoxEvidence = append(singBoxEvidence, field)
		}
	}
	for _, field := range xrayRootFields {
		if hasJSONField(root, field) {
			xrayEvidence = append(xrayEvidence, field)
		}
	}
	inspectEndpointEvidence(root["inbounds"], "inbounds", &singBoxEvidence, &xrayEvidence)
	inspectEndpointEvidence(root["outbounds"], "outbounds", &singBoxEvidence, &xrayEvidence)
	inspectObjectEvidence(root["log"], "log", []string{"disabled", "level", "output", "timestamp"}, []string{"access", "error", "loglevel", "dnsLog", "maskAddress"}, &singBoxEvidence, &xrayEvidence)
	inspectObjectEvidence(root["dns"], "dns", []string{"rules", "final", "strategy", "disable_cache", "disable_expire", "independent_cache", "reverse_mapping", "client_subnet"}, []string{"hosts", "clientIp", "tag", "queryStrategy", "disableCache", "serveStale", "serveExpiredTTL", "disableFallback", "disableFallbackIfMatch", "enableParallelQuery", "useSystemHosts"}, &singBoxEvidence, &xrayEvidence)
	if len(singBoxEvidence) > 0 && len(xrayEvidence) > 0 {
		sort.Strings(singBoxEvidence)
		sort.Strings(xrayEvidence)
		return "", E.New("ambiguous config dialect: sing-box fields [", strings.Join(singBoxEvidence, ", "), "] conflict with Xray fields [", strings.Join(xrayEvidence, ", "), "]")
	}
	if len(xrayEvidence) > 0 {
		return DialectXray, nil
	}
	return DialectSingBox, nil
}

func inspectEndpointEvidence(raw stdjson.RawMessage, field string, singBoxEvidence, xrayEvidence *[]string) {
	if !hasJSONValue(raw) {
		return
	}
	var entries []map[string]stdjson.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return
	}
	for index, entry := range entries {
		hasType := hasJSONField(entry, "type")
		if hasType {
			*singBoxEvidence = append(*singBoxEvidence, field+"["+strconv.Itoa(index)+"].type")
		}
		if hasJSONField(entry, "protocol") && !isNativeEndpointProtocol(entry, field) {
			*xrayEvidence = append(*xrayEvidence, field+"["+strconv.Itoa(index)+"].protocol")
		}
	}
}

func isNativeEndpointProtocol(entry map[string]stdjson.RawMessage, field string) bool {
	rawType, loaded := entry["type"]
	if !loaded {
		return false
	}
	var endpointType string
	if err := json.Unmarshal(rawType, &endpointType); err != nil {
		return false
	}
	return (field == "inbounds" && endpointType == "cloudflared") ||
		(field == "outbounds" && endpointType == "shadowsocksr")
}

func inspectObjectEvidence(raw stdjson.RawMessage, field string, singBoxFields, xrayFields []string, singBoxEvidence, xrayEvidence *[]string) {
	if !hasJSONValue(raw) {
		return
	}
	var object map[string]stdjson.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return
	}
	for _, name := range singBoxFields {
		if hasJSONField(object, name) {
			*singBoxEvidence = append(*singBoxEvidence, field+"."+name)
		}
	}
	for _, name := range xrayFields {
		if hasJSONField(object, name) {
			*xrayEvidence = append(*xrayEvidence, field+"."+name)
		}
	}
}

func hasJSONField(object map[string]stdjson.RawMessage, field string) bool {
	raw, loaded := object[field]
	return loaded && hasJSONValue(raw)
}

func hasJSONValue(raw stdjson.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	return value != "" && value != "null"
}
