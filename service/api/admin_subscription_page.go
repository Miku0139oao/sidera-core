package api

import (
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type subscriptionProfilePage struct {
	Name            string
	SubscriptionURL string
	Nodes           []subscriptionProfileNode
	Upload          int64
	Download        int64
	Total           int64
	Expire          int64
}

type subscriptionProfileNode struct {
	Name     string
	Protocol string
}

func (a *adminAPI) getSubscriptionProfile(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	identifier := request.PathValue("identifier")
	if identifier == "" {
		identifier = chi.URLParam(request, "identifier")
	}
	a.storeAccess.RLock()
	name, subscriptionPath, found := a.subscriptionProfileIdentityLocked(identifier)
	a.storeAccess.RUnlock()
	if !found || a.publicBaseURL == "" {
		http.NotFound(writer, request)
		return
	}

	active := a.activeUsage()
	a.storeAccess.RLock()
	links := a.subscriptionLinksLocked(name, time.Now().UnixMilli(), active)
	upload, download, total, expire := a.subscriptionUsageLocked(name, active)
	a.storeAccess.RUnlock()
	sort.Strings(links)
	nodes := make([]subscriptionProfileNode, 0, len(links))
	for _, link := range links {
		parsed, err := url.Parse(link)
		if err != nil {
			continue
		}
		label := parsed.Fragment
		if label == "" {
			label = parsed.Hostname()
		}
		nodes = append(nodes, subscriptionProfileNode{Name: label, Protocol: strings.ToUpper(parsed.Scheme)})
	}

	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	_ = subscriptionProfileTemplate.Execute(writer, subscriptionProfilePage{
		Name: name, SubscriptionURL: a.publicBaseURL + subscriptionPath, Nodes: nodes,
		Upload: upload, Download: download, Total: total, Expire: expire,
	})
}

func (a *adminAPI) subscriptionProfileIdentityLocked(identifier string) (name string, path string, found bool) {
	if name, found = a.subscriptionNameLocked(identifier); found {
		return name, a.subscriptionPathLocked() + url.PathEscape(identifier), true
	}
	for candidateName, externalID := range a.store.ExternalSubscriptions {
		if externalID == identifier && validExternalSubscriptionID(externalID) {
			return candidateName, "/sub/" + url.PathEscape(externalID), true
		}
	}
	return "", "", false
}

var subscriptionProfileTemplate = template.Must(template.New("subscription-profile").Funcs(template.FuncMap{
	"bytes": func(value int64) string {
		if value == 0 {
			return "0 B"
		}
		return formatSubscriptionGiB(value)
	},
	"quota": func(value int64) string {
		if value == 0 {
			return "不限"
		}
		return formatSubscriptionGiB(value)
	},
	"expiry": func(value int64) string {
		if value == 0 {
			return "無期限"
		}
		return time.Unix(value, 0).Local().Format("2006-01-02 15:04")
	},
}).Parse(`<!doctype html>
<html lang="zh-Hant"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Name}} · Sidera 訂閱</title><style>
:root{color-scheme:light dark;--primary:#6750a4;--on-primary:#fff;--surface:#fffbfe;--surface-2:#f3edf7;--surface-3:#eaddff;--text:#1d1b20;--muted:#49454f;--outline:#79747e;--ok:#386a20}*{box-sizing:border-box}body{margin:0;min-height:100vh;background:radial-gradient(circle at 80% 0,#eaddff 0,transparent 35%),var(--surface);color:var(--text);font:500 16px/1.55 system-ui,-apple-system,"Segoe UI",sans-serif}.shell{width:min(920px,calc(100% - 32px));margin:auto;padding:48px 0 64px}.brand{display:flex;align-items:center;gap:12px;color:var(--primary);font-weight:800;letter-spacing:.04em}.mark{display:grid;place-items:center;width:42px;height:42px;border-radius:14px;background:var(--primary);color:var(--on-primary)}.hero{margin-top:28px;padding:clamp(28px,6vw,56px);border-radius:32px;background:var(--surface-3);box-shadow:0 14px 45px #21005d18}.eyebrow{color:var(--ok);font-size:.82rem;font-weight:800;text-transform:uppercase;letter-spacing:.09em}h1{margin:10px 0 12px;font-size:clamp(2.2rem,8vw,4.8rem);line-height:1.02;letter-spacing:-.05em}p{color:var(--muted)}.actions{display:flex;flex-wrap:wrap;gap:12px;margin-top:26px}.button{display:inline-flex;align-items:center;justify-content:center;min-height:48px;padding:0 22px;border:1px solid var(--outline);border-radius:999px;color:var(--primary);text-decoration:none;font-weight:800}.button.primary{border-color:var(--primary);background:var(--primary);color:var(--on-primary)}.stats{display:grid;grid-template-columns:repeat(4,1fr);gap:14px;margin:18px 0}.card{padding:20px;border-radius:24px;background:var(--surface-2)}.card span{display:block;color:var(--muted);font-size:.8rem}.card strong{display:block;margin-top:7px;font-size:1.15rem}.section{margin-top:24px}.section h2{font-size:1.35rem}.nodes{display:grid;grid-template-columns:repeat(2,1fr);gap:12px}.node{display:flex;align-items:center;gap:14px;padding:17px 19px;border:1px solid #79747e55;border-radius:20px;background:var(--surface)}.protocol{padding:5px 9px;border-radius:9px;background:var(--surface-3);color:var(--primary);font-size:.72rem;font-weight:900}.empty{padding:28px;border:1px dashed var(--outline);border-radius:22px;text-align:center}.url{overflow:hidden;margin-top:22px;padding:15px 18px;border-radius:16px;background:#1d1b20;color:#f5eff7;font:500 .78rem/1.5 ui-monospace,monospace;text-overflow:ellipsis;white-space:nowrap}@media(max-width:700px){.shell{padding-top:24px}.stats{grid-template-columns:repeat(2,1fr)}.nodes{grid-template-columns:1fr}.hero{border-radius:26px}}@media(prefers-color-scheme:dark){:root{--primary:#d0bcff;--on-primary:#381e72;--surface:#141218;--surface-2:#211f26;--surface-3:#4f378b;--text:#e6e0e9;--muted:#cac4d0;--outline:#938f99;--ok:#a8d18d}body{background:radial-gradient(circle at 80% 0,#4f378b 0,transparent 32%),var(--surface)}.hero{box-shadow:none}}
</style></head><body><main class="shell"><div class="brand"><span class="mark">S</span><span>SIDERA SUBSCRIPTION</span></div><section class="hero"><span class="eyebrow">訂閱可用</span><h1>{{.Name}}</h1><p>這是你的私人代理設定入口。請勿公開分享訂閱網址；在支援的客戶端中加入後，節點會自動同步。</p><div class="actions"><a class="button primary" href="{{.SubscriptionURL}}">取得原始訂閱</a><a class="button" href="#nodes">查看節點</a></div><div class="url" title="{{.SubscriptionURL}}">{{.SubscriptionURL}}</div></section><section class="stats" aria-label="訂閱狀態"><article class="card"><span>可用節點</span><strong>{{len .Nodes}}</strong></article><article class="card"><span>已用流量</span><strong>{{bytes .Upload}} ↑ · {{bytes .Download}} ↓</strong></article><article class="card"><span>流量額度</span><strong>{{quota .Total}}</strong></article><article class="card"><span>有效期限</span><strong>{{expiry .Expire}}</strong></article></section><section class="section" id="nodes"><h2>節點清單</h2>{{if .Nodes}}<div class="nodes">{{range .Nodes}}<article class="node"><span class="protocol">{{.Protocol}}</span><strong>{{.Name}}</strong></article>{{end}}</div>{{else}}<div class="empty"><strong>目前沒有可用節點</strong><p>可能是節點尚未套用、帳戶已停用，或流量／期限已用盡。</p></div>{{end}}</section></main></body></html>`))

func formatSubscriptionGiB(value int64) string {
	const gib = 1024 * 1024 * 1024
	return strconv.FormatFloat(float64(value)/gib, 'f', 2, 64) + " GiB"
}
