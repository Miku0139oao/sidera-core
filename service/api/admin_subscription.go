package api

import (
	"context"
	"crypto/ecdh"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/option"
	E "github.com/sagernet/sing/common/exceptions"
	SJSON "github.com/sagernet/sing/common/json"

	"github.com/go-chi/chi/v5"
)

const subscriptionTokenBytes = 32

func validExternalSubscriptionID(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validateSubscriptionBaseURL(value string) error {
	if value == "" {
		return nil
	}
	if strings.TrimSpace(value) != value {
		return E.New("dashboard public_base_url must be an HTTPS origin without a port")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return E.New("dashboard public_base_url must be an HTTPS origin without a port, path, query, userinfo, or fragment")
	}
	hostname := parsed.Hostname()
	hostWithoutPort := hostname
	if strings.Contains(hostname, ":") {
		hostWithoutPort = "[" + hostname + "]"
	}
	if !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || hostname == "" || parsed.Host != hostWithoutPort || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" {
		return E.New("dashboard public_base_url must be an HTTPS origin without a port, path, query, userinfo, or fragment")
	}
	return nil
}

func (a *adminAPI) ensureSubscriptionTokens() error {
	a.storeAccess.Lock()
	defer a.storeAccess.Unlock()
	return a.ensureSubscriptionTokensLocked()
}

func (a *adminAPI) ensureSubscriptionTokensLocked() error {
	names := make(map[string]bool)
	for _, inbound := range a.store.Inbounds {
		if inbound == nil {
			continue
		}
		for _, user := range inbound.Users {
			if user != nil {
				names[user.Name] = true
			}
		}
	}
	if a.store.Subscriptions == nil {
		a.store.Subscriptions = make(map[string]string)
	}
	for name := range a.store.Subscriptions {
		if !names[name] {
			delete(a.store.Subscriptions, name)
		}
	}
	orderedNames := make([]string, 0, len(names))
	for name := range names {
		orderedNames = append(orderedNames, name)
	}
	sort.Strings(orderedNames)
	used := make(map[string]bool, len(orderedNames))
	for _, name := range orderedNames {
		token := a.store.Subscriptions[name]
		if validSubscriptionToken(token) && !used[token] {
			used[token] = true
			continue
		}
		for {
			var err error
			token, err = randomAdminSecret(subscriptionTokenBytes)
			if err != nil {
				return err
			}
			if !used[token] {
				break
			}
		}
		a.store.Subscriptions[name] = token
		used[token] = true
	}
	return nil
}

func validSubscriptionToken(token string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == subscriptionTokenBytes && base64.RawURLEncoding.EncodeToString(decoded) == token
}

func (a *adminAPI) getSubscription(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if a.publicBaseURL == "" {
		http.NotFound(writer, request)
		return
	}
	token := request.PathValue("token")
	if token == "" {
		token = chi.URLParam(request, "token")
	}
	a.storeAccess.RLock()
	name, found := a.subscriptionNameLocked(token)
	a.storeAccess.RUnlock()
	if !found {
		http.NotFound(writer, request)
		return
	}
	active := a.activeUsage()
	now := time.Now().UnixMilli()

	a.storeAccess.RLock()
	currentName, current := a.subscriptionNameLocked(token)
	if !current || currentName != name {
		a.storeAccess.RUnlock()
		http.NotFound(writer, request)
		return
	}
	links := a.subscriptionLinksLocked(name, now, active)
	profilePagePath := a.profilePagePathLocked()
	a.storeAccess.RUnlock()
	if len(links) == 0 {
		http.NotFound(writer, request)
		return
	}
	sort.Strings(links)
	body := base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
	writer.Header().Set("Profile-Update-Interval", "12")
	writer.Header().Set("Profile-Web-Page-Url", a.publicBaseURL+profilePagePath+url.PathEscape(token))
	upload, download, total, expire := a.subscriptionUsageLocked(name, active)
	writer.Header().Set("Subscription-Userinfo", "upload="+strconv.FormatInt(upload, 10)+"; download="+strconv.FormatInt(download, 10)+"; total="+strconv.FormatInt(total, 10)+"; expire="+strconv.FormatInt(expire, 10))
	if request.Method == http.MethodGet {
		_, _ = writer.Write([]byte(body))
	}
}

func (a *adminAPI) subscriptionUsageLocked(name string, active map[string]adminUsage) (upload, download, total, expire int64) {
	unlimited := false
	for tag, record := range a.store.Inbounds {
		if record == nil {
			continue
		}
		for _, user := range record.Users {
			if user == nil || user.Name != name {
				continue
			}
			usage := active[adminUserKey(tag, name)]
			upload += user.UploadBytes + usage.Upload
			download += user.DownloadBytes + usage.Download
			if user.QuotaBytes == 0 {
				unlimited = true
			} else {
				total += user.QuotaBytes
			}
			if user.ExpiresAt > 0 && (expire == 0 || user.ExpiresAt < expire) {
				expire = user.ExpiresAt
			}
		}
	}
	if unlimited {
		total = 0
	}
	if expire > 0 {
		expire /= 1000
	}
	return
}

func (a *adminAPI) subscriptionNameLocked(token string) (string, bool) {
	for candidateName, candidateToken := range a.store.Subscriptions {
		if len(token) == len(candidateToken) && subtle.ConstantTimeCompare([]byte(token), []byte(candidateToken)) == 1 {
			return candidateName, true
		}
	}
	return "", false
}

func (a *adminAPI) subscriptionLinksLocked(name string, now int64, active map[string]adminUsage) []string {
	links := make([]string, 0)
	for tag, profile := range a.store.Servers {
		if profile == nil || profile.Deleted || profile.Kind != adminServerKindInbound || profile.Revision == 0 || profile.Revision != profile.AppliedRevision {
			continue
		}
		runtimeInbound := a.runtimes[tag]
		if runtimeInbound == nil || runtimeInbound.Type != profile.Type {
			continue
		}
		record := a.store.Inbounds[tag]
		if record == nil {
			continue
		}
		for _, user := range record.Users {
			if user == nil || user.Name != name || !adminUserEnabled(user, now, active[adminUserKey(tag, name)]) {
				continue
			}
			if link, ok := subscriptionLink(a.ctx, tag, profile, user); ok {
				links = append(links, link)
			}
		}
	}
	return links
}

func subscriptionLink(ctx context.Context, tag string, profile *adminServerStore, user *adminUser) (string, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if profile.Advertise.Server == "" || profile.Advertise.ServerPort == 0 {
		return "", false
	}
	authority := net.JoinHostPort(profile.Advertise.Server, strconv.Itoa(int(profile.Advertise.ServerPort)))
	label := tag + " - " + user.Name
	switch profile.Type {
	case C.TypeVLESS:
		var config option.VLESSInboundOptions
		if SJSON.UnmarshalContext(ctx, profile.Config, &config) != nil || config.Transport != nil || config.TLS == nil || !config.TLS.Enabled || config.TLS.Reality == nil || !config.TLS.Reality.Enabled || user.UUID == "" {
			return "", false
		}
		privateKeyBytes, err := base64.RawURLEncoding.DecodeString(config.TLS.Reality.PrivateKey)
		if err != nil {
			return "", false
		}
		privateKey, err := ecdh.X25519().NewPrivateKey(privateKeyBytes)
		if err != nil {
			return "", false
		}
		shortIDs := append([]string(nil), config.TLS.Reality.ShortID...)
		sort.Strings(shortIDs)
		query := url.Values{"encryption": {"none"}, "security": {"reality"}, "type": {"tcp"}, "fp": {"chrome"}, "pbk": {base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes())}}
		if user.Flow != "" {
			query.Set("flow", user.Flow)
		}
		if profile.Advertise.TLSServerName != "" {
			query.Set("sni", profile.Advertise.TLSServerName)
		}
		if len(shortIDs) > 0 {
			query.Set("sid", shortIDs[0])
		}
		return (&url.URL{Scheme: "vless", User: url.User(user.UUID), Host: authority, RawQuery: query.Encode(), Fragment: label}).String(), true
	case C.TypeHysteria2:
		var config option.Hysteria2InboundOptions
		if SJSON.UnmarshalContext(ctx, profile.Config, &config) != nil || user.Password == "" {
			return "", false
		}
		query := subscriptionTLSQuery(profile.Advertise, "insecure")
		if config.Obfs != nil && config.Obfs.Type != "" {
			query.Set("obfs", config.Obfs.Type)
			query.Set("obfs-password", config.Obfs.Password)
		}
		return (&url.URL{Scheme: "hysteria2", User: url.User(user.Password), Host: authority, RawQuery: query.Encode(), Fragment: label}).String(), true
	case C.TypeTUIC:
		var config option.TUICInboundOptions
		if SJSON.UnmarshalContext(ctx, profile.Config, &config) != nil || user.UUID == "" || user.Password == "" {
			return "", false
		}
		query := subscriptionTLSQuery(profile.Advertise, "allow_insecure")
		if config.CongestionControl != "" {
			query.Set("congestion_control", config.CongestionControl)
		}
		return (&url.URL{Scheme: "tuic", User: url.UserPassword(user.UUID, user.Password), Host: authority, RawQuery: query.Encode(), Fragment: label}).String(), true
	default:
		return "", false
	}
}

func subscriptionTLSQuery(advertise adminServerAdvertise, insecureKey string) url.Values {
	query := make(url.Values)
	if advertise.TLSServerName != "" {
		query.Set("sni", advertise.TLSServerName)
	}
	if advertise.Insecure {
		query.Set(insecureKey, "1")
	}
	return query
}
