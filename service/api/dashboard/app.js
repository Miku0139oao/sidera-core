(() => {
  "use strict";

  const API_BASE = "/api/admin";
  const TOKEN_KEY = "sidera-token";
  const THEME_KEY = "sidera-theme";
  const POLL_INTERVAL = 5000;
  const REQUEST_TIMEOUT = 12000;
  const GIB = 1024 ** 3;
  const DEFAULT_SUBSCRIPTION_PATH = "/sub/sidera/";
  const DEFAULT_PROFILE_PATH = "/api/list/nodes/";

  const app = document.getElementById("app");
  const toastRegion = document.getElementById("toast-region");

  const ROUTES = {
    overview: {
      hash: "#/overview",
      label: "總覽",
      title: "系統總覽",
      kicker: "Core 狀態",
      icon: "overview",
    },
    servers: {
      hash: "#/servers",
      label: "節點",
      title: "節點管理",
      kicker: "Server 設定",
      icon: "server",
    },
    users: {
      hash: "#/users",
      label: "用戶",
      title: "用戶管理",
      kicker: "存取控制",
      icon: "users",
    },
    connections: {
      hash: "#/connections",
      label: "連線",
      title: "即時連線",
      kicker: "網路活動",
      icon: "activity",
    },
    settings: {
      hash: "#/settings",
      label: "設定",
      title: "訂閱安全",
      kicker: "公開路徑",
      icon: "shield",
    },
  };

  const ICONS = {
    overview: '<path d="M4 13h6V4H4v9Zm0 7h6v-4H4v4Zm10 0h6v-9h-6v9Zm0-16v4h6V4h-6Z"/>',
    users: '<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75"/>',
    activity: '<path d="M3 12h4l2.5-7 5 14 2.5-7h4"/>',
    moon: '<path d="M21 12.8A9 9 0 1 1 11.2 3 7 7 0 0 0 21 12.8Z"/>',
    sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.42 1.42M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.42-1.42M17.66 6.34l1.41-1.41"/>',
    refresh: '<path d="M20 6v5h-5"/><path d="M19 15a8 8 0 1 1-1.9-8.3L20 11"/>',
    logout: '<path d="M10 17l5-5-5-5M15 12H3"/><path d="M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4"/>',
    shield: '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z"/><path d="m9 12 2 2 4-4"/>',
    sparkles: '<path d="m12 3-1.2 3.8L7 8l3.8 1.2L12 13l1.2-3.8L17 8l-3.8-1.2L12 3Z"/><path d="m5 14-.8 2.2L2 17l2.2.8L5 20l.8-2.2L8 17l-2.2-.8L5 14ZM19 13l-.7 1.8-1.8.7 1.8.7L19 18l.7-1.8 1.8-.7-1.8-.7L19 13Z"/>',
    arrowUp: '<path d="m18 15-6-6-6 6"/><path d="M12 9v11"/>',
    arrowDown: '<path d="m6 9 6 6 6-6"/><path d="M12 15V4"/>',
    link: '<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>',
    server: '<rect x="3" y="4" width="18" height="6" rx="2"/><rect x="3" y="14" width="18" height="6" rx="2"/><path d="M7 7h.01M7 17h.01"/>',
    memory: '<rect x="5" y="5" width="14" height="14" rx="2"/><path d="M9 9h6v6H9zM9 1v4M15 1v4M9 19v4M15 19v4M19 9h4M19 14h4M1 9h4M1 14h4"/>',
    clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
    cpu: '<rect x="4" y="4" width="16" height="16" rx="3"/><path d="M9 9h6v6H9zM9 1v3M15 1v3M9 20v3M15 20v3M20 9h3M20 14h3M1 9h3M1 14h3"/>',
    code: '<path d="m8 9-3 3 3 3M16 9l3 3-3 3M14 5l-4 14"/>',
    check: '<path d="m5 12 4 4L19 6"/>',
    plus: '<path d="M12 5v14M5 12h14"/>',
    search: '<circle cx="11" cy="11" r="7"/><path d="m20 20-4-4"/>',
    close: '<path d="M6 6l12 12M18 6 6 18"/>',
    edit: '<path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L8 18l-4 1 1-4L16.5 3.5Z"/>',
    trash: '<path d="M3 6h18M8 6V4h8v2M19 6l-1 15H6L5 6M10 11v5M14 11v5"/>',
    reset: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5"/>',
    key: '<circle cx="8" cy="15" r="4"/><path d="m11 12 9-9M16 7l3 3M14 9l2 2"/>',
    copy: '<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M15 9V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h3"/>',
    eye: '<path d="M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6S2 12 2 12Z"/><circle cx="12" cy="12" r="2.5"/>',
    eyeOff: '<path d="m3 3 18 18M10.6 10.6a2 2 0 0 0 2.8 2.8M9.9 4.2A10.7 10.7 0 0 1 12 4c6.5 0 10 8 10 8a17.3 17.3 0 0 1-2.1 3.2M6.6 6.6C3.6 8.5 2 12 2 12s3.5 8 10 8a10 10 0 0 0 4.1-.9"/>',
    wand: '<path d="m15 4 5 5L8 21l-5-5L15 4Z"/><path d="m13 6 5 5M6 3v3M4.5 4.5h3M20 15v4M18 17h4"/>',
    alert: '<path d="M10.3 3.6 1.9 18a2 2 0 0 0 1.7 3h16.8a2 2 0 0 0 1.7-3L13.7 3.6a2 2 0 0 0-3.4 0Z"/><path d="M12 9v4M12 17h.01"/>',
    wifiOff: '<path d="m2 2 20 20M8.5 8.5A9.8 9.8 0 0 1 12 8c3.7 0 6.8 2 8.5 4M5.5 12A9.5 9.5 0 0 0 3.5 14M8.5 16.5A5.2 5.2 0 0 1 12 15c1.1 0 2.1.3 3 .8M12 20h.01"/>',
    database: '<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v6c0 1.7 3.6 3 8 3s8-1.3 8-3V5M4 11v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6"/>',
    network: '<circle cx="12" cy="5" r="3"/><circle cx="5" cy="19" r="3"/><circle cx="19" cy="19" r="3"/><path d="m10.5 7.7-4 8M13.5 7.7l4 8M8 19h8"/>',
    plug: '<path d="m12 22 1-6-5-3 7-11-1 7 5 2-7 11Z"/>',
    gauge: '<path d="M4.9 19a9 9 0 1 1 14.2 0"/><path d="m12 13 4-4M8 19h8"/>',
    userPlus: '<path d="M15 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2M8 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8ZM19 8v6M16 11h6"/>',
    filter: '<path d="M4 5h16M7 12h10M10 19h4"/>',
  };

  class APIError extends Error {
    constructor(message, status = 0, offline = false, payload = null) {
      super(message);
      this.name = "APIError";
      this.status = status;
      this.offline = offline;
      this.payload = payload;
    }
  }

  const state = {
    authenticated: false,
    route: routeFromHash(),
    overview: null,
    overviewSample: null,
    trafficSamples: [],
    speed: null,
    users: [],
    accounts: [],
    connections: [],
    inbounds: [],
    protocolSchemaVersion: null,
    protocols: [],
    servers: [],
    settings: null,
    import3XUI: {
      busy: false,
      applying: false,
      file: null,
      mapping: "{}",
      report: null,
      reportStale: false,
      error: "",
    },
    restartRequired: false,
    reloading: false,
    savingSettings: false,
    offline: !navigator.onLine,
    lastUpdated: 0,
    loading: {
      overview: false,
      protocols: false,
      servers: false,
      users: false,
      connections: false,
      settings: false,
    },
    loaded: {
      protocols: false,
      servers: false,
      users: false,
      connections: false,
      settings: false,
    },
    errors: {
      overview: "",
      protocols: "",
      servers: "",
      users: "",
      connections: "",
      settings: "",
    },
    filters: {
      userSearch: "",
      userInbound: "",
      userStatus: "all",
      connectionSearch: "",
      connectionInbound: "",
      connectionNetwork: "",
      serverSearch: "",
      serverCategory: "",
      serverStatus: "",
    },
    dialog: null,
    detailRequest: 0,
    pollTimer: 0,
    epoch: 0,
    theme: readTheme(),
  };

  const integerFormat = new Intl.NumberFormat("zh-TW", { maximumFractionDigits: 0 });
  const decimalFormat = new Intl.NumberFormat("zh-TW", { maximumFractionDigits: 1 });
  const dateTimeFormat = new Intl.DateTimeFormat("zh-TW", {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
  const clockFormat = new Intl.DateTimeFormat("zh-TW", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
  const chartTimeFormat = new Intl.DateTimeFormat("zh-TW", {
    hour: "2-digit",
    minute: "2-digit",
  });

  const derivedCache = {
    protocols: null,
    protocolByKey: new Map(),
    inbounds: null,
    inboundByTag: new Map(),
    users: null,
    accounts: null,
    userGroups: [],
  };
  let resultRenderFrame = 0;
  let pendingResultRoute = "";
  let responsiveRenderFrame = 0;
  let mobileDataListMode = null;

  function icon(name, className = "") {
    const paths = ICONS[name] || ICONS.sparkles;
    const classes = className ? `icon ${className}` : "icon";
    return `<svg class="${classes}" viewBox="0 0 24 24" aria-hidden="true" focusable="false">${paths}</svg>`;
  }

  function currentMobileDataListMode() {
    const main = document.getElementById("main-content");
    if (!main) return window.innerWidth <= 700;
    const style = window.getComputedStyle(main);
    const contentWidth = main.clientWidth - (Number.parseFloat(style.paddingLeft) || 0) - (Number.parseFloat(style.paddingRight) || 0);
    return contentWidth <= 760;
  }

  function useMobileDataLists() {
    mobileDataListMode = currentMobileDataListMode();
    return mobileDataListMode;
  }

  function scheduleResultRender(route) {
    pendingResultRoute = route;
    if (resultRenderFrame) return;
    resultRenderFrame = window.requestAnimationFrame(() => {
      resultRenderFrame = 0;
      const pendingRoute = pendingResultRoute;
      pendingResultRoute = "";
      if (pendingRoute !== state.route) return;
      if (pendingRoute === "users") renderUserResults();
      if (pendingRoute === "connections") renderConnectionResults();
      if (pendingRoute === "servers") renderServerResults();
    });
  }

  function escapeHTML(value) {
    return String(value ?? "").replace(/[&<>'"]/g, (character) => ({
      "&": "&amp;",
      "<": "&lt;",
      ">": "&gt;",
      "'": "&#39;",
      '"': "&quot;",
    })[character]);
  }

  function asNumber(value) {
    const number = Number(value);
    return Number.isFinite(number) && number > 0 ? number : 0;
  }

  function formatInteger(value) {
    return integerFormat.format(asNumber(value));
  }

  function formatBytes(value) {
    let bytes = asNumber(value);
    const units = ["B", "KB", "MB", "GB", "TB", "PB"];
    let unitIndex = 0;
    while (bytes >= 1024 && unitIndex < units.length - 1) {
      bytes /= 1024;
      unitIndex += 1;
    }
    const formatter = bytes >= 100 || unitIndex === 0 ? integerFormat : decimalFormat;
    return `${formatter.format(bytes)} ${units[unitIndex]}`;
  }

  function formatRate(value) {
    return value === null || value === undefined ? "等待取樣" : `${formatBytes(value)}/秒`;
  }

  function formatUptime(value) {
    let seconds = Math.max(0, Math.floor(Number(value) || 0));
    const days = Math.floor(seconds / 86400);
    seconds %= 86400;
    const hours = Math.floor(seconds / 3600);
    seconds %= 3600;
    const minutes = Math.floor(seconds / 60);
    const remainingSeconds = seconds % 60;
    const parts = [];
    if (days) parts.push(`${integerFormat.format(days)} 天`);
    if (hours) parts.push(`${integerFormat.format(hours)} 小時`);
    if (minutes && parts.length < 2) parts.push(`${integerFormat.format(minutes)} 分鐘`);
    if (!parts.length) parts.push(`${integerFormat.format(remainingSeconds)} 秒`);
    return parts.slice(0, 2).join(" ");
  }

  function formatTime(value) {
    const timestamp = Number(value);
    if (!Number.isFinite(timestamp) || timestamp <= 0) return "無期限";
    return dateTimeFormat.format(new Date(timestamp));
  }

  function formatConnectionAge(value) {
    const timestamp = Number(value);
    if (!Number.isFinite(timestamp) || timestamp <= 0) return "未知";
    return formatUptime((Date.now() - timestamp) / 1000);
  }

  function toDateTimeLocal(value) {
    const timestamp = Number(value);
    if (!Number.isFinite(timestamp) || timestamp <= 0) return "";
    const date = new Date(timestamp);
    const pad = (part) => String(part).padStart(2, "0");
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
  }

  function toQuotaGB(value) {
    const bytes = asNumber(value);
    if (!bytes) return "";
    return String(Math.round((bytes / GIB) * 1000) / 1000);
  }

  function credentialLabel(value) {
    switch (value) {
      case "uuid": return "UUID";
      case "password": return "密碼";
      case "uuid_password": return "UUID + 密碼";
      default: return value || "未指定";
    }
  }

  function routeFromHash() {
    const route = window.location.hash.replace(/^#\//, "");
    return Object.hasOwn(ROUTES, route) ? route : "overview";
  }

  function readTheme() {
    try {
      const saved = localStorage.getItem(THEME_KEY);
      if (saved === "light" || saved === "dark") return saved;
    } catch (_) {
      // Local storage can be unavailable in hardened browser contexts.
    }
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }

  function applyTheme() {
    document.documentElement.dataset.theme = state.theme;
    const themeColor = state.theme === "dark" ? "#121217" : "#f8f6ff";
    document.querySelector('meta[name="theme-color"]')?.setAttribute("content", themeColor);
    document.querySelectorAll('[data-action="toggle-theme"]').forEach((button) => {
      const nextTheme = state.theme === "dark" ? "淺色" : "深色";
      button.setAttribute("aria-label", `切換至${nextTheme}模式`);
      button.setAttribute("title", `切換至${nextTheme}模式`);
      button.innerHTML = icon(state.theme === "dark" ? "sun" : "moon");
    });
  }

  function toggleTheme() {
    state.theme = state.theme === "dark" ? "light" : "dark";
    try {
      localStorage.setItem(THEME_KEY, state.theme);
    } catch (_) {
      // The selected theme still applies for the current page.
    }
    applyTheme();
  }

  function getToken() {
    try {
      return sessionStorage.getItem(TOKEN_KEY) || "";
    } catch (_) {
      return "";
    }
  }

  function setToken(value) {
    try {
      sessionStorage.setItem(TOKEN_KEY, value);
    } catch (_) {
      throw new APIError("瀏覽器不允許儲存此分頁的 Token");
    }
  }

  function clearToken() {
    try {
      sessionStorage.removeItem(TOKEN_KEY);
    } catch (_) {
      // There is nothing else to clear when session storage is unavailable.
    }
  }

  async function api(path, options = {}) {
    const { timeoutMs = REQUEST_TIMEOUT, ...requestOptions } = options;
    const headers = new Headers(requestOptions.headers || {});
    headers.set("Accept", "application/json");
    if (requestOptions.body !== undefined && !(requestOptions.body instanceof FormData)) headers.set("Content-Type", "application/json");
    const token = getToken();
    if (token) {
      try {
        headers.set("Authorization", `Bearer ${token}`);
      } catch (_) {
        throw new APIError("Token 格式不正確，請移除換行或控制字元");
      }
    }

    const controller = new AbortController();
    const timeout = window.setTimeout(() => controller.abort(), timeoutMs);
    let response;
    try {
      response = await fetch(`${API_BASE}${path}`, {
        ...requestOptions,
        headers,
        cache: "no-store",
        credentials: "same-origin",
        signal: controller.signal,
      });
    } catch (error) {
      const timedOut = error?.name === "AbortError";
      setOffline(true);
      throw new APIError(timedOut ? "連線逾時，請確認 Core 是否可用" : "無法連上 Core，請檢查網路與服務狀態", 0, true);
    } finally {
      window.clearTimeout(timeout);
    }

    setOffline(false);
    let payload = null;
    let responseText = "";
    let invalidJSON = false;
    if (response.status !== 204) {
      try {
        responseText = await response.text();
      } catch (_) {
        setOffline(true);
        throw new APIError("接收 Core 回應時連線中斷", 0, true);
      }
      if (responseText) {
        try {
          payload = JSON.parse(responseText);
        } catch (_) {
          invalidJSON = true;
        }
      }
    }

    if (response.status === 401) {
      const message = typeof payload?.error === "string" ? payload.error : "API Token 無效或已過期";
      handleUnauthorized(message);
      throw new APIError(message, 401);
    }

    if (!response.ok) {
      const message = typeof payload?.error === "string" ? payload.error : `請求失敗（HTTP ${response.status}）`;
      throw new APIError(message, response.status, false, payload);
    }

    if (response.status !== 204 && (!responseText || invalidJSON || payload === null || typeof payload !== "object")) {
      throw new APIError("Core 回應格式不正確", response.status);
    }

    return payload;
  }

  function setOffline(value) {
    state.offline = Boolean(value);
    const banner = document.getElementById("offline-banner");
    if (banner) banner.hidden = !state.offline;
    const loginOffline = document.getElementById("login-offline");
    if (loginOffline) loginOffline.hidden = !state.offline;
  }

  function handleUnauthorized(message) {
    state.epoch += 1;
    state.authenticated = false;
    clearToken();
    stopPolling();
    closeDialog();
    state.dialog = null;
    renderLogin(message, "error");
  }

  function showToast(message, type = "info") {
    const toast = document.createElement("div");
    const symbol = document.createElement("span");
    const text = document.createElement("span");
    toast.className = `toast ${type}`;
    toast.setAttribute("role", type === "error" ? "alert" : "status");
    symbol.className = "toast-symbol";
    symbol.innerHTML = icon(type === "error" ? "alert" : type === "success" ? "check" : "sparkles");
    text.textContent = message;
    toast.append(symbol, text);
    toastRegion.append(toast);
    const dismiss = () => {
      toast.classList.add("is-leaving");
      window.setTimeout(() => toast.remove(), 220);
    };
    window.setTimeout(dismiss, type === "error" ? 6000 : 3800);
  }

  function renderBoot() {
    app.setAttribute("aria-busy", "true");
    app.innerHTML = `
      <main class="boot-page" id="main-content" tabindex="-1" aria-label="正在連線至 Core">
        <div class="boot-card">
          <span class="boot-mark">${icon("sparkles", "icon-xl")}</span>
          <strong>Sidera · Core Console</strong>
          <span>正在建立安全工作階段</span>
          <span class="boot-dots" aria-hidden="true"><i></i><i></i><i></i></span>
        </div>
      </main>`;
    applyTheme();
  }

  function renderLogin(message = "", kind = "error") {
    app.setAttribute("aria-busy", "false");
    document.title = "登入 · Sidera Core Console";
    const notice = message ? `
      <div class="${kind === "error" ? "form-error" : "schema-note"}" id="login-message" role="${kind === "error" ? "alert" : "status"}">
        ${icon(kind === "error" ? "alert" : "check")}
        <span>${escapeHTML(message)}</span>
      </div>` : '<div class="form-error" id="login-message" role="alert" hidden></div>';
    app.innerHTML = `
      <main class="login-page" id="main-content" tabindex="-1">
        <button class="icon-button login-theme" type="button" data-action="toggle-theme" aria-label="切換顯示模式"></button>
        <section class="login-card" aria-labelledby="login-title">
          <div class="login-brand">
            <span class="brand-mark">${icon("sparkles", "icon-lg")}</span>
            <span class="brand-copy">
              <span class="brand-name">Sidera</span>
              <span class="brand-subtitle">Core Console</span>
            </span>
          </div>
          <h1 class="login-title" id="login-title">連線至<br>你的 Core</h1>
          <p class="login-description">輸入管理 API Token 以開啟本次分頁的安全工作階段。</p>
          <div class="login-offline" id="login-offline" role="alert" ${state.offline ? "" : "hidden"}>
            目前無法連上伺服器，仍可輸入 Token 後重試。
          </div>
          ${notice}
          <form class="login-form" data-form="login" novalidate>
            <div class="form-field">
              <label for="api-token">API Token</label>
              <div class="input-with-actions">
                <input class="text-input" id="api-token" name="token" type="password" autocomplete="current-password" spellcheck="false" aria-describedby="token-help">
                <div class="input-actions">
                  <button class="icon-button small" type="button" data-action="toggle-token" aria-label="顯示 Token" title="顯示 Token">${icon("eye")}</button>
                </div>
              </div>
              <span class="supporting-text" id="token-help">Token 只會保留在此分頁的 sessionStorage。</span>
            </div>
            <button class="button button-primary button-block" type="submit">
              ${icon("key")}
              <span>連線至 Core</span>
            </button>
          </form>
          <p class="login-hint">${icon("shield")}<span>若伺服器未啟用 Secret，可保留空白直接探測。</span></p>
        </section>
      </main>`;
    applyTheme();
    document.getElementById("api-token")?.focus();
  }

  function navItem(route, mobile = false) {
    const config = ROUTES[route];
    const active = state.route === route;
    let badge = "";
    if (!mobile && route === "users") {
      badge = `<span class="nav-badge" data-badge="users">${state.overview ? escapeHTML(formatInteger(state.overview.users?.total)) : "…"}</span>`;
    }
    if (!mobile && route === "connections") {
      badge = `<span class="nav-badge" data-badge="connections">${state.overview ? escapeHTML(formatInteger(state.overview.traffic?.active_connections)) : "…"}</span>`;
    }
    if (!mobile && route === "servers") {
      badge = `<span class="nav-badge" data-badge="servers">${state.loaded.servers ? escapeHTML(formatInteger(state.servers.length)) : "…"}</span>`;
    }
    return `
      <a class="nav-item" href="${config.hash}" data-route="${route}" ${active ? 'aria-current="page"' : ""}>
        ${icon(config.icon)}
        <span>${config.label}</span>
        ${badge}
      </a>`;
  }

  function renderShell() {
    const authenticationEnabled = Boolean(state.overview?.authentication_enabled);
    const securityTitle = !state.overview ? "驗證狀態未知" : authenticationEnabled ? "Token 驗證已啟用" : "管理 API 未設 Secret";
    const securityState = !state.overview ? "等待 Core 回應" : authenticationEnabled ? "工作階段憑證" : "目前允許同源存取";
    const config = ROUTES[state.route];
    app.setAttribute("aria-busy", "false");
    app.innerHTML = `
      <div class="app-shell">
        <aside class="drawer" aria-label="側邊導覽">
          <a class="brand" href="#/overview" aria-label="Sidera Core Console 首頁">
            <span class="brand-mark">${icon("sparkles", "icon-lg")}</span>
            <span class="brand-copy">
              <span class="brand-name">Sidera</span>
              <span class="brand-subtitle">Core Console</span>
            </span>
          </a>
          <p class="nav-label">控制台</p>
          <nav class="primary-nav" aria-label="主要導覽">
            ${navItem("overview")}
            ${navItem("servers")}
            ${navItem("users")}
            ${navItem("connections")}
            ${navItem("settings")}
          </nav>
          <div class="drawer-footer">
            <p class="nav-label">帳戶與安全性</p>
            <section class="security-card" aria-label="工作階段安全性">
              <div class="security-heading">
                <span class="security-icon">${icon("shield")}</span>
                <span>
                  <span class="security-title" id="security-title">${securityTitle}</span>
                  <span class="security-state" id="security-state">${securityState}</span>
                </span>
              </div>
              <div class="security-actions">
                <button class="button button-quiet" type="button" data-action="logout">${icon("logout")}登出 Token</button>
              </div>
            </section>
          </div>
        </aside>

        <div class="workspace">
          <header class="topbar">
            <div class="mobile-brand" aria-label="Sidera Core Console">
              <span class="brand-mark">${icon("sparkles")}</span>
              <span class="brand-name">Sidera</span>
            </div>
            <div class="topbar-title">
              <span class="topbar-kicker" id="topbar-kicker">${config.kicker}</span>
              <h1 id="topbar-title">${config.title}</h1>
            </div>
            <div class="topbar-actions">
              <span class="last-sync" id="last-sync">${state.lastUpdated ? `更新於 ${escapeHTML(clockFormat.format(new Date(state.lastUpdated)))}` : "尚未同步"}</span>
              <button class="icon-button" type="button" data-action="toggle-theme" aria-label="切換顯示模式"></button>
              <button class="icon-button" type="button" data-action="logout" aria-label="登出 Token" title="登出 Token">${icon("logout")}</button>
            </div>
          </header>

          <div class="offline-banner" id="offline-banner" role="alert" ${state.offline ? "" : "hidden"}>
            ${icon("wifiOff")}
            <span>無法連上 Core，畫面保留最近一次資料。</span>
            <button class="button button-quiet" type="button" data-action="retry">重新連線</button>
          </div>

          <main class="main-content" id="main-content" tabindex="-1"></main>
        </div>

        <nav class="bottom-nav" aria-label="行動版主要導覽">
          ${navItem("overview", true)}
          ${navItem("servers", true)}
          ${navItem("users", true)}
          ${navItem("connections", true)}
          ${navItem("settings", true)}
        </nav>
        <dialog class="app-dialog" id="app-dialog"></dialog>
      </div>`;
    setupDialog();
    applyTheme();
    renderCurrentRoute();
  }

  function setupDialog() {
    const dialog = document.getElementById("app-dialog");
    if (!dialog) return;
    dialog.addEventListener("close", () => {
      state.dialog = null;
      dialog.innerHTML = "";
      dialog.className = "app-dialog";
      dialog.removeAttribute("aria-labelledby");
      dialog.removeAttribute("aria-describedby");
    });
    dialog.addEventListener("cancel", (event) => {
      if (state.dialog?.submitting) event.preventDefault();
    });
  }

  function updateShell() {
    document.querySelectorAll('[data-badge="users"]').forEach((element) => {
      element.textContent = formatInteger(state.overview?.users?.total);
    });
    document.querySelectorAll('[data-badge="connections"]').forEach((element) => {
      element.textContent = formatInteger(state.overview?.traffic?.active_connections);
    });
    document.querySelectorAll('[data-badge="servers"]').forEach((element) => {
      element.textContent = state.loaded.servers ? formatInteger(state.servers.length) : "…";
    });
    const lastSync = document.getElementById("last-sync");
    if (lastSync) lastSync.textContent = state.lastUpdated ? `更新於 ${clockFormat.format(new Date(state.lastUpdated))}` : "尚未同步";
    const securityTitle = document.getElementById("security-title");
    const securityState = document.getElementById("security-state");
    if (securityTitle && securityState && state.overview) {
      const enabled = Boolean(state.overview.authentication_enabled);
      securityTitle.textContent = enabled ? "Token 驗證已啟用" : "管理 API 未設 Secret";
      securityState.textContent = enabled ? "工作階段憑證" : "目前允許同源存取";
    }
    setOffline(state.offline);
  }

  function updateRouteChrome() {
    const config = ROUTES[state.route];
    document.querySelectorAll("[data-route]").forEach((link) => {
      if (link.dataset.route === state.route) link.setAttribute("aria-current", "page");
      else link.removeAttribute("aria-current");
    });
    const title = document.getElementById("topbar-title");
    const kicker = document.getElementById("topbar-kicker");
    if (title) title.textContent = config.title;
    if (kicker) kicker.textContent = config.kicker;
    document.title = `${config.label} · Sidera Core Console`;
  }

  function renderCurrentRoute() {
    const main = document.getElementById("main-content");
    if (!main) return;
    updateRouteChrome();
    if (state.route === "overview") renderOverview();
    if (state.route === "servers") renderServers();
    if (state.route === "users") renderUsers();
    if (state.route === "connections") renderConnections();
    if (state.route === "settings") renderSettings();
  }

  function loadingPage(type) {
    if (type === "overview") {
      return `
        <div class="loading-page" role="status" aria-label="正在載入系統總覽">
          <span class="sr-only">正在載入系統總覽</span>
          <div class="skeleton skeleton-hero"></div>
          <div class="skeleton-row">
            <div class="skeleton skeleton-metric"></div><div class="skeleton skeleton-metric"></div>
            <div class="skeleton skeleton-metric"></div><div class="skeleton skeleton-metric"></div>
          </div>
        </div>`;
    }
    return `
      <div class="loading-page" role="status" aria-label="正在載入資料">
        <span class="sr-only">正在載入資料</span>
        <div class="skeleton skeleton-toolbar"></div>
        <div class="skeleton skeleton-table"></div>
      </div>`;
  }

  function errorState(title, message, action) {
    return `
      <section class="empty-state error-state page-enter" role="alert">
        <div>
          <span class="empty-illustration">${icon("alert", "icon-xl")}</span>
          <h2>${escapeHTML(title)}</h2>
          <p>${escapeHTML(message)}</p>
          <button class="button button-tonal" type="button" data-action="${action}">${icon("refresh")}再試一次</button>
        </div>
      </section>`;
  }

  function metricCard({ iconName, label, value, detail, tone = "" }) {
    return `
      <article class="card metric-card ${tone}">
        <div class="metric-top">
          <span class="metric-label">${escapeHTML(label)}</span>
          <span class="metric-icon">${icon(iconName)}</span>
        </div>
        <strong class="metric-value">${escapeHTML(value)}</strong>
        <span class="metric-detail">${escapeHTML(detail)}</span>
      </article>`;
  }

  function replaceContent(element, html, preserveScroll = false) {
    const scrollingElement = preserveScroll ? document.scrollingElement : null;
    const scrollLeft = scrollingElement?.scrollLeft || 0;
    const scrollTop = scrollingElement?.scrollTop || 0;
    element.innerHTML = html;
    if (scrollingElement) {
      scrollingElement.scrollLeft = scrollLeft;
      scrollingElement.scrollTop = scrollTop;
    }
  }

  function renderOverview({ animate = true, preserveScroll = false } = {}) {
    const main = document.getElementById("main-content");
    if (!main) return;
    const restoreRefreshFocus = main.contains(document.activeElement) && document.activeElement?.dataset.action === "refresh-overview";
    if (!state.overview && state.loading.overview) {
      main.innerHTML = loadingPage("overview");
      main.setAttribute("aria-busy", "true");
      return;
    }
    if (!state.overview) {
      main.innerHTML = errorState("無法取得 Core 狀態", state.errors.overview || "尚未收到伺服器資料。", "refresh-overview");
      main.setAttribute("aria-busy", "false");
      return;
    }

    const overview = state.overview;
    const traffic = overview.traffic || {};
    const users = overview.users || {};
    const platform = overview.platform || {};
    const running = overview.status === "running";
    const statusText = running ? "Core 正在穩定運行" : `Core 狀態：${overview.status || "未知"}`;
    const statusDescription = running
      ? "管理 API 已回應，流量與連線資料持續同步中。"
      : "管理 API 已回應，但 Core 回報了非運行狀態。";
    main.setAttribute("aria-busy", state.loading.overview ? "true" : "false");
    replaceContent(main, `
      <div class="${animate ? "page-enter" : ""}">
        <div class="page-heading">
          <div>
            <h2>系統總覽</h2>
            <p>一眼掌握 Core 健康狀態、流量趨勢與入站能力。</p>
          </div>
          <div class="page-actions">
            <button class="button button-outline ${state.loading.overview ? "is-loading" : ""}" type="button" data-action="refresh-overview" ${state.loading.overview ? "disabled" : ""}>
              ${icon("refresh")}<span>手動刷新</span>
            </button>
          </div>
        </div>

        <section class="hero-card" aria-labelledby="core-status-title">
          <div class="hero-content">
            <span class="eyebrow">${running ? '<span class="status-dot"></span>' : icon("alert")} ${running ? "服務在線" : "請留意狀態"}</span>
            <h3 class="hero-title" id="core-status-title">${running ? 'Core <span class="accent">運行良好</span>' : escapeHTML(statusText)}</h3>
            <p class="hero-description">${escapeHTML(statusDescription)} 已運行 ${escapeHTML(formatUptime(overview.uptime_seconds))}。</p>
          </div>
          <div class="hero-orbit" aria-hidden="true">
            <span class="hero-orbit-core">${icon("sparkles", "icon-xl")}</span>
            <span class="uptime-pill">${escapeHTML(formatUptime(overview.uptime_seconds))}</span>
          </div>
        </section>

        <section class="metrics-grid" aria-label="關鍵指標">
          ${metricCard({ iconName: "arrowUp", label: "上行總量", value: formatBytes(traffic.uplink_total), detail: `目前 ${formatRate(state.speed?.up)}`, tone: "primary-tone" })}
          ${metricCard({ iconName: "arrowDown", label: "下行總量", value: formatBytes(traffic.downlink_total), detail: `目前 ${formatRate(state.speed?.down)}`, tone: "success-tone" })}
          ${metricCard({ iconName: "link", label: "活躍連線", value: formatInteger(traffic.active_connections), detail: "每 3 秒同步" })}
          ${metricCard({ iconName: "users", label: "有效用戶", value: formatInteger(users.enabled), detail: `共 ${formatInteger(users.total)} 位用戶` })}
          ${metricCard({ iconName: "memory", label: "記憶體", value: formatBytes(platform.memory_bytes), detail: `${formatInteger(platform.goroutines)} 個 Goroutine` })}
          ${metricCard({ iconName: "clock", label: "運行時間", value: formatUptime(overview.uptime_seconds), detail: `啟動於 ${formatTime(overview.started_at)}` })}
          ${metricCard({ iconName: "userPlus", label: "停用用戶", value: formatInteger(users.disabled), detail: `${formatInteger(users.expired)} 位已到期` })}
          ${metricCard({ iconName: "cpu", label: "執行環境", value: `${platform.os || "未知"} / ${platform.arch || "未知"}`, detail: `${formatInteger(platform.cpu_cores)} 個 CPU 核心` })}
        </section>

        <div class="overview-grid">
          <section class="card section-card" aria-labelledby="traffic-chart-title">
            <div class="section-heading">
              <div>
                <h3 id="traffic-chart-title">即時流量脈動</h3>
                <p>最近 24 個速率取樣點</p>
              </div>
              <div class="chart-legend" aria-label="圖例">
                <span class="legend-item"><i class="legend-swatch"></i>上行 ${escapeHTML(formatRate(state.speed?.up))}</span>
                <span class="legend-item"><i class="legend-swatch down"></i>下行 ${escapeHTML(formatRate(state.speed?.down))}</span>
              </div>
            </div>
            ${renderTrafficChart()}
          </section>

          <section class="card section-card" aria-labelledby="platform-title">
            <div class="section-heading">
              <div>
                <h3 id="platform-title">平台資訊</h3>
                <p>Core 與管理 API 版本</p>
              </div>
            </div>
            <div class="platform-list">
              ${platformRow("Core 版本", overview.version || "未知")}
              ${platformRow("API 版本", overview.api_version || "未知")}
              ${platformRow("作業系統", platform.os || "未知")}
              ${platformRow("架構", platform.arch || "未知")}
              ${platformRow("CPU 核心", formatInteger(platform.cpu_cores))}
              ${platformRow("Goroutine", formatInteger(platform.goroutines))}
            </div>
          </section>
        </div>

        <section class="inbound-section" aria-labelledby="inbound-title">
          <div class="section-heading">
            <div>
              <h3 id="inbound-title">入站狀態</h3>
              <p>受管理入站可直接由此控制台維護用戶。</p>
            </div>
          </div>
          ${renderInboundCards(overview.inbounds)}
        </section>
      </div>`, preserveScroll);
    if (restoreRefreshFocus) main.querySelector('[data-action="refresh-overview"]')?.focus();
  }

  function platformRow(label, value) {
    return `<div class="platform-row"><span class="platform-key">${escapeHTML(label)}</span><span class="platform-value">${escapeHTML(value)}</span></div>`;
  }

  function renderTrafficChart() {
    const samples = state.trafficSamples.slice(-24);
    if (!samples.length) {
      return `
        <div class="chart-waiting" role="status">
          <div>${icon("activity", "icon-lg")}<strong>等待下一次流量取樣</strong><br><span>取得兩次總量後即可計算即時速率。</span></div>
        </div>`;
    }

    const width = 820;
    const height = 245;
    const left = 62;
    const right = 16;
    const top = 16;
    const bottom = 32;
    const plotWidth = width - left - right;
    const plotHeight = height - top - bottom;
    const maxValue = Math.max(1, ...samples.flatMap((sample) => [sample.up, sample.down]));
    const point = (sample, index, key) => {
      const x = left + (index / Math.max(samples.length - 1, 1)) * plotWidth;
      const y = top + plotHeight - (sample[key] / maxValue) * plotHeight;
      return [x, y];
    };
    const pathFor = (key) => samples.map((sample, index) => {
      const [x, y] = point(sample, index, key);
      return `${index ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`;
    }).join(" ");
    const grid = [0, 0.33, 0.66, 1].map((ratio) => {
      const y = top + plotHeight - ratio * plotHeight;
      const label = formatRate(maxValue * ratio);
      return `<line class="chart-grid-line" x1="${left}" y1="${y.toFixed(1)}" x2="${width - right}" y2="${y.toFixed(1)}"/><text class="chart-label" x="0" y="${(y + 4).toFixed(1)}">${escapeHTML(label)}</text>`;
    }).join("");
    const lastIndex = samples.length - 1;
    const [upX, upY] = point(samples[lastIndex], lastIndex, "up");
    const [downX, downY] = point(samples[lastIndex], lastIndex, "down");
    const startLabel = chartTimeFormat.format(new Date(samples[0].time));
    const endLabel = chartTimeFormat.format(new Date(samples[lastIndex].time));
    const ariaLabel = `流量折線圖，目前上行 ${formatRate(samples[lastIndex].up)}，下行 ${formatRate(samples[lastIndex].down)}`;
    return `
      <svg class="traffic-chart" viewBox="0 0 ${width} ${height}" role="img" aria-label="${escapeHTML(ariaLabel)}" preserveAspectRatio="none">
        ${grid}
        <path class="chart-line up" d="${pathFor("up")}"/>
        <path class="chart-line down" d="${pathFor("down")}"/>
        <circle class="chart-point up" cx="${upX.toFixed(1)}" cy="${upY.toFixed(1)}" r="4.5"/>
        <circle class="chart-point down" cx="${downX.toFixed(1)}" cy="${downY.toFixed(1)}" r="4.5"/>
        <text class="chart-label" x="${left}" y="${height - 5}">${escapeHTML(startLabel)}</text>
        <text class="chart-label" x="${width - right}" y="${height - 5}" text-anchor="end">${escapeHTML(endLabel)}</text>
      </svg>`;
  }

  function renderInboundCards(inbounds) {
    const items = Array.isArray(inbounds) ? inbounds : [];
    if (!items.length) {
      return `
        <div class="empty-state empty-state-compact">
          <div><span class="empty-illustration">${icon("server", "icon-xl")}</span><h3>尚未發現入站</h3><p>Core 目前沒有可顯示的入站服務。</p></div>
        </div>`;
    }
    return `<div class="inbound-grid">${items.map((inbound) => {
      const managed = Boolean(inbound.managed);
      const schemaChips = managed
        ? `<span class="chip primary">${escapeHTML(credentialLabel(inbound.credential))}</span>${inbound.flow ? '<span class="chip">Flow</span>' : ""}${inbound.alter_id ? '<span class="chip">Alter ID</span>' : ""}`
        : '<span class="chip">不支援動態用戶</span>';
      return `
        <article class="card inbound-card ${managed ? "" : "unmanaged"}">
          <div class="inbound-card-top">
            <div><h4 class="inbound-name">${escapeHTML(inbound.tag)}</h4><div class="inbound-type">${escapeHTML(inbound.type)}</div></div>
            <span class="chip ${managed ? "success" : ""}">${managed ? "可管理" : "唯讀"}</span>
          </div>
          <div class="inbound-count"><strong>${escapeHTML(formatInteger(inbound.enabled_user_count))}</strong><span>/ ${escapeHTML(formatInteger(inbound.user_count))} 位有效用戶</span></div>
          <div class="chip-row">${schemaChips}</div>
        </article>`;
    }).join("")}</div>`;
  }

  function protocolForServer(server) {
    return protocolsByKey().get(`${server.kind}:${server.type}`) || null;
  }

  function protocolKey(protocol) {
    return `${protocol.kind}:${protocol.type}`;
  }

  function findProtocol(key) {
    return protocolsByKey().get(key) || null;
  }

  function protocolsByKey() {
    if (derivedCache.protocols !== state.protocols) {
      derivedCache.protocols = state.protocols;
      derivedCache.protocolByKey = new Map(state.protocols.map((protocol) => [protocolKey(protocol), protocol]));
    }
    return derivedCache.protocolByKey;
  }

  function serverStatusInfo(status) {
    switch (status) {
      case "pending_create": return { label: "待建立", tone: "warning" };
      case "pending_update": return { label: "待更新", tone: "warning" };
      case "pending_delete": return { label: "待刪除", tone: "danger" };
      case "active": return { label: "運作中", tone: "success" };
      default: return { label: status || "未知", tone: "" };
    }
  }

  function categoryLabel(category) {
    const labels = {
      standard: "標準代理",
      encrypted: "加密代理",
      v2ray: "V2Ray",
      tls: "TLS 代理",
      transport: "傳輸層",
      quic: "QUIC",
      vpn: "VPN",
    };
    return labels[category] || category || "其他";
  }

  function networkLabel(network) {
    return String(network || "未知").split("+").map((part) => part.toUpperCase()).join(" + ");
  }

  function tlsLabel(value) {
    switch (value) {
      case "required": return "TLS 必要";
      case "optional": return "TLS 選用";
      case "none": return "無 TLS";
      default: return value || "TLS 未知";
    }
  }

  function updatePolicyLabel(value) {
    switch (value) {
      case "hot": return "熱更新";
      case "disruptive": return "中斷式更新";
      default: return value || "更新策略未知";
    }
  }

  function advertisedAddress(server) {
    const advertise = server.advertise || {};
    const host = String(advertise.server || "");
    if (!host) return "未設定公開位址";
    const displayHost = host.includes(":") && !host.startsWith("[") ? `[${host}]` : host;
    return advertise.server_port ? `${displayHost}:${advertise.server_port}` : displayHost;
  }

  function renderServerNotices() {
    const notices = [];
    if (state.errors.protocols) notices.push(`協議目錄同步失敗：${state.errors.protocols}`);
    if (state.loaded.servers && state.errors.servers) notices.push(`節點清單可能不是最新資料：${state.errors.servers}`);
    if (!notices.length) return "";
    return `
      <div class="server-sync-notice" role="status">
        ${icon("alert")}
        <span>${notices.map(escapeHTML).join(" ")}</span>
        <button class="button button-quiet" type="button" data-action="refresh-servers" ${state.loading.protocols || state.loading.servers ? "disabled" : ""}>重新同步</button>
      </div>`;
  }

  function renderRestartBanner() {
    if (!state.restartRequired && !state.reloading) return "";
    return `
      <section class="restart-banner" aria-labelledby="restart-title" aria-live="polite">
        <span class="restart-symbol">${icon(state.reloading ? "refresh" : "alert", "icon-lg")}</span>
        <div class="restart-copy">
          <h3 id="restart-title">${state.reloading ? "正在重新載入 Core" : "有節點變更等待套用"}</h3>
          <p>${state.reloading ? "Core 可能短暫離線；控制台正在等待服務與節點狀態恢復。" : "目前的連線不受影響。準備好後重新載入，待建立、更新或刪除的項目才會生效。"}</p>
        </div>
        <button class="button button-primary ${state.reloading ? "is-loading" : ""}" type="button" data-action="reload-core" ${state.reloading ? "disabled" : ""}>
          ${icon(state.reloading ? "refresh" : "reset")}<span>${state.reloading ? "重新連線中…" : "套用並重新載入"}</span>
        </button>
      </section>`;
  }

  function renderServers() {
    const main = document.getElementById("main-content");
    if (!main) return;
    const waitingForServers = !state.loaded.servers && !state.errors.servers;
    const waitingForProtocols = !state.loaded.protocols && !state.errors.protocols;
    if (waitingForServers || waitingForProtocols) {
      main.innerHTML = loadingPage("servers");
      main.setAttribute("aria-busy", "true");
      return;
    }
    if (!state.loaded.servers) {
      main.innerHTML = errorState("無法載入節點", state.errors.servers || "尚未收到節點資料。", "refresh-servers");
      main.setAttribute("aria-busy", "false");
      return;
    }

    const categories = [...new Set(state.protocols.map((protocol) => protocol.category).filter(Boolean))];
    const busy = state.loading.protocols || state.loading.servers;
    main.setAttribute("aria-busy", busy ? "true" : "false");
    main.innerHTML = `
      <div class="page-enter">
        <div class="page-heading">
          <div>
            <h2>節點管理</h2>
            <p>集中檢視基礎設定與面板節點，透過原生 JSON 精準管理協議能力。</p>
          </div>
          <div class="page-actions">
            <button class="icon-button ${busy ? "spin" : ""}" type="button" data-action="refresh-servers" aria-label="刷新節點與協議" title="刷新節點與協議" ${busy || state.reloading ? "disabled" : ""}>${icon("refresh")}</button>
            <button class="button button-primary" type="button" data-action="new-server" ${state.protocols.length && !busy && !state.reloading ? "" : "disabled"}>${icon("plus")}<span>新增節點</span></button>
          </div>
        </div>

        ${renderRestartBanner()}
        ${renderServerNotices()}

        ${state.servers.length ? `
          <section class="toolbar-card server-toolbar" aria-label="節點搜尋與篩選">
            <label class="search-field">
              <span class="sr-only">搜尋節點</span>
              ${icon("search")}
              <input type="search" data-filter="server-search" value="${escapeHTML(state.filters.serverSearch)}" placeholder="搜尋標籤、協議或公開位址…" autocomplete="off">
              <button class="icon-button small search-clear" type="button" data-action="clear-server-search" aria-label="清除搜尋" ${state.filters.serverSearch ? "" : "hidden"}>${icon("close")}</button>
            </label>
            <label><span class="sr-only">依協議類別篩選</span><select class="select-control" data-filter="server-category"><option value="">所有類別</option>${categories.map((category) => `<option value="${escapeHTML(category)}" ${state.filters.serverCategory === category ? "selected" : ""}>${escapeHTML(categoryLabel(category))}</option>`).join("")}</select></label>
            <label><span class="sr-only">依節點狀態篩選</span><select class="select-control" data-filter="server-status"><option value="">所有狀態</option><option value="active" ${state.filters.serverStatus === "active" ? "selected" : ""}>運作中</option><option value="pending_create" ${state.filters.serverStatus === "pending_create" ? "selected" : ""}>待建立</option><option value="pending_update" ${state.filters.serverStatus === "pending_update" ? "selected" : ""}>待更新</option><option value="pending_delete" ${state.filters.serverStatus === "pending_delete" ? "selected" : ""}>待刪除</option></select></label>
          </section>
          <div class="filter-row server-filter-row">
            <span class="chip">${icon("database")}協議目錄 v${escapeHTML(state.protocolSchemaVersion ?? "?")}</span>
            <span class="result-count" id="server-result-count"></span>
          </div>` : ""}
        <div id="server-results"></div>
      </div>`;
    renderServerResults();
  }

  function filteredServers() {
    const query = state.filters.serverSearch.trim().toLocaleLowerCase("zh-Hant");
    return state.servers.filter((server) => {
      const protocol = protocolForServer(server);
      if (state.filters.serverCategory && protocol?.category !== state.filters.serverCategory) return false;
      if (state.filters.serverStatus && server.status !== state.filters.serverStatus) return false;
      if (!query) return true;
      const advertise = server.advertise || {};
      return [server.tag, server.type, server.kind, server.source, protocol?.name, protocol?.category, advertise.server, advertise.tls_server_name]
        .map((value) => String(value || "").toLocaleLowerCase("zh-Hant"))
        .join("\n")
        .includes(query);
    });
  }

  function renderServerResults() {
    const container = document.getElementById("server-results");
    if (!container) return;
    const focusKey = focusedActionKey(container);
    const servers = filteredServers();
    const count = document.getElementById("server-result-count");
    if (count) count.textContent = `顯示 ${integerFormat.format(servers.length)} / ${integerFormat.format(state.servers.length)} 個`;
    container.setAttribute("aria-busy", state.loading.servers ? "true" : "false");

    if (!servers.length) {
      const filtered = Boolean(state.filters.serverSearch || state.filters.serverCategory || state.filters.serverStatus);
      container.innerHTML = `
        <section class="empty-state">
          <div>
            <span class="empty-illustration">${icon(filtered ? "search" : "server", "icon-xl")}</span>
            <h3>${filtered ? "沒有符合條件的節點" : "尚未設定節點"}</h3>
            <p>${filtered ? "調整搜尋文字或篩選條件後再試一次。" : state.protocols.length ? "從 Core 提供的安全範本建立第一個面板節點。" : "協議目錄目前不可用，請重新同步後再建立節點。"}</p>
            ${filtered ? '<button class="button button-tonal" type="button" data-action="reset-server-filters">清除篩選</button>' : state.protocols.length && !state.loading.protocols && !state.reloading ? `<button class="button button-primary" type="button" data-action="new-server">${icon("plus")}新增節點</button>` : ""}
          </div>
        </section>`;
      restoreActionFocus(container, focusKey);
      return;
    }

    const pending = servers.filter((server) => server.status !== "active");
    const active = servers.filter((server) => server.status === "active");
    container.innerHTML = `
      ${pending.length ? renderServerGroup(pending, "等待套用", "重新載入 Core 後，這些變更才會成為運行設定。", true) : ""}
      ${active.length ? renderServerGroup(active, "目前運行", "基礎設定為唯讀；由面板建立的節點可在此維護。", false) : ""}`;
    restoreActionFocus(container, focusKey);
  }

  function renderServerGroup(servers, title, description, pending) {
    const mobile = useMobileDataLists();
    return `
      <section class="server-group ${pending ? "pending" : ""}" aria-labelledby="server-group-${pending ? "pending" : "active"}">
        <div class="server-group-heading">
          <div><h3 id="server-group-${pending ? "pending" : "active"}">${escapeHTML(title)}</h3><p>${escapeHTML(description)}</p></div>
          <span class="server-group-count">${escapeHTML(formatInteger(servers.length))}</span>
        </div>
        ${mobile ? `<div class="mobile-cards">${servers.map(renderServerCard).join("")}</div>` : `<div class="data-surface desktop-table">
          <table class="data-table server-table">
            <caption class="sr-only">${escapeHTML(title)}節點清單</caption>
            <colgroup><col style="width:18%"><col style="width:16%"><col style="width:16%"><col style="width:21%"><col style="width:19%"><col style="width:10%"></colgroup>
            <thead><tr><th scope="col">節點</th><th scope="col">協議</th><th scope="col">狀態 / 來源</th><th scope="col">網路能力</th><th scope="col">公開位址</th><th scope="col"><span class="sr-only">操作</span></th></tr></thead>
            <tbody>${servers.map(renderServerRow).join("")}</tbody>
          </table>
        </div>`}
      </section>`;
  }

  function serverActions(server, mobile = false) {
    if (state.reloading) return `<span class="read-only-label">${icon("refresh")}重新載入中</span>`;
    if (state.loading.protocols || state.loading.servers) return `<span class="read-only-label">${icon("refresh")}同步中</span>`;
    const editable = server.source === "dashboard" && Boolean(server.editable) && server.status !== "pending_delete";
    if (!editable) {
      const message = server.status === "pending_delete" ? "等待重新載入" : "基礎設定唯讀";
      return `<span class="read-only-label">${icon(server.status === "pending_delete" ? "clock" : "shield")}${message}</span>`;
    }
    const tag = escapeHTML(server.tag);
    const label = escapeHTML(server.tag || "節點");
    if (mobile) {
      return `<button class="button button-quiet" type="button" data-action="edit-server" data-tag="${tag}">${icon("edit")}編輯</button><button class="icon-button small" type="button" data-action="delete-server" data-tag="${tag}" aria-label="刪除 ${label}">${icon("trash")}</button>`;
    }
    return `<button class="icon-button small" type="button" data-action="edit-server" data-tag="${tag}" aria-label="編輯 ${label}" title="編輯">${icon("edit")}</button><button class="icon-button small" type="button" data-action="delete-server" data-tag="${tag}" aria-label="刪除 ${label}" title="刪除">${icon("trash")}</button>`;
  }

  function renderServerRow(server) {
    const protocol = protocolForServer(server);
    const status = serverStatusInfo(server.status);
    const credential = protocol?.credential || server.credential;
    const tls = tlsLabel(protocol?.tls);
    const updatePolicy = updatePolicyLabel(protocol?.update_policy || server.update_policy);
    const advertise = server.advertise || {};
    return `
      <tr class="${server.status !== "active" ? "pending-row" : ""}">
        <td><span class="cell-primary">${escapeHTML(server.tag || "未命名")}</span><span class="cell-secondary">${escapeHTML(server.kind === "endpoint" ? "端點" : "入站")}</span></td>
        <td><span class="cell-primary">${escapeHTML(protocol?.name || server.type || "未知")}</span><span class="cell-secondary mono">${escapeHTML(server.type || "未知")}${server.managed && credential ? ` · ${escapeHTML(credentialLabel(credential))}` : ""}</span></td>
        <td><span class="status-badge ${status.tone}">${escapeHTML(status.label)}</span><span class="cell-secondary server-source ${server.source === "dashboard" ? "dashboard" : ""}">${server.source === "dashboard" ? "面板管理" : "基礎設定 · 唯讀"}</span></td>
        <td><span class="cell-primary">${escapeHTML(networkLabel(protocol?.network))}</span><span class="cell-secondary">${escapeHTML(tls)} · ${escapeHTML(updatePolicy)}</span></td>
        <td><span class="cell-primary mono" title="${escapeHTML(advertisedAddress(server))}">${escapeHTML(advertisedAddress(server))}</span><span class="cell-secondary">${advertise.tls_server_name ? `SNI ${escapeHTML(advertise.tls_server_name)}` : advertise.insecure ? "允許不安全 TLS" : "使用預設 TLS 驗證"}</span></td>
        <td><div class="table-actions">${serverActions(server)}</div></td>
      </tr>`;
  }

  function renderServerCard(server) {
    const protocol = protocolForServer(server);
    const status = serverStatusInfo(server.status);
    const advertise = server.advertise || {};
    const credential = protocol?.credential || server.credential;
    return `
      <article class="card mobile-data-card server-mobile-card ${server.status !== "active" ? "pending" : ""}">
        <div class="mobile-card-heading">
          <div><h3>${escapeHTML(server.tag || "未命名")}</h3><p>${escapeHTML(protocol?.name || server.type || "未知")} · ${escapeHTML(server.kind === "endpoint" ? "端點" : "入站")}</p></div>
          <span class="status-badge ${status.tone}">${escapeHTML(status.label)}</span>
        </div>
        <div class="server-owner-row"><span class="chip ${server.source === "dashboard" ? "primary" : ""}">${server.source === "dashboard" ? server.status === "pending_delete" ? "面板管理 · 待移除" : "面板管理 · 可編輯" : "基礎設定 · 唯讀"}</span>${server.managed && credential ? `<span class="chip">${escapeHTML(credentialLabel(credential))}</span>` : ""}</div>
        <dl class="mobile-detail-grid">
          <div class="detail-item"><dt>網路</dt><dd>${escapeHTML(networkLabel(protocol?.network))}</dd></div>
          <div class="detail-item"><dt>TLS</dt><dd>${escapeHTML(tlsLabel(protocol?.tls))}</dd></div>
          <div class="detail-item"><dt>更新策略</dt><dd>${escapeHTML(updatePolicyLabel(protocol?.update_policy || server.update_policy))}</dd></div>
          <div class="detail-item"><dt>公開位址</dt><dd class="mono">${escapeHTML(advertisedAddress(server))}</dd></div>
          ${advertise.tls_server_name ? `<div class="detail-item server-detail-wide"><dt>TLS Server Name</dt><dd class="mono">${escapeHTML(advertise.tls_server_name)}</dd></div>` : ""}
        </dl>
        <div class="mobile-card-actions">${serverActions(server, true)}</div>
      </article>`;
  }

  function renderUsers() {
    const main = document.getElementById("main-content");
    if (!main) return;
    if (!state.loaded.users && !state.errors.users) {
      main.innerHTML = loadingPage("users");
      main.setAttribute("aria-busy", "true");
      return;
    }
    if (!state.loaded.users && state.errors.users) {
      main.innerHTML = errorState("無法載入用戶", state.errors.users, "refresh-users");
      main.setAttribute("aria-busy", "false");
      return;
    }

    const managed = managedInbounds();
    main.setAttribute("aria-busy", state.loading.users ? "true" : "false");
    main.innerHTML = `
      <div class="page-enter">
        <div class="page-heading">
          <div>
            <h2>用戶管理</h2>
            <p>按名稱聚合邏輯用戶，集中管理跨節點憑證、額度與到期規則。</p>
          </div>
          <div class="page-actions">
            <button class="icon-button ${state.loading.users ? "spin" : ""}" type="button" data-action="refresh-users" aria-label="刷新用戶" title="刷新用戶" ${state.loading.users ? "disabled" : ""}>${icon("refresh")}</button>
            <button class="button button-primary" type="button" data-action="new-user" ${managed.length ? "" : "disabled"}>${icon("plus")}<span>新增用戶</span></button>
          </div>
        </div>

        <section class="toolbar-card" aria-label="用戶搜尋與篩選">
          <label class="search-field">
            <span class="sr-only">搜尋用戶</span>
            ${icon("search")}
            <input type="search" data-filter="user-search" value="${escapeHTML(state.filters.userSearch)}" placeholder="搜尋名稱、入站、協議…" autocomplete="off">
            <button class="icon-button small search-clear" type="button" data-action="clear-user-search" aria-label="清除搜尋" ${state.filters.userSearch ? "" : "hidden"}>${icon("close")}</button>
          </label>
          <label>
            <span class="sr-only">依入站篩選</span>
            <select class="select-control" data-filter="user-inbound">
              <option value="">所有入站</option>
              ${state.inbounds.filter((item) => item.managed).map((inbound) => `<option value="${escapeHTML(inbound.tag)}" ${state.filters.userInbound === inbound.tag ? "selected" : ""}>${escapeHTML(inbound.tag)} · ${escapeHTML(inbound.type)}</option>`).join("")}
            </select>
          </label>
        </section>

        <div class="filter-row">
          <div class="segmented-control" role="group" aria-label="用戶狀態">
            ${userStatusSegment("all", "全部")}
            ${userStatusSegment("valid", "有效")}
            ${userStatusSegment("disabled", "停用")}
            ${userStatusSegment("expired", "到期")}
            ${userStatusSegment("exhausted", "額度用盡")}
          </div>
          <span class="result-count" id="user-result-count"></span>
        </div>
        <div id="user-results"></div>
      </div>`;
    renderUserResults();
  }

  function userStatusSegment(value, label) {
    const active = state.filters.userStatus === value;
    return `<button class="segment" type="button" data-user-status="${value}" aria-pressed="${active}">${label}</button>`;
  }

  function getUserStatus(user) {
    const now = Date.now();
    const used = asNumber(user.upload_bytes) + asNumber(user.download_bytes);
    const expired = asNumber(user.expires_at) > 0 && Number(user.expires_at) <= now;
    const exhausted = asNumber(user.quota_bytes) > 0 && used >= Number(user.quota_bytes);
    if (!user.enabled) return { key: "disabled", label: "已停用", className: "" };
    if (expired) return { key: "expired", label: "已到期", className: "warning" };
    if (exhausted) return { key: "exhausted", label: "額度用盡", className: "danger" };
    return { key: "valid", label: "有效", className: "success" };
  }

  function logicalUserGroups() {
    if (derivedCache.users === state.users && derivedCache.accounts === state.accounts) return derivedCache.userGroups;
    const accounts = new Map(state.accounts.map((account) => [account.id, account]));
    const groups = new Map();
    state.users.forEach((user) => {
      const key = user.account_id || user.name;
      let group = groups.get(key);
      if (!group) {
        const account = accounts.get(user.account_id) || null;
        group = { name: user.name, account, memberships: [], upload_bytes: 0, download_bytes: 0, active_connections: 0, online_ips: [], onlineIPAddresses: new Set() };
        groups.set(key, group);
      }
      group.memberships.push(user);
      group.upload_bytes += asNumber(user.upload_bytes);
      group.download_bytes += asNumber(user.download_bytes);
      group.active_connections += asNumber(user.active_connections);
      (user.online_ips || []).forEach((entry) => {
        if (!group.onlineIPAddresses.has(entry.address)) {
          group.onlineIPAddresses.add(entry.address);
          group.online_ips.push(entry);
        }
      });
    });
    groups.forEach((group) => {
      if (group.account?.policy_scope !== "account_global") return;
      group.name = group.account.name;
      group.upload_bytes = asNumber(group.account.upload_bytes);
      group.download_bytes = asNumber(group.account.download_bytes);
      group.active_connections = asNumber(group.account.active_connections);
      group.online_ips = Array.isArray(group.account.online_ips) ? group.account.online_ips : [];
    });
    const result = [...groups.values()].sort((left, right) => left.name.localeCompare(right.name, "zh-Hant"));
    result.forEach((group) => { delete group.onlineIPAddresses; });
    derivedCache.users = state.users;
    derivedCache.accounts = state.accounts;
    derivedCache.userGroups = result;
    return result;
  }

  function getUserGroupStatus(group) {
    if (group.account?.policy_scope === "account_global") {
      const accountStatus = getUserStatus(group.account);
      if (accountStatus.key !== "valid") return accountStatus;
      if (!group.memberships.some((membership) => membership.enabled)) {
        return { key: "disabled", label: "所有節點已停用", className: "" };
      }
      return accountStatus;
    }
    let fallback = null;
    let expired = null;
    let exhausted = null;
    for (const membership of group.memberships) {
      const status = getUserStatus(membership);
      fallback ||= status;
      if (status.key === "disabled") return status;
      if (status.key === "expired") expired ||= status;
      if (status.key === "exhausted") exhausted ||= status;
    }
    return expired || exhausted || fallback || { key: "disabled", label: "無節點", className: "" };
  }

  function filteredUsers(groups = logicalUserGroups()) {
    const query = state.filters.userSearch.trim().toLocaleLowerCase("zh-Hant");
    return groups.filter((group) => {
      if (state.filters.userInbound && !group.memberships.some((user) => user.inbound === state.filters.userInbound)) return false;
      const status = getUserGroupStatus(group);
      if (state.filters.userStatus !== "all" && status.key !== state.filters.userStatus) return false;
      if (!query) return true;
      const haystack = [group.name, ...group.memberships.flatMap((user) => [user.inbound, user.type])].map((value) => String(value || "").toLocaleLowerCase("zh-Hant")).join("\n");
      return haystack.includes(query);
    });
  }

  function renderUserResults() {
    const container = document.getElementById("user-results");
    if (!container) return;
    const focusKey = focusedActionKey(container);
    const allGroups = logicalUserGroups();
    const users = filteredUsers(allGroups);
    const count = document.getElementById("user-result-count");
    if (count) count.textContent = `顯示 ${integerFormat.format(users.length)} / ${integerFormat.format(allGroups.length)} 位`;
    container.setAttribute("aria-busy", state.loading.users ? "true" : "false");

    if (!users.length) {
      const filtered = Boolean(state.filters.userSearch || state.filters.userInbound || state.filters.userStatus !== "all");
      const hasManaged = managedInbounds().length > 0;
      container.innerHTML = `
        <section class="empty-state">
          <div>
            <span class="empty-illustration">${icon(filtered ? "search" : "users", "icon-xl")}</span>
            <h3>${filtered ? "沒有符合條件的用戶" : "尚未建立用戶"}</h3>
            <p>${filtered ? "調整搜尋文字或篩選條件後再試一次。" : hasManaged ? "從受管理入站建立第一位用戶。" : "目前沒有支援動態用戶管理的入站。"}</p>
            ${filtered ? '<button class="button button-tonal" type="button" data-action="reset-user-filters">清除篩選</button>' : hasManaged ? `<button class="button button-primary" type="button" data-action="new-user">${icon("plus")}新增用戶</button>` : ""}
          </div>
        </section>`;
      restoreActionFocus(container, focusKey);
      return;
    }

    const mobile = useMobileDataLists();
    container.innerHTML = mobile ? `<div class="mobile-cards">${users.map(renderUserCard).join("")}</div>` : `
      <div class="data-surface desktop-table">
        <table class="data-table">
          <caption class="sr-only">用戶清單</caption>
          <colgroup><col style="width:20%"><col style="width:15%"><col style="width:13%"><col style="width:22%"><col style="width:12%"><col style="width:18%"></colgroup>
          <thead><tr><th scope="col">用戶</th><th scope="col">入站</th><th scope="col">狀態</th><th scope="col">流量額度</th><th scope="col">連線 / 到期</th><th scope="col"><span class="sr-only">操作</span></th></tr></thead>
          <tbody>${users.map(renderUserRow).join("")}</tbody>
        </table>
      </div>`;
    restoreActionFocus(container, focusKey);
  }

  function renderQuota(user) {
    const inbound = inboundFor(user.inbound);
    if (inbound?.traffic === false) {
      return '<span class="cell-secondary">此協議未提供個別流量統計</span>';
    }
    const used = asNumber(user.upload_bytes) + asNumber(user.download_bytes);
    const quota = asNumber(user.quota_bytes);
    if (!quota) {
      return `
        <div class="quota-wrap" aria-label="已使用 ${escapeHTML(formatBytes(used))}，額度不限">
          <div class="quota-label"><span>${escapeHTML(formatBytes(used))}</span><span>不限</span></div>
          <div class="progress-track"><span class="progress-value" style="width:0"></span></div>
        </div>`;
    }
    const percent = Math.min(100, Math.max(0, (used / quota) * 100));
    const tone = percent >= 100 ? "danger" : percent >= 80 ? "warning" : "";
    return `
      <div class="quota-wrap">
        <div class="quota-label"><span>${escapeHTML(formatBytes(used))}</span><span>${escapeHTML(decimalFormat.format(percent))}%</span></div>
        <div class="progress-track" role="progressbar" aria-label="流量額度" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${percent.toFixed(1)}">
          <span class="progress-value ${tone}" style="width:${percent.toFixed(1)}%"></span>
        </div>
        <span class="cell-secondary">上限 ${escapeHTML(formatBytes(quota))}</span>
      </div>`;
  }

  function renderGroupQuota(group) {
    const trafficMembers = group.memberships.filter((user) => inboundFor(user.inbound)?.traffic !== false);
    if (!trafficMembers.length) return '<span class="cell-secondary">節點未提供個別流量統計</span>';
    if (group.account?.policy_scope === "account_global") {
      const used = asNumber(group.upload_bytes) + asNumber(group.download_bytes);
      const quota = asNumber(group.account.quota_bytes);
      return `<span class="cell-primary">${escapeHTML(formatBytes(used))}</span><span class="cell-secondary">${quota ? `全域上限 ${escapeHTML(formatBytes(quota))}` : "全域額度不限"}</span>`;
    }
    const used = trafficMembers.reduce((total, user) => total + asNumber(user.upload_bytes) + asNumber(user.download_bytes), 0);
    const limited = trafficMembers.filter((user) => asNumber(user.quota_bytes) > 0);
    return `<span class="cell-primary">${escapeHTML(formatBytes(used))}</span><span class="cell-secondary">${limited.length ? `${limited.length} 個節點設有額度` : "所有節點不限額"}</span>`;
  }

  function renderUserRow(group) {
    const status = getUserGroupStatus(group);
    const rawName = group.name || "未命名";
    const name = escapeHTML(rawName);
    const encodedName = escapeHTML(rawName);
    const createdAt = Math.min(...group.memberships.map((user) => Number(user.created_at) || Date.now()));
    return `
      <tr>
        <td><span class="cell-primary">${name}</span><span class="cell-secondary">建立於 ${escapeHTML(formatTime(createdAt))}</span></td>
        <td><span class="cell-primary">${escapeHTML(formatInteger(group.memberships.length))} 個節點</span><span class="cell-secondary user-node-list">${group.memberships.map((user) => escapeHTML(user.inbound)).join(" · ")}</span></td>
        <td><span class="status-badge ${status.className}">${status.label}</span></td>
        <td>${renderGroupQuota(group)}</td>
        <td><span class="cell-primary">${escapeHTML(formatInteger(group.active_connections))} 條</span><span class="cell-secondary">${escapeHTML(formatInteger(group.online_ips.length))} 個在線 IP · 跨 ${escapeHTML(formatInteger(group.memberships.length))} 個節點</span></td>
        <td>
          <div class="table-actions">
            <button class="icon-button small" type="button" data-action="edit-user" data-name="${encodedName}" aria-label="編輯 ${name}" title="編輯">${icon("edit")}</button>
            ${group.memberships.some((user) => inboundFor(user.inbound)?.traffic !== false) ? `<button class="icon-button small" type="button" data-action="reset-user-traffic" data-name="${encodedName}" aria-label="將 ${name} 的流量歸零" title="流量歸零">${icon("reset")}</button>` : ""}
            <button class="icon-button small" type="button" data-action="delete-user" data-name="${encodedName}" aria-label="刪除 ${name}" title="刪除">${icon("trash")}</button>
          </div>
        </td>
      </tr>`;
  }

  function renderUserCard(group) {
    const status = getUserGroupStatus(group);
    const rawName = group.name || "未命名";
    const name = escapeHTML(rawName);
    const encodedName = escapeHTML(rawName);
    return `
      <article class="card mobile-data-card">
        <div class="mobile-card-heading">
          <div><h3>${name}</h3><p>${group.memberships.map((user) => escapeHTML(user.inbound)).join(" · ")}</p></div>
          <span class="status-badge ${status.className}">${status.label}</span>
        </div>
        <dl class="mobile-detail-grid">
          <div class="detail-item"><dt>節點</dt><dd>${escapeHTML(formatInteger(group.memberships.length))} 個</dd></div>
          <div class="detail-item"><dt>活躍連線</dt><dd>${escapeHTML(formatInteger(group.active_connections))} 條</dd></div>
          <div class="detail-item"><dt>在線 IP</dt><dd>${escapeHTML(formatInteger(group.online_ips.length))} 個</dd></div>
          <div class="detail-item"><dt>上行</dt><dd>${escapeHTML(formatBytes(group.upload_bytes))}</dd></div>
          <div class="detail-item"><dt>下行</dt><dd>${escapeHTML(formatBytes(group.download_bytes))}</dd></div>
        </dl>
        <div class="mobile-card-quota">${renderGroupQuota(group)}</div>
        <div class="mobile-card-actions">
          ${group.memberships.some((user) => inboundFor(user.inbound)?.traffic !== false) ? `<button class="button button-quiet" type="button" data-action="reset-user-traffic" data-name="${encodedName}">${icon("reset")}歸零</button>` : ""}
          <button class="button button-quiet" type="button" data-action="edit-user" data-name="${encodedName}">${icon("edit")}編輯</button>
          <button class="icon-button small" type="button" data-action="delete-user" data-name="${encodedName}" aria-label="刪除 ${name}">${icon("trash")}</button>
        </div>
      </article>`;
  }

  function renderConnections() {
    const main = document.getElementById("main-content");
    if (!main) return;
    if (!state.loaded.connections && !state.errors.connections) {
      main.innerHTML = loadingPage("connections");
      main.setAttribute("aria-busy", "true");
      return;
    }
    if (!state.loaded.connections && state.errors.connections) {
      main.innerHTML = errorState("無法載入連線", state.errors.connections, "refresh-connections");
      main.setAttribute("aria-busy", "false");
      return;
    }

    const inboundOptions = [...new Set(state.connections.map((connection) => connection.inbound).filter(Boolean))].sort();
    const networkOptions = [...new Set(state.connections.map((connection) => connection.network).filter(Boolean))].sort();
    main.setAttribute("aria-busy", state.loading.connections ? "true" : "false");
    main.innerHTML = `
      <div class="page-enter">
        <div class="page-heading">
          <div>
            <h2>即時連線</h2>
            <p>每 3 秒同步中的活動工作階段，可立即中止單一或全部連線。</p>
          </div>
          <div class="page-actions">
            <button class="icon-button ${state.loading.connections ? "spin" : ""}" type="button" data-action="refresh-connections" aria-label="刷新連線" title="刷新連線" ${state.loading.connections ? "disabled" : ""}>${icon("refresh")}</button>
            <button class="button button-danger" id="close-all-connections" type="button" data-action="close-all-connections" ${state.connections.length ? "" : "disabled"}>${icon("close")}<span>全部關閉</span></button>
          </div>
        </div>

        <section class="toolbar-card" aria-label="連線搜尋與篩選">
          <label class="search-field">
            <span class="sr-only">搜尋連線</span>
            ${icon("search")}
            <input type="search" data-filter="connection-search" value="${escapeHTML(state.filters.connectionSearch)}" placeholder="搜尋用戶、來源或目的地…" autocomplete="off">
            <button class="icon-button small search-clear" type="button" data-action="clear-connection-search" aria-label="清除搜尋" ${state.filters.connectionSearch ? "" : "hidden"}>${icon("close")}</button>
          </label>
          <label><span class="sr-only">依入站篩選</span><select class="select-control" data-filter="connection-inbound"><option value="">所有入站</option>${inboundOptions.map((value) => `<option value="${escapeHTML(value)}" ${state.filters.connectionInbound === value ? "selected" : ""}>${escapeHTML(value)}</option>`).join("")}</select></label>
          <label><span class="sr-only">依網路類型篩選</span><select class="select-control" data-filter="connection-network"><option value="">所有網路</option>${networkOptions.map((value) => `<option value="${escapeHTML(value)}" ${state.filters.connectionNetwork === value ? "selected" : ""}>${escapeHTML(value).toUpperCase()}</option>`).join("")}</select></label>
        </section>
        <div class="filter-row">
          <span class="chip success">${icon("activity")}每 3 秒自動更新</span>
          <span class="result-count" id="connection-result-count"></span>
        </div>
        <div id="connection-results"></div>
      </div>`;
    renderConnectionResults();
  }

  function publicSettingsURL(path) {
    const base = String(state.settings?.public_base_url || "").replace(/\/$/, "");
    return `${base}${path || "/"}user-token`;
  }

  function settingsRouteState(subscriptionPath, profilePath, legacyEnabled) {
    if (legacyEnabled) {
      return { tone: "warning", iconName: "alert", message: "Core 舊版入口仍會保留。確認所有原生用戶完成遷移後再關閉。" };
    }
    if (subscriptionPath === DEFAULT_SUBSCRIPTION_PATH || profilePath === DEFAULT_PROFILE_PATH) {
      return { tone: "warning", iconName: "alert", message: "雖未保留舊版別名，目前仍有路徑使用預設值；改為自訂路徑才能撤銷該入口。" };
    }
    return { tone: "success", iconName: "check", message: "不保留 Core 舊版入口；仍使用舊連結的客戶端將無法更新。" };
  }

  function render3XUIImportReport() {
    const report = state.import3XUI.report;
    if (!report) {
      const targets = state.inbounds.filter((inbound) => inbound.managed).map((inbound) => inbound.tag);
      return `
        <div class="import-empty" id="import-report" role="status">
          ${icon("database")}
          <span>先執行預檢。可映射的 Sidera inbound：${targets.length ? targets.map(escapeHTML).join("、") : "目前沒有可管理的 inbound"}</span>
        </div>`;
    }
    const summary = report.summary || {};
    const source = report.source || {};
    const issues = Array.isArray(report.issues) ? report.issues : [];
    const inbounds = Array.isArray(report.inbounds) ? report.inbounds : [];
    const issueLimit = 100;
    const inboundLimit = 100;
    return `
      <div class="import-report ${report.ready ? "ready" : "blocked"} ${state.import3XUI.reportStale ? "stale" : ""}" id="import-report" role="status">
        ${state.import3XUI.reportStale ? `<div class="import-stale-notice">${icon("alert")}<span>資料庫或 mapping 已變更；此報告僅供參考，請重新執行預檢。</span></div>` : ""}
        <div class="import-report-heading">
          <span class="settings-card-icon ${report.ready ? "preview" : "danger"}">${icon(report.ready ? "check" : "alert")}</span>
          <div>
            <span class="eyebrow">${report.ready ? "可以正式匯入" : "需要處理阻擋項目"}</span>
            <h4>${escapeHTML(formatInteger(summary.creatable_accounts))} 個帳戶可建立，${escapeHTML(formatInteger(summary.blocked_accounts))} 個遭阻擋</h4>
            <p>來源包含 ${escapeHTML(formatInteger(source.accounts))} 個帳戶、${escapeHTML(formatInteger(source.memberships))} 個 membership 與 ${escapeHTML(formatInteger(source.inbounds))} 個 inbound。</p>
          </div>
        </div>
        <div class="import-summary-grid" aria-label="預檢摘要">
          <div><span>錯誤</span><strong>${escapeHTML(formatInteger(summary.errors))}</strong></div>
          <div><span>警告</span><strong>${escapeHTML(formatInteger(summary.warnings))}</strong></div>
          <div><span>Fingerprint</span><code>${escapeHTML(report.fingerprint || "-")}</code></div>
        </div>
        ${inbounds.length ? `
          <div class="import-report-section">
            <h5>Inbound 映射</h5>
            <div class="import-inbound-list">
              ${inbounds.slice(0, inboundLimit).map((inbound) => `
                <div><code>${escapeHTML(inbound.source_id)}</code><span>${escapeHTML(inbound.tag || inbound.remark || "未命名")} · ${escapeHTML(inbound.protocol)}</span><strong>${inbound.target_tag ? `→ ${escapeHTML(inbound.target_tag)}` : "未映射"}</strong></div>`).join("")}
            </div>
            ${inbounds.length > inboundLimit ? `<p class="import-truncated">另有 ${escapeHTML(formatInteger(inbounds.length - inboundLimit))} 筆未顯示。</p>` : ""}
          </div>` : ""}
        ${issues.length ? `
          <div class="import-report-section">
            <h5>相容性問題</h5>
            <div class="import-issue-list">
              ${issues.slice(0, issueLimit).map((issue) => `
                <div class="${issue.severity === "error" ? "error" : "warning"}">
                  ${icon(issue.severity === "error" ? "alert" : "sparkles")}
                  <span><strong>${escapeHTML(issue.message)}</strong><code>${escapeHTML(issue.code)} · ${escapeHTML(issue.path)}</code></span>
                </div>`).join("")}
            </div>
            ${issues.length > issueLimit ? `<p class="import-truncated">另有 ${escapeHTML(formatInteger(issues.length - issueLimit))} 筆未顯示；請透過 API 取得完整報告。</p>` : ""}
          </div>` : ""}
      </div>`;
  }

  function renderSettings() {
    const main = document.getElementById("main-content");
    if (!main) return;
    if (!state.loaded.settings && !state.errors.settings) {
      main.innerHTML = loadingPage("settings");
      main.setAttribute("aria-busy", "true");
      return;
    }
    if (!state.loaded.settings && state.errors.settings) {
      main.innerHTML = errorState("無法載入安全設定", state.errors.settings, "refresh-settings");
      main.setAttribute("aria-busy", "false");
      return;
    }

    const settings = state.settings || {};
    const subscriptionPath = settings.subscription_path || DEFAULT_SUBSCRIPTION_PATH;
    const profilePath = settings.profile_page_path || DEFAULT_PROFILE_PATH;
    const legacyEnabled = Boolean(settings.legacy_routes_enabled);
    const routeState = settingsRouteState(subscriptionPath, profilePath, legacyEnabled);
    const busy = state.loading.settings || state.savingSettings;
    const importAvailable = Boolean(state.overview?.features?.three_x_ui_import);
    main.setAttribute("aria-busy", busy || state.import3XUI.busy ? "true" : "false");
    main.innerHTML = `
      <div class="page-enter settings-page">
        <div class="page-heading">
          <div>
            <h2>訂閱安全</h2>
            <p>輪替 Core 原生訂閱入口並撤銷舊路徑，不需重新載入 Core。</p>
          </div>
          <div class="page-actions">
            <button class="icon-button ${state.loading.settings ? "spin" : ""}" type="button" data-action="refresh-settings" aria-label="重新載入設定" title="重新載入設定" ${busy ? "disabled" : ""}>${icon("refresh")}</button>
          </div>
        </div>

        <section class="settings-security-banner" aria-label="路徑安全提醒">
          <span class="settings-security-mark">${icon("shield", "icon-lg")}</span>
          <div>
            <span class="eyebrow">公開入口防護</span>
            <h3>讓可猜測的預設路徑退出服務</h3>
            <p>自訂 Core 原生路徑可降低自動掃描，但不會取代每位用戶的訂閱 Token。懷疑連結外洩時，仍應更換該用戶的訂閱識別碼。</p>
          </div>
        </section>

        <div class="settings-layout">
          <form class="card settings-form-card" data-form="settings" novalidate>
            <div class="settings-card-heading">
              <span class="settings-card-icon">${icon("link")}</span>
              <div><h3>公開路徑</h3><p>路徑儲存後立即生效；所有值都必須以斜線開頭及結尾。</p></div>
            </div>
            <div class="schema-note settings-scope-note">${icon("alert")}<span>此頁只控制可設定入口。3x-ui 相容的 /sub/{id} 與 Caddy 代理不會被這個開關撤銷。</span></div>
            <div class="form-error" id="settings-form-error" role="alert" hidden></div>
            <fieldset class="settings-fieldset" ${busy ? "disabled" : ""}>
              <div class="settings-form-grid">
                <div class="form-field">
                  <label for="subscription-path">訂閱內容路徑 <span class="required-mark" aria-hidden="true">*</span></label>
                  <input class="text-input" id="subscription-path" name="subscription_path" value="${escapeHTML(subscriptionPath)}" maxlength="128" autocomplete="off" spellcheck="false" aria-describedby="subscription_path-error">
                  <span class="supporting-text field-message" id="subscription_path-error" data-help="只可使用英數字、-、_ 與路徑分隔符。">只可使用英數字、-、_ 與路徑分隔符。</span>
                </div>
                <div class="form-field">
                  <label for="profile-page-path">資訊頁路徑 <span class="required-mark" aria-hidden="true">*</span></label>
                  <input class="text-input" id="profile-page-path" name="profile_page_path" value="${escapeHTML(profilePath)}" maxlength="128" autocomplete="off" spellcheck="false" aria-describedby="profile_page_path-error">
                  <span class="supporting-text field-message" id="profile_page_path-error" data-help="不可與訂閱路徑、管理 API 或 dashboard 重疊。">不可與訂閱路徑、管理 API 或 dashboard 重疊。</span>
                </div>
                <label class="switch-row settings-legacy-switch">
                  <span class="switch-copy"><strong>保留 Core 舊版入口</strong><span>變更路徑後，繼續接受預設的原生訂閱與資訊頁入口。</span></span>
                  <span class="switch-control"><input name="legacy_routes_enabled" type="checkbox" ${legacyEnabled ? "checked" : ""}><span class="switch-track" aria-hidden="true"></span></span>
                </label>
              </div>
            </fieldset>
            <div class="settings-form-actions">
              <button class="button button-outline" type="button" data-action="reset-settings-paths" ${busy ? "disabled" : ""}>${icon("reset")}預設值</button>
              <button class="button button-primary ${state.savingSettings ? "is-loading" : ""}" type="submit" data-settings-submit ${busy ? "disabled" : ""}>${icon(state.savingSettings ? "refresh" : "check")}<span>${state.savingSettings ? "儲存中…" : "儲存設定"}</span></button>
            </div>
          </form>

          <aside class="card settings-preview-card" aria-labelledby="settings-preview-title">
            <div class="settings-card-heading compact">
              <span class="settings-card-icon preview">${icon("network")}</span>
              <div><h3 id="settings-preview-title">生效後入口</h3><p>${settings.public_base_url ? `公開來源：${escapeHTML(settings.public_base_url)}` : "尚未設定公開 Base URL，以下僅顯示路徑。"}</p></div>
            </div>
            <dl class="settings-preview-list">
              <div><dt>訂閱內容</dt><dd><code id="settings-subscription-preview">${escapeHTML(publicSettingsURL(subscriptionPath))}</code></dd></div>
              <div><dt>用戶資訊頁</dt><dd><code id="settings-profile-preview">${escapeHTML(publicSettingsURL(profilePath))}</code></dd></div>
            </dl>
            <div class="settings-route-state ${routeState.tone}" id="settings-route-state">
              ${icon(routeState.iconName)}
              <span>${routeState.message}</span>
            </div>
          </aside>
        </div>

        ${importAvailable ? `
        <section class="card settings-import-card" aria-labelledby="settings-import-title">
          <div class="settings-card-heading compact">
            <span class="settings-card-icon">${icon("database")}</span>
            <div><h3 id="settings-import-title">3x-ui 3.5.0 資料匯入</h3><p>以相同資料庫完成預檢與正式套用；來源憑證不會顯示於報告。</p></div>
          </div>
          <div class="schema-note settings-scope-note">${icon("shield")}<span>正式匯入會建立 account-global 帳戶、membership、流量基線及新舊訂閱識別碼。任何 runtime 或儲存失敗都會回滾。</span></div>
          <form class="import-form" data-form="3x-ui-import" novalidate>
            <div class="form-error" id="import-form-error" role="alert" ${state.import3XUI.error ? "" : "hidden"}>${state.import3XUI.error ? `${icon("alert")}<span>${escapeHTML(state.import3XUI.error)}</span>` : ""}</div>
            <fieldset class="settings-fieldset" ${state.import3XUI.busy ? "disabled" : ""}>
              <div class="import-form-grid">
                <div class="form-field">
                  <label for="import-database">3x-ui SQLite 備份 <span class="required-mark" aria-hidden="true">*</span></label>
                  <input class="text-input import-file-input" id="import-database" name="database" type="file" accept=".db,.sqlite,.sqlite3,application/vnd.sqlite3">
                  <span class="supporting-text" id="import-file-state">${state.import3XUI.file ? `已保留：${escapeHTML(state.import3XUI.file.name)} · ${escapeHTML(formatBytes(state.import3XUI.file.size))}；重新選檔會使報告失效。` : "最大 256 MiB；檔案只會送往目前的 Core。"}</span>
                </div>
                <div class="form-field">
                  <label for="import-inbound-map">Inbound mapping JSON</label>
                  <textarea class="text-input import-map-editor" id="import-inbound-map" name="inbound_map" spellcheck="false" aria-describedby="import-map-help">${escapeHTML(state.import3XUI.mapping)}</textarea>
                  <span class="supporting-text" id="import-map-help">以 3x-ui inbound ID 映射到現有 Sidera tag，例如 {"1":"reality-in"}。</span>
                </div>
              </div>
            </fieldset>
            <div class="settings-form-actions import-actions">
              <button class="button button-outline ${state.import3XUI.busy && !state.import3XUI.applying ? "is-loading" : ""}" type="submit" ${state.import3XUI.busy ? "disabled" : ""}>${icon(state.import3XUI.busy && !state.import3XUI.applying ? "refresh" : "shield")}<span>${state.import3XUI.busy && !state.import3XUI.applying ? "預檢中…" : "執行預檢"}</span></button>
              <button class="button button-primary ${state.import3XUI.applying ? "is-loading" : ""}" type="button" data-action="apply-3x-ui-import" ${state.import3XUI.busy || state.import3XUI.reportStale || !state.import3XUI.report?.ready ? "disabled" : ""}>${icon(state.import3XUI.applying ? "refresh" : "database")}<span>${state.import3XUI.applying ? "匯入中…" : "確認正式匯入"}</span></button>
            </div>
          </form>
          ${render3XUIImportReport()}
        </section>` : `
        <section class="card settings-import-card" aria-labelledby="settings-import-title">
          <div class="settings-card-heading compact">
            <span class="settings-card-icon">${icon("database")}</span>
            <div><h3 id="settings-import-title">3x-ui 資料匯入未啟用</h3><p>此精簡版本未連結 SQLite 引擎；請使用含 <code>with_3xui_import</code> build tag 的版本執行匯入。</p></div>
          </div>
        </section>`}
      </div>`;
  }

  function updateSettingsPreview(form) {
    const subscriptionPath = form.elements.subscription_path.value.trim();
    const profilePath = form.elements.profile_page_path.value.trim();
    const subscriptionPreview = document.getElementById("settings-subscription-preview");
    const profilePreview = document.getElementById("settings-profile-preview");
    if (subscriptionPreview) subscriptionPreview.textContent = publicSettingsURL(subscriptionPath);
    if (profilePreview) profilePreview.textContent = publicSettingsURL(profilePath);
    const legacyEnabled = form.elements.legacy_routes_enabled.checked;
    const stateView = settingsRouteState(subscriptionPath, profilePath, legacyEnabled);
    const routeState = document.getElementById("settings-route-state");
    if (routeState) {
      routeState.className = `settings-route-state ${stateView.tone}`;
      routeState.innerHTML = `${icon(stateView.iconName)}<span>${stateView.message}</span>`;
    }
  }

  function validateSettingsForm(form) {
    const subscriptionPath = form.elements.subscription_path.value.trim();
    const profilePath = form.elements.profile_page_path.value.trim();
    const errors = {};
    const safePath = /^\/[A-Za-z0-9_-]+(?:\/[A-Za-z0-9_-]+)*\/$/;
    if (!safePath.test(subscriptionPath)) errors.subscription_path = "訂閱路徑必須以 / 開頭與結尾，且只能包含英數字、-、_ 與路徑分隔符。";
    if (!safePath.test(profilePath)) errors.profile_page_path = "資訊頁路徑必須以 / 開頭與結尾，且只能包含英數字、-、_ 與路徑分隔符。";
    if (subscriptionPath.length > 128) errors.subscription_path = "訂閱路徑不可超過 128 個字元。";
    if (profilePath.length > 128) errors.profile_page_path = "資訊頁路徑不可超過 128 個字元。";
    const overlaps = (left, right) => left.startsWith(right) || right.startsWith(left);
    if (!errors.subscription_path && !errors.profile_page_path && overlaps(subscriptionPath, profilePath)) {
      errors.profile_page_path = "訂閱路徑與資訊頁路徑不可重疊。";
    }
    for (const reserved of ["/api/admin/", "/dashboard/"]) {
      if (!errors.subscription_path && overlaps(subscriptionPath, reserved)) errors.subscription_path = "訂閱路徑不可與管理 API 或 dashboard 重疊。";
      if (!errors.profile_page_path && overlaps(profilePath, reserved)) errors.profile_page_path = "資訊頁路徑不可與管理 API 或 dashboard 重疊。";
    }
    if (form.elements.legacy_routes_enabled.checked) {
      if (!errors.subscription_path && subscriptionPath !== DEFAULT_SUBSCRIPTION_PATH && overlaps(subscriptionPath, DEFAULT_PROFILE_PATH)) {
        errors.subscription_path = "訂閱路徑不可與舊版資訊頁路徑重疊；請更換路徑或停用舊版入口。";
      }
      if (!errors.profile_page_path && profilePath !== DEFAULT_PROFILE_PATH && overlaps(profilePath, DEFAULT_SUBSCRIPTION_PATH)) {
        errors.profile_page_path = "資訊頁路徑不可與舊版訂閱路徑重疊；請更換路徑或停用舊版入口。";
      }
    }
    return {
      errors,
      body: {
        subscription_path: subscriptionPath,
        profile_page_path: profilePath,
        legacy_routes_enabled: form.elements.legacy_routes_enabled.checked,
      },
    };
  }

  function filteredConnections() {
    const query = state.filters.connectionSearch.trim().toLocaleLowerCase("zh-Hant");
    return state.connections.filter((connection) => {
      if (state.filters.connectionInbound && connection.inbound !== state.filters.connectionInbound) return false;
      if (state.filters.connectionNetwork && connection.network !== state.filters.connectionNetwork) return false;
      if (!query) return true;
      return [connection.user, connection.source, connection.destination, connection.inbound, connection.protocol, connection.outbound, connection.network]
        .map((value) => String(value || "").toLocaleLowerCase("zh-Hant"))
        .join("\n")
        .includes(query);
    });
  }

  function renderConnectionResults() {
    const container = document.getElementById("connection-results");
    if (!container) return;
    const focusKey = focusedActionKey(container);
    const connections = filteredConnections();
    const count = document.getElementById("connection-result-count");
    if (count) count.textContent = `顯示 ${integerFormat.format(connections.length)} / ${integerFormat.format(state.connections.length)} 條`;
    const closeAll = document.getElementById("close-all-connections");
    if (closeAll) closeAll.disabled = !state.connections.length;
    container.setAttribute("aria-busy", state.loading.connections ? "true" : "false");

    if (!connections.length) {
      const filtered = Boolean(state.filters.connectionSearch || state.filters.connectionInbound || state.filters.connectionNetwork);
      container.innerHTML = `
        <section class="empty-state">
          <div>
            <span class="empty-illustration">${icon(filtered ? "search" : "activity", "icon-xl")}</span>
            <h3>${filtered ? "沒有符合條件的連線" : "目前沒有活躍連線"}</h3>
            <p>${filtered ? "調整搜尋或篩選條件後再試一次。" : "新連線建立後會在三秒內顯示於此。"}</p>
            ${filtered ? '<button class="button button-tonal" type="button" data-action="reset-connection-filters">清除篩選</button>' : ""}
          </div>
        </section>`;
      restoreActionFocus(container, focusKey);
      return;
    }

    const mobile = useMobileDataLists();
    container.innerHTML = mobile ? `<div class="mobile-cards">${connections.map(renderConnectionCard).join("")}</div>` : `
      <div class="data-surface desktop-table">
        <table class="data-table">
          <caption class="sr-only">活躍連線清單</caption>
          <colgroup><col style="width:16%"><col style="width:14%"><col style="width:21%"><col style="width:21%"><col style="width:12%"><col style="width:10%"><col style="width:6%"></colgroup>
          <thead><tr><th scope="col">用戶 / 入站</th><th scope="col">協定</th><th scope="col">來源</th><th scope="col">目的地</th><th scope="col">流量</th><th scope="col">持續時間</th><th scope="col"><span class="sr-only">操作</span></th></tr></thead>
          <tbody>${connections.map(renderConnectionRow).join("")}</tbody>
        </table>
      </div>`;
    restoreActionFocus(container, focusKey);
  }

  function focusedActionKey(container) {
    const active = document.activeElement;
    if (!active || !container.contains(active) || !active.dataset.action) return null;
    return { action: active.dataset.action, id: active.dataset.id || "", name: active.dataset.name || "" };
  }

  function restoreActionFocus(container, key) {
    if (!key) return;
    const match = [...container.querySelectorAll("[data-action]")].find((element) => (
      element.dataset.action === key.action
      && (element.dataset.id || "") === key.id
      && (element.dataset.name || "") === key.name
      && element.getClientRects().length > 0
    ));
    match?.focus();
  }

  function renderConnectionRow(connection) {
    const id = escapeHTML(connection.id);
    const label = escapeHTML(connection.user || connection.destination || "連線");
    return `
      <tr>
        <td><span class="cell-primary">${escapeHTML(connection.user || "未識別用戶")}</span><span class="cell-secondary">${escapeHTML(connection.inbound)} · ${escapeHTML(connection.inbound_type)}</span></td>
        <td><span class="cell-primary">${escapeHTML(String(connection.network || "未知").toUpperCase())}</span><span class="cell-secondary">${escapeHTML(connection.protocol || "未識別")} → ${escapeHTML(connection.outbound || "未指定")}</span></td>
        <td><span class="cell-primary mono" title="${escapeHTML(connection.source)}">${escapeHTML(connection.source)}</span></td>
        <td><span class="cell-primary mono" title="${escapeHTML(connection.destination)}">${escapeHTML(connection.destination)}</span></td>
        <td><span class="cell-primary">↑ ${escapeHTML(formatBytes(connection.upload_bytes))}</span><span class="cell-secondary">↓ ${escapeHTML(formatBytes(connection.download_bytes))}</span></td>
        <td><span class="cell-primary">${escapeHTML(formatConnectionAge(connection.created_at))}</span><span class="cell-secondary">${escapeHTML(formatTime(connection.created_at))}</span></td>
        <td><div class="table-actions"><button class="icon-button small" type="button" data-action="close-connection" data-id="${id}" aria-label="關閉 ${label}" title="關閉連線">${icon("close")}</button></div></td>
      </tr>`;
  }

  function renderConnectionCard(connection) {
    const id = escapeHTML(connection.id);
    const label = escapeHTML(connection.user || connection.destination || "連線");
    return `
      <article class="card mobile-data-card">
        <div class="mobile-card-heading">
          <div><h3>${escapeHTML(connection.user || "未識別用戶")}</h3><p>${escapeHTML(connection.inbound)} · ${escapeHTML(connection.inbound_type)}</p></div>
          <span class="chip success">${escapeHTML(String(connection.network || "未知").toUpperCase())}</span>
        </div>
        <dl class="mobile-detail-grid">
          <div class="detail-item"><dt>來源</dt><dd class="mono">${escapeHTML(connection.source)}</dd></div>
          <div class="detail-item"><dt>目的地</dt><dd class="mono">${escapeHTML(connection.destination)}</dd></div>
          <div class="detail-item"><dt>路由</dt><dd>${escapeHTML(connection.protocol || "未識別")} → ${escapeHTML(connection.outbound || "未指定")}</dd></div>
          <div class="detail-item"><dt>持續時間</dt><dd>${escapeHTML(formatConnectionAge(connection.created_at))}</dd></div>
          <div class="detail-item"><dt>上行</dt><dd>${escapeHTML(formatBytes(connection.upload_bytes))}</dd></div>
          <div class="detail-item"><dt>下行</dt><dd>${escapeHTML(formatBytes(connection.download_bytes))}</dd></div>
        </dl>
        <div class="mobile-card-actions"><button class="button button-danger" type="button" data-action="close-connection" data-id="${id}" aria-label="關閉 ${label}">${icon("close")}關閉連線</button></div>
      </article>`;
  }

  function managedInbounds() {
    return state.inbounds.filter((inbound) => Boolean(inbound.managed));
  }

  function inboundFor(tag) {
    if (derivedCache.inbounds !== state.inbounds) {
      derivedCache.inbounds = state.inbounds;
      derivedCache.inboundByTag = new Map(state.inbounds.map((inbound) => [inbound.tag, inbound]));
    }
    return derivedCache.inboundByTag.get(tag) || null;
  }

  function secureUUID() {
    if (!window.crypto?.randomUUID) throw new APIError("此瀏覽器環境不支援 crypto.randomUUID，請手動輸入 UUID");
    return window.crypto.randomUUID();
  }

  function securePassword(length = 24) {
    if (!window.crypto?.getRandomValues) throw new APIError("此瀏覽器環境不支援安全亂數，請手動輸入密碼");
    const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#$%_-+=";
    const limit = 256 - (256 % alphabet.length);
    let result = "";
    while (result.length < length) {
      const bytes = new Uint8Array(Math.max(16, (length - result.length) * 2));
      window.crypto.getRandomValues(bytes);
      for (const byte of bytes) {
        if (byte >= limit) continue;
        result += alphabet[byte % alphabet.length];
        if (result.length === length) break;
      }
    }
    return result;
  }

  function securePasswordForInbound(inbound) {
    const byteLength = Number(inbound?.password_bytes) || 0;
    if (inbound?.password_encoding !== "base64" || byteLength <= 0) return securePassword();
    if (!window.crypto?.getRandomValues) throw new APIError("此瀏覽器環境不支援安全亂數，請手動輸入密碼");
    const bytes = new Uint8Array(byteLength);
    window.crypto.getRandomValues(bytes);
    let binary = "";
    bytes.forEach((value) => { binary += String.fromCharCode(value); });
    return window.btoa(binary);
  }

  function newMembershipDraft(inbound, user = null) {
    let uuid = user?.uuid || "";
    let password = user?.password || "";
    if (!user && (inbound.credential === "uuid" || inbound.credential === "uuid_password")) {
      try { uuid = secureUUID(); } catch (_) { uuid = ""; }
    }
    if (!user && (inbound.credential === "password" || inbound.credential === "uuid_password")) {
      try { password = securePasswordForInbound(inbound); } catch (_) { password = ""; }
    }
    return {
      id: user?.id || "",
      inbound: inbound.tag,
      uuid,
      password,
      flow: user?.flow || "",
      alter_id: Number(user?.alter_id) || 0,
      enabled: user ? Boolean(user.enabled) : true,
      quota_gb: toQuotaGB(user?.quota_bytes),
      max_ips: Number(user?.max_ips) || 0,
      expires_at: toDateTimeLocal(user?.expires_at),
      passwordVisible: false,
    };
  }

  function openUserDialog(group = null) {
    state.detailRequest += 1;
    const managed = managedInbounds();
    if (!group && !managed.length) {
      showToast("目前沒有支援動態用戶管理的入站", "error");
      return;
    }
    const memberships = group
      ? group.memberships.map((user) => newMembershipDraft(inboundFor(user.inbound), user))
      : [newMembershipDraft(inboundFor(state.filters.userInbound) || managed[0])];
    state.dialog = {
      type: "user",
      group,
      submitting: false,
      activeTab: "basics",
      activeMembership: 0,
      draft: {
        name: group?.name || "",
        policy_scope: group?.account?.policy_scope || "account_global",
        account_revision: Number(group?.account?.revision) || 0,
        account_enabled: group?.account ? Boolean(group.account.enabled) : true,
        account_quota_gb: toQuotaGB(group?.account?.quota_bytes),
        account_max_ips: Number(group?.account?.max_ips) || 0,
        account_expires_at: toDateTimeLocal(group?.account?.expires_at),
        account_reset_days: Number(group?.account?.reset_days) || 0,
        memberships,
      },
    };
    renderUserDialog();
    const dialog = document.getElementById("app-dialog");
    if (dialog && !dialog.open) dialog.showModal();
    window.setTimeout(() => document.getElementById("user-name")?.focus(), 0);
  }

  function renderUserDialog() {
    const dialog = document.getElementById("app-dialog");
    const model = state.dialog;
    if (!dialog || model?.type !== "user") return;
    const editing = Boolean(model.group);
    const draft = model.draft;
    const globalPolicy = draft.policy_scope === "account_global";
    const activeTab = model.activeTab || "basics";
    const activeMembership = Math.min(model.activeMembership || 0, Math.max(0, draft.memberships.length - 1));
    model.activeMembership = activeMembership;
    const selected = new Set(draft.memberships.map((membership) => membership.inbound));
    const available = managedInbounds().filter((inbound) => !selected.has(inbound.tag));
    dialog.className = "app-dialog user-dialog";
    dialog.setAttribute("aria-labelledby", "user-dialog-title");
    dialog.removeAttribute("aria-describedby");
    dialog.innerHTML = `
      <form class="dialog-form" data-form="user" novalidate>
        <header class="dialog-header">
          <div><h2 id="user-dialog-title">${editing ? "編輯邏輯用戶" : "新增邏輯用戶"}</h2><p>在同一處管理此名稱於所有節點的憑證與限制。</p></div>
          <button class="icon-button" type="button" data-action="dialog-cancel" aria-label="關閉對話框">${icon("close")}</button>
        </header>
        <nav class="user-dialog-tabs" role="tablist" aria-label="用戶設定區段">
          ${userDialogTab("basics", "基本資料", "users", activeTab)}
          ${userDialogTab("nodes", `節點與憑證 · ${formatInteger(draft.memberships.length)}`, "server", activeTab)}
          ${userDialogTab("links", "訂閱", "link", activeTab)}
        </nav>
        <fieldset class="dialog-fieldset" ${model.submitting ? "disabled" : ""}>
          <div class="dialog-body">
            <div class="form-error" id="form-error" role="alert" hidden></div>
            <section class="user-tab-panel" id="user-panel-basics" role="tabpanel" aria-labelledby="user-tab-basics" data-user-panel="basics" ${activeTab === "basics" ? "" : "hidden"}>
              <div class="form-grid">
                <div class="form-field full">
                  <label for="user-name">用戶名稱 <span class="required-mark" aria-hidden="true">*</span></label>
                  <input class="text-input" id="user-name" name="name" value="${escapeHTML(draft.name)}" maxlength="128" autocomplete="off" aria-describedby="name-error">
                  <span class="supporting-text field-message" id="name-error" data-help="用於識別流量、連線與訂閱，最多 128 個字元。">用於識別流量、連線與訂閱，最多 128 個字元。</span>
                </div>
                ${globalPolicy ? `
                  <div class="form-field"><label for="account-quota-gb">全域流量額度（GB）</label><input class="text-input" id="account-quota-gb" name="account_quota_gb" type="number" value="${escapeHTML(draft.account_quota_gb)}" min="0" step="0.01" inputmode="decimal" placeholder="0" aria-describedby="account_quota_gb-error"><span class="supporting-text field-message" id="account_quota_gb-error" data-help="跨所有節點合計；0 或留空表示不限額。">跨所有節點合計；0 或留空表示不限額。</span></div>
                  <div class="form-field"><label for="account-expires-at">帳戶到期時間</label><input class="text-input" id="account-expires-at" name="account_expires_at" type="datetime-local" value="${escapeHTML(draft.account_expires_at)}" aria-describedby="account_expires_at-error"><span class="supporting-text field-message" id="account_expires_at-error" data-help="留空表示永不到期。">留空表示永不到期。</span></div>
                  <div class="form-field"><label for="account-max-ips">全域來源 IP 上限</label><input class="text-input" id="account-max-ips" name="account_max_ips" type="number" value="${escapeHTML(draft.account_max_ips)}" min="0" max="65535" step="1" inputmode="numeric" aria-describedby="account_max_ips-error"><span class="supporting-text field-message" id="account_max_ips-error" data-help="跨所有節點合計；0 表示不限。">跨所有節點合計；0 表示不限。</span></div>
                  <div class="form-field"><label for="account-reset-days">自動續期天數</label><input class="text-input" id="account-reset-days" name="account_reset_days" type="number" value="${escapeHTML(draft.account_reset_days)}" min="0" max="24855" step="1" inputmode="numeric" aria-describedby="account_reset_days-error"><span class="supporting-text field-message" id="account_reset_days-error" data-help="到期後流量歸零並延長指定天數；0 表示不自動續期。">到期後流量歸零並延長指定天數；0 表示不自動續期。</span></div>
                  <div class="form-field full"><div class="switch-row"><span class="switch-copy"><strong>啟用帳戶</strong><span>停用後立即拒絕此帳戶在所有節點的新連線。</span></span><label class="switch-control"><span class="sr-only">啟用帳戶</span><input name="account_enabled" type="checkbox" role="switch" ${draft.account_enabled ? "checked" : ""}><span class="switch-track"></span></label></div></div>`
                  : '<div class="schema-note full">此帳戶由舊版資料遷移，限制仍分別套用於各節點。新帳戶會使用跨節點全域政策。</div>'}
                <div class="user-basics-summary full">
                  <span class="summary-symbol">${icon("server")}</span>
                  <div><strong>${formatInteger(draft.memberships.length)} 個節點</strong><p>${draft.memberships.map((membership) => escapeHTML(membership.inbound)).join(" · ")}</p></div>
                </div>
              </div>
            </section>
            <section class="user-tab-panel" id="user-panel-nodes" role="tabpanel" aria-labelledby="user-tab-nodes" data-user-panel="nodes" ${activeTab === "nodes" ? "" : "hidden"}>
              <div class="user-node-workspace">
                <aside class="user-node-rail" aria-label="已加入的節點">
                  <div class="user-node-listbox">${draft.memberships.map((membership, index) => renderUserNodeOption(membership, index, activeMembership)).join("")}</div>
                  ${available.length ? `<div class="add-membership"><select class="form-select" id="add-user-inbound" aria-label="選擇要新增的節點">${available.map((inbound) => `<option value="${escapeHTML(inbound.tag)}">${escapeHTML(inbound.tag)} · ${escapeHTML(inbound.type)}</option>`).join("")}</select><button class="button button-tonal" type="button" data-action="add-user-membership">${icon("plus")}<span>加入節點</span></button></div>` : `<p class="all-nodes-added">所有可管理節點均已加入</p>`}
                </aside>
                <div class="user-node-editor">${draft.memberships.map((membership, index) => renderUserMembership(membership, index, index === activeMembership)).join("")}</div>
              </div>
            </section>
            <section class="user-tab-panel" id="user-panel-links" role="tabpanel" aria-labelledby="user-tab-links" data-user-panel="links" ${activeTab === "links" ? "" : "hidden"}>
              ${editing && model.group.subscription_url ? `
                <div class="subscription-panel">
                  <span class="summary-symbol">${icon("link")}</span>
                  <div><h3>統一訂閱連結</h3><p>同名用戶在已套用節點上的可用連線會合併至此連結。</p></div>
                  <div class="form-field full">
                    <label for="user-subscription-url">訂閱 URL</label>
                    <div class="input-with-actions">
                      <input class="text-input mono" id="user-subscription-url" type="password" value="${escapeHTML(model.group.subscription_url)}" readonly spellcheck="false">
                      <div class="input-actions">
                        <button class="icon-button small" type="button" data-action="toggle-subscription-url" aria-label="顯示訂閱連結" title="顯示訂閱連結">${icon("eye")}</button>
                        <button class="icon-button small" type="button" data-action="copy-subscription-url" aria-label="複製訂閱連結" title="複製訂閱連結">${icon("copy")}</button>
                      </div>
                    </div>
                  </div>
                </div>` : `<div class="subscription-empty"><span class="summary-symbol">${icon("link")}</span><h3>建立後產生訂閱</h3><p>儲存用戶後，系統會產生整合所有可用節點的統一訂閱連結。</p></div>`}
            </section>
          </div>
        </fieldset>
        <footer class="dialog-actions">
          <button class="button" type="button" data-action="dialog-cancel" ${model.submitting ? "disabled" : ""}>取消</button>
          <button class="button button-primary" type="submit" data-dialog-submit ${model.submitting ? "disabled" : ""}>${model.submitting ? icon("refresh") : icon(editing ? "check" : "plus")}<span>${model.submitting ? "處理中…" : editing ? "儲存變更" : "建立用戶"}</span></button>
        </footer>
      </form>`;
  }

  function userDialogTab(tab, label, iconName, activeTab) {
    const active = tab === activeTab;
    return `<button class="user-dialog-tab" id="user-tab-${tab}" type="button" role="tab" data-action="select-user-tab" data-tab="${tab}" aria-controls="user-panel-${tab}" aria-selected="${active}" tabindex="${active ? "0" : "-1"}">${icon(iconName)}<span>${escapeHTML(label)}</span></button>`;
  }

  function renderUserNodeOption(draft, index, activeIndex) {
    const inbound = inboundFor(draft.inbound);
    if (!inbound) return "";
    return `<button class="user-node-option" type="button" data-action="select-user-membership" data-index="${index}" aria-pressed="${index === activeIndex}"><span class="status-dot ${draft.enabled ? "online" : "offline"}"></span><span><strong>${escapeHTML(inbound.tag)}</strong><small>${escapeHTML(inbound.type)} · ${escapeHTML(credentialLabel(inbound.credential))}</small></span>${icon("check")}</button>`;
  }

  function renderUserMembership(draft, index, active) {
    const inbound = inboundFor(draft.inbound);
    if (!inbound) return "";
    const suffix = String(index);
    const globalPolicy = state.dialog.draft.policy_scope === "account_global";
    return `<section class="membership-card" data-membership-index="${suffix}" ${active ? "" : "hidden"}>
      <div class="membership-heading"><div><strong>${escapeHTML(inbound.tag)}</strong><span>${escapeHTML(inbound.type)} · ${escapeHTML(credentialLabel(inbound.credential))}</span></div><button class="icon-button small" type="button" data-action="remove-user-membership" data-index="${suffix}" aria-label="移除 ${escapeHTML(inbound.tag)}" ${state.dialog.draft.memberships.length === 1 ? "disabled" : ""}>${icon("trash")}</button></div>
      <input type="hidden" name="id_${suffix}" value="${escapeHTML(draft.id)}"><input type="hidden" name="inbound_${suffix}" value="${escapeHTML(inbound.tag)}">
      <div class="form-grid membership-fields">
        ${credentialFields(inbound, draft, suffix)}
        ${inbound.flow ? `<div class="form-field full"><label for="flow_${suffix}">Flow</label><input class="text-input" id="flow_${suffix}" name="flow_${suffix}" value="${escapeHTML(draft.flow)}" autocomplete="off" spellcheck="false" aria-describedby="flow_${suffix}-error"><span class="supporting-text field-message" id="flow_${suffix}-error" data-help="留空表示不指定 Flow。">留空表示不指定 Flow。</span></div>` : ""}
        ${inbound.alter_id ? `<div class="form-field"><label for="alter_id_${suffix}">Alter ID</label><input class="text-input" id="alter_id_${suffix}" name="alter_id_${suffix}" type="number" value="${escapeHTML(draft.alter_id)}" min="0" max="2147483647" step="1" inputmode="numeric" aria-describedby="alter_id_${suffix}-error"><span class="supporting-text field-message" id="alter_id_${suffix}-error" data-help="必須是 0 或正整數。">必須是 0 或正整數。</span></div>` : ""}
        ${globalPolicy ? '<div class="schema-note full">流量額度、到期時間及來源 IP 上限由基本資料中的全域帳戶政策統一管理。</div>' : `
          ${inbound.traffic === false ? '<div class="schema-note">此協議不提供個別流量額度。</div>' : `<div class="form-field"><label for="quota_gb_${suffix}">流量額度（GB）</label><input class="text-input" id="quota_gb_${suffix}" name="quota_gb_${suffix}" type="number" value="${escapeHTML(draft.quota_gb)}" min="0" step="0.01" inputmode="decimal" placeholder="0" aria-describedby="quota_gb_${suffix}-error"><span class="supporting-text field-message" id="quota_gb_${suffix}-error" data-help="0 或留空表示不限額。">0 或留空表示不限額。</span></div>`}
          <div class="form-field"><label for="expires_at_${suffix}">到期時間</label><input class="text-input" id="expires_at_${suffix}" name="expires_at_${suffix}" type="datetime-local" value="${escapeHTML(draft.expires_at)}" aria-describedby="expires_at_${suffix}-error"><span class="supporting-text field-message" id="expires_at_${suffix}-error" data-help="留空表示永不到期。">留空表示永不到期。</span></div>
          <div class="form-field"><label for="max_ips_${suffix}">來源 IP 上限</label><input class="text-input" id="max_ips_${suffix}" name="max_ips_${suffix}" type="number" value="${escapeHTML(draft.max_ips)}" min="0" max="65535" step="1" inputmode="numeric" aria-describedby="max_ips_${suffix}-error"><span class="supporting-text field-message" id="max_ips_${suffix}-error" data-help="0 表示不限。">0 表示不限。</span></div>`}
        <div class="form-field full"><div class="switch-row"><span class="switch-copy"><strong>啟用此節點</strong><span>停用後拒絕此 membership 的新連線。</span></span><label class="switch-control"><span class="sr-only">啟用此節點</span><input name="enabled_${suffix}" type="checkbox" role="switch" ${draft.enabled ? "checked" : ""}><span class="switch-track"></span></label></div></div>
      </div>
    </section>`;
  }

  function credentialFields(inbound, draft, suffix) {
    const needsUUID = inbound.credential === "uuid" || inbound.credential === "uuid_password";
    const needsPassword = inbound.credential === "password" || inbound.credential === "uuid_password";
    return `
      ${needsUUID ? `
        <div class="form-field full">
          <label for="uuid_${suffix}">UUID <span class="required-mark" aria-hidden="true">*</span></label>
          <div class="input-with-actions">
            <input class="text-input mono" id="uuid_${suffix}" name="uuid_${suffix}" value="${escapeHTML(draft.uuid)}" autocomplete="off" spellcheck="false" aria-describedby="uuid_${suffix}-error">
            <div class="input-actions"><button class="icon-button small" type="button" data-action="generate-uuid" data-index="${suffix}" aria-label="產生安全 UUID" title="產生 UUID">${icon("wand")}</button></div>
          </div>
          <span class="supporting-text field-message" id="uuid_${suffix}-error" data-help="使用 crypto.randomUUID 產生或輸入有效 UUID。">使用 crypto.randomUUID 產生或輸入有效 UUID。</span>
        </div>` : ""}
      ${needsPassword ? `
        <div class="form-field full">
          <label for="password_${suffix}">密碼 <span class="required-mark" aria-hidden="true">*</span></label>
          <div class="input-with-actions">
            <input class="text-input mono" id="password_${suffix}" name="password_${suffix}" type="${draft.passwordVisible ? "text" : "password"}" value="${escapeHTML(draft.password)}" maxlength="1024" autocomplete="new-password" spellcheck="false" aria-describedby="password_${suffix}-error">
            <div class="input-actions">
              <button class="icon-button small" type="button" data-action="toggle-user-password" data-index="${suffix}" aria-label="${draft.passwordVisible ? "隱藏" : "顯示"}密碼" title="${draft.passwordVisible ? "隱藏" : "顯示"}密碼">${icon(draft.passwordVisible ? "eyeOff" : "eye")}</button>
              <button class="icon-button small" type="button" data-action="generate-password" data-index="${suffix}" aria-label="產生安全隨機密碼" title="產生密碼">${icon("wand")}</button>
            </div>
          </div>
          <span class="supporting-text field-message" id="password_${suffix}-error" data-help="${inbound.password_encoding === "base64" ? `此協議需要 ${escapeHTML(inbound.password_bytes)} bytes 標準 Base64 金鑰。` : "可使用安全亂數產生 24 字元密碼。"}">${inbound.password_encoding === "base64" ? `此協議需要 ${escapeHTML(inbound.password_bytes)} bytes 標準 Base64 金鑰。` : "可使用安全亂數產生 24 字元密碼。"}</span>
        </div>` : ""}`;
  }

  function prettyJSON(value) {
    try {
      return JSON.stringify(value, null, 2) || "";
    } catch (_) {
      return "";
    }
  }

  function openServerDialog(server = null) {
    state.detailRequest += 1;
    if (state.reloading || state.loading.protocols || state.loading.servers) {
      showToast("節點資料同步中，請稍後再試", "error");
      return;
    }
    if (server && (server.source !== "dashboard" || !server.editable || server.status === "pending_delete")) {
      showToast("此節點目前無法編輯", "error");
      return;
    }
    const protocol = server ? protocolForServer(server) : state.protocols[0];
    if (!server && !protocol) {
      showToast("協議目錄尚未載入，請先重新同步", "error");
      return;
    }
    const configText = prettyJSON(server ? server.config : protocol.template);
    if (!configText) {
      showToast("此節點沒有可編輯的原生設定", "error");
      return;
    }
    const advertise = server?.advertise || {};
    state.dialog = {
      type: "server",
      server,
      protocol,
      submitting: false,
      draft: {
        protocol: protocol ? protocolKey(protocol) : `${server.kind}:${server.type}`,
        server: advertise.server || "",
        server_port: advertise.server_port ? String(advertise.server_port) : "",
        tls_server_name: advertise.tls_server_name || "",
        insecure: Boolean(advertise.insecure),
        config: configText,
      },
    };
    renderServerDialog();
    const dialog = document.getElementById("app-dialog");
    if (dialog && !dialog.open) dialog.showModal();
    window.setTimeout(() => document.getElementById(server ? "server-config" : "server-protocol")?.focus(), 0);
  }

  function serverProtocolSummary(protocol, server) {
    const name = protocol?.name || server?.type || "未知協議";
    const type = protocol?.type || server?.type || "未知";
    const kind = protocol?.kind || server?.kind || "inbound";
    const credential = protocol?.credential || server?.credential;
    return `
      <div class="server-protocol-summary">
        <span class="server-protocol-icon">${icon(kind === "endpoint" ? "network" : "server", "icon-lg")}</span>
        <div>
          <strong>${escapeHTML(name)}</strong>
          <span class="mono">${escapeHTML(type)} · ${kind === "endpoint" ? "端點" : "入站"}</span>
          ${protocol?.description ? `<p>${escapeHTML(protocol.description)}</p>` : ""}
        </div>
        <div class="server-protocol-chips">
          ${protocol?.network ? `<span class="chip">${escapeHTML(networkLabel(protocol.network))}</span>` : ""}
          ${protocol?.tls ? `<span class="chip">${escapeHTML(tlsLabel(protocol.tls))}</span>` : ""}
          ${credential ? `<span class="chip primary">${escapeHTML(credentialLabel(credential))}</span>` : ""}
          ${protocol?.composite ? '<span class="chip warning">組合協議</span>' : ""}
        </div>
      </div>`;
  }

  function renderServerDialog() {
    const dialog = document.getElementById("app-dialog");
    const model = state.dialog;
    if (!dialog || model?.type !== "server") return;
    const editing = Boolean(model.server);
    const protocol = model.protocol;
    const draft = model.draft;
    const fixedTag = model.server?.tag || "";
    dialog.className = "app-dialog server-dialog";
    dialog.setAttribute("aria-labelledby", "server-dialog-title");
    dialog.setAttribute("aria-describedby", "server-dialog-description");
    dialog.innerHTML = `
      <form class="dialog-form" data-form="server" novalidate>
        <header class="dialog-header">
          <div><h2 id="server-dialog-title">${editing ? "編輯節點" : "新增節點"}</h2><p id="server-dialog-description">${editing ? "保留節點身分，更新公開資訊與原生 Server 設定。" : "從安全範本開始，再依部署環境調整完整原生設定。"}</p></div>
          <button class="icon-button" type="button" data-action="dialog-cancel" aria-label="關閉對話框">${icon("close")}</button>
        </header>
        <div class="server-dialog-scroll">
          <fieldset class="dialog-fieldset" ${model.submitting ? "disabled" : ""}>
            <div class="dialog-body">
            <div class="form-error" id="form-error" role="alert" hidden></div>
            <div class="form-grid server-form-grid">
              ${editing ? `
                <div class="form-field full">
                  <span class="field-label">固定節點身分</span>
                  ${serverProtocolSummary(protocol, model.server)}
                  <span class="supporting-text">協議、種類與 tag「${escapeHTML(fixedTag)}」建立後不可變更。</span>
                </div>` : `
                <div class="form-field full">
                  <label for="server-protocol">協議 <span class="required-mark" aria-hidden="true">*</span></label>
                  <select class="form-select" id="server-protocol" name="protocol" aria-describedby="protocol-error">
                    ${state.protocols.map((item) => `<option value="${escapeHTML(protocolKey(item))}" ${protocolKey(item) === draft.protocol ? "selected" : ""}>${escapeHTML(item.name)} · ${escapeHTML(item.type)} · ${escapeHTML(categoryLabel(item.category))}</option>`).join("")}
                  </select>
                  <span class="supporting-text field-message" id="protocol-error" data-help="切換協議會載入該協議最新的隨機安全範本。">切換協議會載入該協議最新的隨機安全範本。</span>
                </div>
                <div class="form-field full">${serverProtocolSummary(protocol, null)}</div>`}

              <div class="dialog-section-heading full"><div><h3>公開連線資訊</h3><p>用於產生或展示客戶端可連線的位址，不會覆寫原生監聽設定。</p></div></div>

              <div class="form-field">
                <label for="advertise-server">公開主機</label>
                <input class="text-input mono" id="advertise-server" name="server" value="${escapeHTML(draft.server)}" maxlength="253" autocomplete="off" spellcheck="false" placeholder="node.example.com" aria-describedby="server-error">
                <span class="supporting-text field-message" id="server-error" data-help="可填網域名稱或 IP；留空表示不指定。">可填網域名稱或 IP；留空表示不指定。</span>
              </div>

              <div class="form-field">
                <label for="advertise-port">公開連接埠</label>
                <input class="text-input" id="advertise-port" name="server_port" type="number" value="${escapeHTML(draft.server_port)}" min="1" max="65535" step="1" inputmode="numeric" placeholder="自動使用 listen_port" aria-describedby="server_port-error">
                <span class="supporting-text field-message" id="server_port-error" data-help="留空時，Core 會採用原生設定的 listen_port。">留空時，Core 會採用原生設定的 listen_port。</span>
              </div>

              <div class="form-field full">
                <label for="advertise-tls-name">TLS Server Name</label>
                <input class="text-input mono" id="advertise-tls-name" name="tls_server_name" value="${escapeHTML(draft.tls_server_name)}" maxlength="253" autocomplete="off" spellcheck="false" placeholder="node.example.com" aria-describedby="tls_server_name-error">
                <span class="supporting-text field-message" id="tls_server_name-error" data-help="選填；用於 TLS 憑證名稱驗證。">選填；用於 TLS 憑證名稱驗證。</span>
              </div>

              <div class="form-field full">
                <div class="switch-row">
                  <span class="switch-copy"><strong>允許不安全的 TLS</strong><span>略過 TLS 憑證驗證，僅應用於受控測試環境。</span></span>
                  <label class="switch-control">
                    <span class="sr-only">允許不安全的 TLS</span>
                    <input name="insecure" type="checkbox" role="switch" ${draft.insecure ? "checked" : ""}>
                    <span class="switch-track"></span>
                  </label>
                </div>
              </div>

              <div class="dialog-section-heading full native-config-heading"><div><h3>原生 Server JSON</h3><p>${editing ? model.server?.users_managed ? "type 與 tag 必須維持不變；users 已由用戶管理頁面接管並從此處隱藏。" : "type 與 tag 必須維持不變。" : "範本包含後端產生的隨機憑證，請在送出前完成必要調整。"}</p></div><span class="chip">完整設定</span></div>
              <div class="form-field full">
                <label class="sr-only" for="server-config">完整原生 Server JSON</label>
                <textarea class="text-input json-editor" id="server-config" name="config" rows="18" wrap="off" autocomplete="off" autocapitalize="off" spellcheck="false" aria-describedby="config-error">${escapeHTML(draft.config)}</textarea>
                <span class="supporting-text field-message" id="config-error" data-help="必須是 JSON 物件，且包含有效的 type、tag 與 listen_port。">必須是 JSON 物件，且包含有效的 type、tag 與 listen_port。</span>
              </div>
            </div>
            </div>
          </fieldset>
        </div>
        <footer class="dialog-actions">
          <button class="button" type="button" data-action="dialog-cancel" ${model.submitting ? "disabled" : ""}>取消</button>
          <button class="button button-primary" type="submit" data-dialog-submit ${model.submitting ? "disabled" : ""}>${model.submitting ? icon("refresh") : icon(editing ? "check" : "plus")}<span>${model.submitting ? "處理中…" : editing ? "儲存變更" : "建立節點"}</span></button>
        </footer>
      </form>`;
  }

  function captureServerDraft(form) {
    const value = (name) => form.elements.namedItem(name)?.value || "";
    return {
      protocol: value("protocol") || state.dialog?.draft?.protocol || "",
      server: value("server"),
      server_port: value("server_port"),
      tls_server_name: value("tls_server_name"),
      insecure: Boolean(form.elements.namedItem("insecure")?.checked),
      config: value("config"),
    };
  }

  function validateServerForm(form) {
    const model = state.dialog;
    const draft = captureServerDraft(form);
    model.draft = draft;
    const errors = {};
    const protocol = model.server ? model.protocol : findProtocol(draft.protocol);
    if (!model.server && !protocol) errors.protocol = "請選擇 Core 支援的協議。";

    let config = null;
    let parsed = false;
    const configErrors = [];
    try {
      config = JSON.parse(draft.config);
      parsed = true;
    } catch (error) {
      configErrors.push(`JSON 格式不正確：${error.message}`);
    }
    if (parsed && (config === null || typeof config !== "object" || Array.isArray(config))) {
      configErrors.push("設定必須是 JSON 物件。");
      config = null;
    }
    if (config) {
      const configType = typeof config.type === "string" ? config.type.trim() : "";
      const configTag = typeof config.tag === "string" ? config.tag.trim() : "";
      if (!configType) configErrors.push("type 為必填字串。");
      else if (config.type !== configType) configErrors.push("type 前後不可包含空白。");
      if (!configTag) configErrors.push("tag 為必填字串。");
      else if (config.tag !== configTag) configErrors.push("tag 前後不可包含空白。");
      else if (configTag.length > 128 || /[\u0000-\u001f/\\]/.test(config.tag)) configErrors.push("tag 必須是 1 至 128 字元，且不可包含斜線或控制字元。");
      if (!Number.isInteger(config.listen_port) || config.listen_port < 1 || config.listen_port > 65535) configErrors.push("listen_port 必須是 1 至 65535 的整數。");
      const expectedType = model.server?.type || protocol?.type;
      if (expectedType && configType && configType !== String(expectedType)) configErrors.push(`type 必須維持為 ${expectedType}。`);
      if (model.server && configTag && configTag !== model.server.tag) configErrors.push(`tag 必須維持為 ${model.server.tag}。`);
    }
    if (configErrors.length) errors.config = configErrors.join(" ");

    const server = draft.server.trim();
    const tlsServerName = draft.tls_server_name.trim();
    if (/[\u0000-\u0020/]/.test(server)) errors.server = "公開主機不可包含空白、斜線或控制字元。";
    if (/[\u0000-\u0020/]/.test(tlsServerName)) errors.tls_server_name = "TLS Server Name 不可包含空白、斜線或控制字元。";
    let serverPort = 0;
    if (draft.server_port !== "") {
      serverPort = Number(draft.server_port);
      if (!Number.isInteger(serverPort) || serverPort < 1 || serverPort > 65535) errors.server_port = "公開連接埠必須是 1 至 65535 的整數。";
    }

    return {
      errors,
      body: config && (protocol || model.server) ? {
        kind: model.server?.kind || protocol.kind,
        config,
        revision: Number(model.server?.revision) || 0,
        advertise: {
          server,
          server_port: serverPort,
          tls_server_name: tlsServerName,
          insecure: draft.insecure,
        },
      } : null,
    };
  }

  function setServerDialogBusy(busy) {
    if (state.dialog?.type !== "server") return;
    state.dialog.submitting = busy;
    const form = document.querySelector('[data-form="server"]');
    const fieldset = form?.querySelector("fieldset");
    const submit = form?.querySelector("[data-dialog-submit]");
    const cancelButtons = form?.querySelectorAll('[data-action="dialog-cancel"]') || [];
    if (fieldset) fieldset.disabled = busy;
    if (submit) {
      const editing = Boolean(state.dialog.server);
      submit.disabled = busy;
      submit.classList.toggle("is-loading", busy);
      submit.innerHTML = `${icon(busy ? "refresh" : editing ? "check" : "plus")}<span>${busy ? "處理中…" : editing ? "儲存變更" : "建立節點"}</span>`;
    }
    cancelButtons.forEach((button) => { button.disabled = busy; });
  }

  async function submitServer(form) {
    if (state.dialog?.type !== "server" || state.dialog.submitting) return;
    const { errors, body } = validateServerForm(form);
    applyFieldErrors(form, errors);
    const formError = document.getElementById("form-error");
    if (formError) formError.hidden = true;
    if (Object.keys(errors).length || !body) return;
    const model = state.dialog;
    const editing = Boolean(model.server);
    setServerDialogBusy(true);
    try {
      const path = editing ? `/servers/${encodeURIComponent(model.server.tag)}` : "/servers";
      await api(path, { method: editing ? "PUT" : "POST", body: JSON.stringify(body) });
      model.submitting = false;
      closeDialog();
      showToast(editing ? "節點變更已排入等待套用" : "節點已建立並等待套用", "success");
      await loadServerData({ silent: true, force: true });
    } catch (error) {
      if (error.status === 401) return;
      setServerDialogBusy(false);
      showFormError(error.message);
    }
  }

  function captureUserDraft(form) {
    const current = state.dialog.draft;
    const value = (name, fallback = "") => form.elements.namedItem(name)?.value ?? fallback;
    const memberships = state.dialog.draft.memberships.map((membership, index) => ({
      id: membership.id,
      inbound: membership.inbound,
      uuid: value(`uuid_${index}`, membership.uuid),
      password: value(`password_${index}`, membership.password),
      flow: value(`flow_${index}`, membership.flow),
      alter_id: value(`alter_id_${index}`, membership.alter_id),
      enabled: form.elements.namedItem(`enabled_${index}`)?.checked ?? membership.enabled,
      quota_gb: value(`quota_gb_${index}`, membership.quota_gb),
      max_ips: value(`max_ips_${index}`, membership.max_ips),
      expires_at: value(`expires_at_${index}`, membership.expires_at),
      passwordVisible: membership.passwordVisible,
    }));
    return {
      name: value("name", current.name),
      policy_scope: current.policy_scope,
      account_revision: current.account_revision,
      account_enabled: form.elements.namedItem("account_enabled")?.checked ?? current.account_enabled,
      account_quota_gb: value("account_quota_gb", current.account_quota_gb),
      account_max_ips: value("account_max_ips", current.account_max_ips),
      account_expires_at: value("account_expires_at", current.account_expires_at),
      account_reset_days: value("account_reset_days", current.account_reset_days),
      memberships,
    };
  }

  function validateUserForm(form) {
    const draft = captureUserDraft(form);
    state.dialog.draft = draft;
    const errors = {};
    const name = draft.name.trim();
    if (!name) errors.name = "請輸入用戶名稱。";
    else if (name.length > 128) errors.name = "用戶名稱不可超過 128 個字元。";
    const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
    const globalPolicy = draft.policy_scope === "account_global";

    let accountQuotaBytes = 0;
    let accountExpiresAt = 0;
    let accountMaxIPs = 0;
    let accountResetDays = 0;
    if (globalPolicy) {
      if (draft.account_quota_gb !== "") {
        const quotaGB = Number(draft.account_quota_gb);
        if (!Number.isFinite(quotaGB) || quotaGB < 0) errors.account_quota_gb = "額度必須是 0 或正數。";
        else {
          accountQuotaBytes = Math.round(quotaGB * GIB);
          if (!Number.isSafeInteger(accountQuotaBytes)) errors.account_quota_gb = "額度過大，請輸入較小的數值。";
        }
      }
      if (draft.account_expires_at) {
        accountExpiresAt = new Date(draft.account_expires_at).getTime();
        if (!Number.isFinite(accountExpiresAt) || accountExpiresAt < 0) errors.account_expires_at = "到期時間格式不正確。";
      }
      accountMaxIPs = Number(draft.account_max_ips || 0);
      if (!Number.isSafeInteger(accountMaxIPs) || accountMaxIPs < 0 || accountMaxIPs > 65535) errors.account_max_ips = "來源 IP 上限必須是 0 至 65,535 的整數。";
      accountResetDays = Number(draft.account_reset_days || 0);
      if (!Number.isSafeInteger(accountResetDays) || accountResetDays < 0 || accountResetDays > 24855) errors.account_reset_days = "自動續期天數必須是 0 至 24,855 的整數。";
      if (accountResetDays > 0 && accountExpiresAt <= 0) errors.account_reset_days = "設定自動續期前必須先設定帳戶到期時間。";
    }

    const memberships = draft.memberships.map((membership, index) => {
      const inbound = inboundFor(membership.inbound);
      const needsUUID = inbound.credential === "uuid" || inbound.credential === "uuid_password";
      const needsPassword = inbound.credential === "password" || inbound.credential === "uuid_password";
      if (needsUUID && !uuidPattern.test(membership.uuid.trim())) errors[`uuid_${index}`] = "請輸入有效的 UUID 格式。";
      if (needsPassword && !membership.password) errors[`password_${index}`] = "密碼不能留空。";
      else if (needsPassword && membership.password.length > 1024) errors[`password_${index}`] = "密碼不可超過 1024 個字元。";
      else if (needsPassword && inbound.password_encoding === "base64" && Number(inbound.password_bytes) > 0) {
        try {
          const decoded = window.atob(membership.password);
          if (decoded.length !== Number(inbound.password_bytes)) errors[`password_${index}`] = `密碼必須是 ${inbound.password_bytes} bytes 的標準 Base64。`;
        } catch (_) {
          errors[`password_${index}`] = `密碼必須是 ${inbound.password_bytes} bytes 的標準 Base64。`;
        }
      }
      let quotaBytes = 0;
      if (!globalPolicy && inbound.traffic !== false && membership.quota_gb !== "") {
        const quotaGB = Number(membership.quota_gb);
        if (!Number.isFinite(quotaGB) || quotaGB < 0) errors[`quota_gb_${index}`] = "額度必須是 0 或正數。";
        else {
          quotaBytes = Math.round(quotaGB * GIB);
          if (!Number.isSafeInteger(quotaBytes)) errors[`quota_gb_${index}`] = "額度過大，請輸入較小的數值。";
        }
      }
      let expiresAt = 0;
      if (!globalPolicy && membership.expires_at) {
        expiresAt = new Date(membership.expires_at).getTime();
        if (!Number.isFinite(expiresAt) || expiresAt < 0) errors[`expires_at_${index}`] = "到期時間格式不正確。";
      }
      let alterID = 0;
      if (inbound.alter_id) {
        alterID = Number(membership.alter_id || 0);
        if (!Number.isSafeInteger(alterID) || alterID < 0 || alterID > 2147483647) errors[`alter_id_${index}`] = "Alter ID 必須是 0 至 2,147,483,647 的整數。";
      }
      const maxIPs = globalPolicy ? 0 : Number(membership.max_ips || 0);
      if (!globalPolicy && (!Number.isSafeInteger(maxIPs) || maxIPs < 0 || maxIPs > 65535)) errors[`max_ips_${index}`] = "來源 IP 上限必須是 0 至 65,535 的整數。";
      return {
        id: membership.id,
        inbound: inbound.tag,
        uuid: needsUUID ? membership.uuid.trim() : "",
        password: needsPassword ? membership.password : "",
        flow: inbound.flow ? membership.flow.trim() : "",
        alter_id: alterID,
        enabled: membership.enabled,
        quota_bytes: quotaBytes,
        expires_at: expiresAt,
        max_ips: maxIPs,
      };
    });

    const affected = new Set([...(state.dialog.group?.memberships || []).map((membership) => membership.inbound), ...memberships.map((membership) => membership.inbound)]);
    const revisions = {};
    affected.forEach((tag) => {
      revisions[tag] = Number(state.dialog.group?.revisions?.[tag] || inboundFor(tag)?.revision) || 0;
    });

    return {
      errors,
      body: {
        name,
        policy_scope: draft.policy_scope,
        account_revision: Number(draft.account_revision) || 0,
        enabled: draft.account_enabled,
        quota_bytes: accountQuotaBytes,
        expires_at: accountExpiresAt,
        max_ips: accountMaxIPs,
        reset_days: accountResetDays,
        memberships,
        revisions,
      },
    };
  }

  function applyFieldErrors(form, errors) {
    form.querySelectorAll(".field-message").forEach((message) => {
      message.textContent = message.dataset.help || "";
      message.classList.remove("field-error");
    });
    form.querySelectorAll("[aria-invalid]").forEach((field) => field.removeAttribute("aria-invalid"));
    let first = null;
    Object.entries(errors).forEach(([name, message]) => {
      const field = form.elements.namedItem(name);
      const error = document.getElementById(`${name}-error`);
      if (field) {
        field.setAttribute("aria-invalid", "true");
        first ||= field;
      }
      if (error) {
        error.textContent = message;
        error.classList.add("field-error");
      }
    });
    first?.focus();
  }

  function showFormError(message) {
    const error = document.getElementById("form-error");
    if (!error) return;
    error.hidden = false;
    error.innerHTML = `${icon("alert")}<span>${escapeHTML(message)}</span>`;
    error.scrollIntoView({ block: "nearest" });
  }

  function setUserDialogBusy(busy) {
    if (state.dialog?.type !== "user") return;
    state.dialog.submitting = busy;
    const form = document.querySelector('[data-form="user"]');
    const fieldset = form?.querySelector("fieldset");
    const submit = form?.querySelector("[data-dialog-submit]");
    const cancelButtons = form?.querySelectorAll('[data-action="dialog-cancel"]') || [];
    if (fieldset) fieldset.disabled = busy;
    if (submit) {
      submit.disabled = busy;
      submit.classList.toggle("is-loading", busy);
      submit.innerHTML = `${icon(busy ? "refresh" : state.dialog.group ? "check" : "plus")}<span>${busy ? "處理中…" : state.dialog.group ? "儲存變更" : "建立用戶"}</span>`;
    }
    cancelButtons.forEach((button) => { button.disabled = busy; });
  }

  function openConfirm({ title, message, confirmLabel, action, successMessage, danger = true }) {
    state.detailRequest += 1;
    const dialog = document.getElementById("app-dialog");
    if (!dialog) return;
    state.dialog = { type: "confirm", action, successMessage, submitting: false };
    dialog.className = "app-dialog confirm-dialog";
    dialog.setAttribute("aria-labelledby", "confirm-title");
    dialog.setAttribute("aria-describedby", "confirm-message");
    dialog.innerHTML = `
      <form class="dialog-form" data-form="confirm">
        <header class="dialog-header">
          <div><span class="confirm-symbol">${icon(danger ? "alert" : "reset", "icon-lg")}</span><h2 id="confirm-title">${escapeHTML(title)}</h2></div>
          <button class="icon-button" type="button" data-action="dialog-cancel" aria-label="關閉對話框">${icon("close")}</button>
        </header>
        <div class="dialog-body">
          <p class="confirm-message" id="confirm-message">${escapeHTML(message)}</p>
          <div class="form-error" id="form-error" role="alert" hidden></div>
        </div>
        <footer class="dialog-actions">
          <button class="button" type="button" data-action="dialog-cancel">取消</button>
          <button class="button ${danger ? "button-danger" : "button-primary"}" type="submit" data-dialog-submit>${icon(danger ? "trash" : "check")}<span>${escapeHTML(confirmLabel)}</span></button>
        </footer>
      </form>`;
    if (!dialog.open) dialog.showModal();
    window.setTimeout(() => dialog.querySelector("[data-dialog-submit]")?.focus(), 0);
  }

  function closeDialog() {
    state.detailRequest += 1;
    const dialog = document.getElementById("app-dialog");
    if (dialog?.open && !state.dialog?.submitting) dialog.close();
    else if (!dialog?.open) state.dialog = null;
  }

  function acceptOverview(data) {
    const now = Date.now();
    const upTotal = asNumber(data?.traffic?.uplink_total);
    const downTotal = asNumber(data?.traffic?.downlink_total);
    if (state.overviewSample) {
      const elapsed = (now - state.overviewSample.time) / 1000;
      if (elapsed >= 0.5) {
        const up = Math.max(0, upTotal - state.overviewSample.up) / elapsed;
        const down = Math.max(0, downTotal - state.overviewSample.down) / elapsed;
        state.speed = { up, down };
        state.trafficSamples.push({ time: now, up, down });
        state.trafficSamples = state.trafficSamples.slice(-24);
      }
    }
    state.overviewSample = { time: now, up: upTotal, down: downTotal };
    state.overview = data;
    if (Array.isArray(data?.inbounds)) state.inbounds = data.inbounds;
    state.lastUpdated = now;
    state.errors.overview = "";
  }

  async function loadOverview({ silent = false, force = false } = {}) {
    if (!state.authenticated) return;
    if (state.loading.overview) {
      if (!force) return;
      const epoch = state.epoch;
      while (state.loading.overview && epoch === state.epoch) await new Promise((resolve) => window.setTimeout(resolve, 25));
      if (epoch !== state.epoch || !state.authenticated) return;
    }
    const epoch = state.epoch;
    const preserveScroll = state.route === "overview" && Boolean(state.overview);
    state.loading.overview = true;
    if (state.route === "overview" && !state.overview) renderOverview();
    try {
      const data = await api("/overview");
      if (epoch !== state.epoch) return;
      acceptOverview(data || {});
      updateShell();
    } catch (error) {
      if (epoch !== state.epoch || error.status === 401) return;
      state.errors.overview = error.message;
      if (!silent && state.overview) showToast(error.message, "error");
    } finally {
      if (epoch === state.epoch) {
        state.loading.overview = false;
        if (state.route === "overview") renderOverview({ animate: false, preserveScroll });
      }
    }
  }

  async function loadProtocols({ silent = false, force = false } = {}) {
    if (!state.authenticated) return;
    if (state.loading.protocols) {
      if (!force) return;
      const epoch = state.epoch;
      while (state.loading.protocols && epoch === state.epoch) await new Promise((resolve) => window.setTimeout(resolve, 25));
      if (epoch !== state.epoch || !state.authenticated) return;
    }
    const epoch = state.epoch;
    state.loading.protocols = true;
    if (!state.loaded.protocols && state.route === "servers") renderServers();
    try {
      const data = await api("/protocols");
      if (epoch !== state.epoch) return;
      state.protocolSchemaVersion = data?.schema_version ?? null;
      state.protocols = Array.isArray(data?.protocols) ? data.protocols : [];
      state.loaded.protocols = true;
      state.errors.protocols = "";
    } catch (error) {
      if (epoch !== state.epoch || error.status === 401) return;
      state.errors.protocols = error.message;
      if (!silent && state.loaded.protocols) showToast(error.message, "error");
    } finally {
      if (epoch === state.epoch) {
        state.loading.protocols = false;
        if (state.route === "servers") renderServers();
      }
    }
  }

  function acceptServers(data) {
    state.servers = Array.isArray(data?.servers) ? data.servers : [];
    state.restartRequired = Boolean(data?.restart_required);
    state.loaded.servers = true;
    state.errors.servers = "";
    state.lastUpdated = Date.now();
  }

  async function loadServers({ silent = false, force = false } = {}) {
    if (!state.authenticated) return;
    if (state.loading.servers) {
      if (!force) return;
      const epoch = state.epoch;
      while (state.loading.servers && epoch === state.epoch) await new Promise((resolve) => window.setTimeout(resolve, 25));
      if (epoch !== state.epoch || !state.authenticated) return;
    }
    const epoch = state.epoch;
    state.loading.servers = true;
    if (!state.loaded.servers && state.route === "servers") renderServers();
    try {
      const data = await api("/servers");
      if (epoch !== state.epoch) return;
      acceptServers(data || {});
      updateShell();
    } catch (error) {
      if (epoch !== state.epoch || error.status === 401) return;
      state.errors.servers = error.message;
      if (!silent && state.loaded.servers) showToast(error.message, "error");
    } finally {
      if (epoch === state.epoch) {
        state.loading.servers = false;
        if (state.route === "servers") renderServers();
      }
    }
  }

  async function loadServerData(options = {}) {
    const requests = [loadProtocols(options), loadServers(options)];
    if (state.route === "servers") renderServers();
    await Promise.all(requests);
  }

  function wait(milliseconds) {
    return new Promise((resolve) => window.setTimeout(resolve, milliseconds));
  }

  async function reloadCore() {
    if (!state.authenticated || state.reloading) return;
    const epoch = state.epoch;
    const dialog = document.getElementById("app-dialog");
    if (dialog?.open) {
      if (state.dialog) state.dialog.submitting = false;
      dialog.close();
    }
    state.reloading = true;
    stopPolling();
    if (state.route === "servers") renderServers();
    const deadline = Date.now() + 30000;

    try {
      let response = null;
      try {
        response = await api("/reload", { method: "POST", timeoutMs: 5000 });
      } catch (error) {
        if (!error.offline) throw error;
      }
      if (epoch !== state.epoch || !state.authenticated) return;

      if (response?.reloading === false) {
        await Promise.all([loadOverview({ silent: true, force: true }), loadServerData({ silent: true, force: true })]);
        showToast(response.message || "目前沒有待套用的節點變更", "success");
        return;
      }

      let lastError = null;
      await wait(700);
      while (Date.now() < deadline && epoch === state.epoch && state.authenticated) {
        try {
          const timeoutMs = Math.max(500, Math.min(4000, deadline - Date.now()));
          const [overview, servers] = await Promise.all([
            api("/overview", { timeoutMs }),
            api("/servers", { timeoutMs }),
          ]);
          if (servers?.restart_required) {
            lastError = new APIError("Core 尚未套用節點變更");
          } else {
            acceptOverview(overview || {});
            acceptServers(servers || {});
            updateShell();
            loadProtocols({ silent: true, force: true });
            showToast("節點變更已套用，Core 已恢復連線", "success");
            return;
          }
        } catch (error) {
          if (error.status === 401 || epoch !== state.epoch) return;
          lastError = error;
        }
        await wait(Math.min(1200, Math.max(0, deadline - Date.now())));
      }
      throw new APIError(lastError?.offline ? "Core 未在 30 秒內恢復連線，請稍後手動刷新" : "Core 未在 30 秒內完成節點變更，請檢查服務狀態");
    } catch (error) {
      if (error.status !== 401 && epoch === state.epoch) showToast(error.message, "error");
    } finally {
      if (epoch === state.epoch) {
        state.reloading = false;
        if (state.authenticated) startPolling();
        updateShell();
        if (state.route === "servers") renderServers();
      }
    }
  }

  async function loadUsers({ silent = false, force = false } = {}) {
    if (!state.authenticated) return;
    if (state.loading.users) {
      if (!force) return;
      const epoch = state.epoch;
      while (state.loading.users && epoch === state.epoch) await new Promise((resolve) => window.setTimeout(resolve, 25));
      if (epoch !== state.epoch || !state.authenticated) return;
    }
    const epoch = state.epoch;
    state.loading.users = true;
    if (!state.loaded.users && state.route === "users") renderUsers();
    try {
      const data = await api("/users");
      if (epoch !== state.epoch) return;
      state.users = Array.isArray(data?.users) ? data.users : [];
      state.accounts = Array.isArray(data?.accounts) ? data.accounts : [];
      if (Array.isArray(data?.inbounds)) state.inbounds = data.inbounds;
      state.loaded.users = true;
      state.errors.users = "";
    } catch (error) {
      if (epoch !== state.epoch || error.status === 401) return;
      state.errors.users = error.message;
      if (!silent && state.loaded.users) showToast(error.message, "error");
    } finally {
      if (epoch === state.epoch) {
        state.loading.users = false;
        if (state.route === "users") renderUsers();
      }
    }
  }

  async function loadConnections({ silent = false, force = false } = {}) {
    if (!state.authenticated) return;
    if (state.loading.connections) {
      if (!force) return;
      const epoch = state.epoch;
      while (state.loading.connections && epoch === state.epoch) await new Promise((resolve) => window.setTimeout(resolve, 25));
      if (epoch !== state.epoch || !state.authenticated) return;
    }
    const epoch = state.epoch;
    state.loading.connections = true;
    if (!state.loaded.connections && state.route === "connections") renderConnections();
    try {
      const data = await api("/connections");
      if (epoch !== state.epoch) return;
      state.connections = Array.isArray(data?.connections) ? data.connections : [];
      state.loaded.connections = true;
      state.errors.connections = "";
    } catch (error) {
      if (epoch !== state.epoch || error.status === 401) return;
      state.errors.connections = error.message;
      if (!silent && state.loaded.connections) showToast(error.message, "error");
    } finally {
      if (epoch === state.epoch) {
        state.loading.connections = false;
        if (state.route === "connections") {
          if (state.loaded.connections && document.getElementById("connection-results")) renderConnectionResults();
          else renderConnections();
        }
      }
    }
  }

  async function loadSettings({ silent = false, force = false } = {}) {
    if (!state.authenticated) return;
    if (state.loading.settings) {
      if (!force) return;
      const epoch = state.epoch;
      while (state.loading.settings && epoch === state.epoch) await new Promise((resolve) => window.setTimeout(resolve, 25));
      if (epoch !== state.epoch || !state.authenticated) return;
    }
    const epoch = state.epoch;
    state.loading.settings = true;
    if (!state.loaded.settings && state.route === "settings") renderSettings();
    try {
      const data = await api("/settings");
      if (epoch !== state.epoch) return;
      state.settings = data || {};
      state.loaded.settings = true;
      state.errors.settings = "";
      state.lastUpdated = Date.now();
      updateShell();
    } catch (error) {
      if (epoch !== state.epoch || error.status === 401) return;
      state.errors.settings = error.message;
      if (!silent && state.loaded.settings) showToast(error.message, "error");
    } finally {
      if (epoch === state.epoch) {
        state.loading.settings = false;
        if (state.route === "settings") renderSettings();
      }
    }
  }

  function setSettingsBusy(busy) {
    state.savingSettings = busy;
    const form = document.querySelector('[data-form="settings"]');
    const fieldset = form?.querySelector("fieldset");
    const buttons = form?.querySelectorAll("button") || [];
    const submit = form?.querySelector("[data-settings-submit]");
    if (fieldset) fieldset.disabled = busy;
    buttons.forEach((button) => { button.disabled = busy; });
    if (submit) {
      submit.classList.toggle("is-loading", busy);
      submit.innerHTML = `${icon(busy ? "refresh" : "check")}<span>${busy ? "儲存中…" : "儲存設定"}</span>`;
    }
    document.getElementById("main-content")?.setAttribute("aria-busy", busy ? "true" : "false");
  }

  async function submitSettings(form) {
    if (state.savingSettings) return;
    const { errors, body } = validateSettingsForm(form);
    applyFieldErrors(form, errors);
    const formError = document.getElementById("settings-form-error");
    if (formError) formError.hidden = true;
    if (Object.keys(errors).length) return;
    setSettingsBusy(true);
    try {
      const data = await api("/settings", { method: "PUT", body: JSON.stringify(body) });
      state.settings = data || body;
      state.loaded.settings = true;
      state.errors.settings = "";
      state.lastUpdated = Date.now();
      state.savingSettings = false;
      updateShell();
      if (state.route === "settings") renderSettings();
      showToast("訂閱安全設定已立即生效", "success");
    } catch (error) {
      if (error.status === 401) return;
      setSettingsBusy(false);
      const currentError = document.getElementById("settings-form-error");
      if (currentError) {
        currentError.hidden = false;
        currentError.innerHTML = `${icon("alert")}<span>${escapeHTML(error.message)}</span>`;
        currentError.scrollIntoView({ block: "nearest" });
      }
    }
  }

  function validate3XUIImportMapping(value) {
    let mapping;
    try {
      mapping = JSON.parse(value || "{}");
    } catch (_) {
      return "Inbound mapping 必須是有效的 JSON 物件。";
    }
    if (!mapping || Array.isArray(mapping) || typeof mapping !== "object") return "Inbound mapping 必須是 JSON 物件。";
    for (const [source, target] of Object.entries(mapping)) {
      if (!/^[1-9]\d*$/.test(source) || typeof target !== "string" || !target || target !== target.trim()) {
        return "每個 mapping key 必須是正整數 inbound ID，value 必須是無前後空白的 Sidera tag。";
      }
    }
    return "";
  }

  function invalidate3XUIImportReport() {
    if (!state.import3XUI.report) return;
    if (state.import3XUI.reportStale) return;
    state.import3XUI.reportStale = true;
    const report = document.getElementById("import-report");
    if (report) {
      report.classList.add("stale");
      report.insertAdjacentHTML("afterbegin", `<div class="import-stale-notice">${icon("alert")}<span>資料庫或 mapping 已變更；此報告僅供參考，請重新執行預檢。</span></div>`);
    }
    const apply = document.querySelector('[data-action="apply-3x-ui-import"]');
    if (apply) apply.disabled = true;
  }

  async function submit3XUIImport(form, applying = false) {
    if (state.import3XUI.busy) return;
    const selectedFile = form.elements.database.files?.[0] || state.import3XUI.file;
    const mapping = form.elements.inbound_map.value.trim() || "{}";
    state.import3XUI.file = selectedFile || null;
    state.import3XUI.mapping = mapping;
    state.import3XUI.error = !selectedFile ? "請選擇 3x-ui SQLite 備份。" : validate3XUIImportMapping(mapping);
    if (applying && (state.import3XUI.reportStale || !state.import3XUI.report?.ready || !state.import3XUI.report.fingerprint)) {
      state.import3XUI.error = "資料庫或 mapping 已變更，請重新執行預檢。";
    }
    if (state.import3XUI.error) {
      if (state.route === "settings") renderSettings();
      return;
    }
    if (applying && !window.confirm(`即將建立 ${formatInteger(state.import3XUI.report.summary?.creatable_accounts)} 個 account-global 帳戶。確定要正式匯入？`)) return;

    const body = new FormData();
    body.append("database", selectedFile, selectedFile.name || "x-ui.db");
    body.append("inbound_map", mapping);
    if (applying) body.append("fingerprint", state.import3XUI.report.fingerprint);
    state.import3XUI.busy = true;
    state.import3XUI.applying = applying;
    if (state.route === "settings") renderSettings();
    try {
      const report = await api(`/imports/3x-ui/${applying ? "apply" : "dry-run"}`, { method: "POST", body, timeoutMs: 90000 });
      if (applying) {
        const imported = report.summary?.creatable_accounts || report.source?.accounts || 0;
        state.import3XUI = { busy: false, applying: false, file: null, mapping: "{}", report: null, reportStale: false, error: "" };
        showToast(`已正式匯入 ${formatInteger(imported)} 個 account-global 帳戶`, "success");
        await Promise.all([loadUsers({ force: true }), loadOverview({ silent: true, force: true })]);
      } else {
        state.import3XUI.report = report;
        state.import3XUI.reportStale = false;
        showToast(report.ready ? "預檢通過，可以正式匯入" : "預檢完成，請先處理阻擋項目", report.ready ? "success" : "info");
      }
    } catch (error) {
      if (error.status === 401) return;
      if (error.status === 409 && error.payload?.fingerprint) {
        state.import3XUI.report = error.payload;
        state.import3XUI.reportStale = false;
      }
      state.import3XUI.error = error.status === 409 && error.payload?.fingerprint ? "匯入狀態已變更，請依照最新報告處理後重新預檢。" : error.message;
    } finally {
      state.import3XUI.busy = false;
      state.import3XUI.applying = false;
      if (state.authenticated && state.route === "settings") renderSettings();
    }
  }

  function startPolling() {
    stopPolling();
    state.pollTimer = window.setInterval(() => {
      if (document.hidden || !state.authenticated) return;
      loadOverview({ silent: true });
      if (state.route === "connections") loadConnections({ silent: true });
    }, POLL_INTERVAL);
  }

  function stopPolling() {
    if (state.pollTimer) window.clearInterval(state.pollTimer);
    state.pollTimer = 0;
  }

  async function activateRoute() {
    state.route = routeFromHash();
    closeDialog();
    renderCurrentRoute();
    document.getElementById("main-content")?.focus({ preventScroll: true });
    if (state.route === "overview" && !state.overview) await loadOverview();
    if (state.route === "servers") await loadServerData();
    if (state.route === "users" && !state.loaded.users) await loadUsers();
    if (state.route === "connections" && !state.loaded.connections) await loadConnections();
    if (state.route === "settings" && !state.loaded.settings) await loadSettings();
  }

  async function retryCurrent() {
    await loadOverview();
    if (state.route === "servers") await loadServerData();
    if (state.route === "users") await loadUsers();
    if (state.route === "connections") await loadConnections();
    if (state.route === "settings") await loadSettings();
  }

  function resetData() {
    state.overview = null;
    state.overviewSample = null;
    state.trafficSamples = [];
    state.speed = null;
    state.users = [];
    state.accounts = [];
    state.connections = [];
    state.inbounds = [];
    state.protocolSchemaVersion = null;
    state.protocols = [];
    state.servers = [];
    state.settings = null;
    state.import3XUI = { busy: false, applying: false, file: null, mapping: "{}", report: null, reportStale: false, error: "" };
    state.restartRequired = false;
    state.reloading = false;
    state.savingSettings = false;
    state.loaded.protocols = false;
    state.loaded.servers = false;
    state.loaded.users = false;
    state.loaded.connections = false;
    state.loaded.settings = false;
    state.errors.overview = "";
    state.errors.protocols = "";
    state.errors.servers = "";
    state.errors.users = "";
    state.errors.connections = "";
    state.errors.settings = "";
    state.loading.overview = false;
    state.loading.protocols = false;
    state.loading.servers = false;
    state.loading.users = false;
    state.loading.connections = false;
    state.loading.settings = false;
  }

  async function bootstrap() {
    renderBoot();
    const epoch = ++state.epoch;
    try {
      const data = await api("/overview");
      if (epoch !== state.epoch) return;
      acceptOverview(data || {});
      state.authenticated = true;
      renderShell();
      startPolling();
      loadServerData({ silent: true });
      if (state.route === "users") loadUsers();
      if (state.route === "connections") loadConnections();
      if (state.route === "settings") loadSettings();
    } catch (error) {
      if (epoch !== state.epoch || error.status === 401) return;
      state.authenticated = true;
      state.errors.overview = error.message;
      renderShell();
      startPolling();
      loadServerData({ silent: true });
      if (state.route === "users") loadUsers();
      if (state.route === "connections") loadConnections();
      if (state.route === "settings") loadSettings();
    }
  }

  async function submitLogin(form) {
    const token = form.elements.token.value;
    const button = form.querySelector('button[type="submit"]');
    const input = form.elements.token;
    const message = document.getElementById("login-message");
    button.disabled = true;
    input.disabled = true;
    button.classList.add("is-loading");
    button.innerHTML = `${icon("refresh")}<span>驗證中…</span>`;
    if (message) message.hidden = true;
    const epoch = ++state.epoch;
    try {
      setToken(token);
      const data = await api("/overview");
      if (epoch !== state.epoch) return;
      resetData();
      acceptOverview(data || {});
      state.authenticated = true;
      state.route = routeFromHash();
      renderShell();
      startPolling();
      loadServerData({ silent: true });
      if (state.route === "users") loadUsers();
      if (state.route === "connections") loadConnections();
      if (state.route === "settings") loadSettings();
    } catch (error) {
      if (epoch !== state.epoch || error.status === 401) return;
      const currentMessage = document.getElementById("login-message");
      if (currentMessage) {
        currentMessage.className = "form-error";
        currentMessage.hidden = false;
        currentMessage.innerHTML = `${icon("alert")}<span>${escapeHTML(error.message)}</span>`;
      }
      const currentButton = form.querySelector('button[type="submit"]');
      if (currentButton) {
        currentButton.disabled = false;
        currentButton.classList.remove("is-loading");
        currentButton.innerHTML = `${icon("key")}<span>連線至 Core</span>`;
      }
      input.disabled = false;
      input.focus();
    }
  }

  async function submitUser(form) {
    const { errors, body } = validateUserForm(form);
    const firstError = Object.keys(errors)[0] || "";
    if (firstError) {
      const membershipMatch = firstError.match(/_(\d+)$/);
      const nextTab = firstError === "name" || firstError.startsWith("account_") ? "basics" : membershipMatch ? "nodes" : state.dialog.activeTab;
      const nextMembership = membershipMatch ? Number(membershipMatch[1]) : state.dialog.activeMembership;
      if (nextTab !== state.dialog.activeTab || nextMembership !== state.dialog.activeMembership) {
        state.dialog.activeTab = nextTab;
        state.dialog.activeMembership = nextMembership;
        renderUserDialog();
        form = document.querySelector('[data-form="user"]');
      }
    }
    applyFieldErrors(form, errors);
    const formError = document.getElementById("form-error");
    if (formError) formError.hidden = true;
    if (Object.keys(errors).length) return;
    const model = state.dialog;
    const editing = Boolean(model.group);
    setUserDialogBusy(true);
    try {
      const path = editing ? `/user-groups?name=${encodeURIComponent(model.group.name)}` : "/user-groups";
      await api(path, { method: editing ? "PUT" : "POST", body: JSON.stringify(body) });
      state.dialog.submitting = false;
      closeDialog();
      showToast(editing ? "用戶資料已更新" : "用戶已建立", "success");
      await Promise.all([loadUsers({ silent: true, force: true }), loadOverview({ silent: true, force: true })]);
    } catch (error) {
      if (error.status === 401) return;
      setUserDialogBusy(false);
      showFormError(error.message);
    }
  }

  async function submitConfirm(form) {
    const model = state.dialog;
    if (model?.type !== "confirm" || model.submitting) return;
    model.submitting = true;
    const buttons = form.querySelectorAll("button");
    buttons.forEach((button) => { button.disabled = true; });
    const submit = form.querySelector("[data-dialog-submit]");
    const originalSubmitHTML = submit?.innerHTML || "";
    if (submit) {
      submit.classList.add("is-loading");
      submit.innerHTML = `${icon("refresh")}<span>處理中…</span>`;
    }
    try {
      await model.action();
      const successMessage = model.successMessage;
      model.submitting = false;
      closeDialog();
      showToast(successMessage, "success");
    } catch (error) {
      if (error.status === 401) return;
      model.submitting = false;
      buttons.forEach((button) => { button.disabled = false; });
      if (submit) {
        submit.classList.remove("is-loading");
        submit.innerHTML = originalSubmitHTML;
      }
      showFormError(error.message);
    }
  }

  function clearFieldError(field) {
    if (!field.name) return;
    field.removeAttribute("aria-invalid");
    const message = document.getElementById(`${field.name}-error`);
    if (message) {
      message.textContent = message.dataset.help || "";
      message.classList.remove("field-error");
    }
  }

  function findUserGroup(name) {
    return logicalUserGroups().find((group) => group.name === name) || null;
  }

  function findConnection(id) {
    return state.connections.find((connection) => String(connection.id) === String(id));
  }

  function findServerByTag(tag) {
    return state.servers.find((server) => String(server.tag) === String(tag));
  }

  app.addEventListener("submit", (event) => {
    event.preventDefault();
    const type = event.target.dataset.form;
    if (type === "login") submitLogin(event.target);
    if (type === "server") submitServer(event.target);
    if (type === "user") submitUser(event.target);
    if (type === "confirm") submitConfirm(event.target);
    if (type === "settings") submitSettings(event.target);
    if (type === "3x-ui-import") submit3XUIImport(event.target);
  });

  app.addEventListener("input", (event) => {
    const target = event.target;
    if (target.dataset.filter === "user-search") {
      state.filters.userSearch = target.value;
      const clear = target.parentElement.querySelector(".search-clear");
      if (clear) clear.hidden = !target.value;
      scheduleResultRender("users");
    }
    if (target.dataset.filter === "connection-search") {
      state.filters.connectionSearch = target.value;
      const clear = target.parentElement.querySelector(".search-clear");
      if (clear) clear.hidden = !target.value;
      scheduleResultRender("connections");
    }
    if (target.dataset.filter === "server-search") {
      state.filters.serverSearch = target.value;
      const clear = target.parentElement.querySelector(".search-clear");
      if (clear) clear.hidden = !target.value;
      scheduleResultRender("servers");
    }
    if (target.closest('[data-form="user"]')) clearFieldError(target);
    if (target.closest('[data-form="server"]')) clearFieldError(target);
    if (target.closest('[data-form="settings"]')) {
      clearFieldError(target);
      updateSettingsPreview(target.closest("form"));
    }
    if (target.name === "inbound_map" && target.closest('[data-form="3x-ui-import"]')) {
      state.import3XUI.mapping = target.value;
      state.import3XUI.error = "";
      invalidate3XUIImportReport();
    }
  });

  app.addEventListener("change", (event) => {
    const target = event.target;
    if (target.dataset.filter === "user-inbound") {
      state.filters.userInbound = target.value;
      renderUserResults();
    }
    if (target.dataset.filter === "connection-inbound") {
      state.filters.connectionInbound = target.value;
      renderConnectionResults();
    }
    if (target.dataset.filter === "connection-network") {
      state.filters.connectionNetwork = target.value;
      renderConnectionResults();
    }
    if (target.dataset.filter === "server-category") {
      state.filters.serverCategory = target.value;
      renderServerResults();
    }
    if (target.dataset.filter === "server-status") {
      state.filters.serverStatus = target.value;
      renderServerResults();
    }
    if (target.name === "protocol" && state.dialog?.type === "server" && !state.dialog.server) {
      const form = target.closest("form");
      const draft = captureServerDraft(form);
      const protocol = findProtocol(target.value);
      if (!protocol) return;
      draft.protocol = protocolKey(protocol);
      draft.config = prettyJSON(protocol.template);
      state.dialog.protocol = protocol;
      state.dialog.draft = draft;
      renderServerDialog();
      window.setTimeout(() => document.getElementById("server-protocol")?.focus(), 0);
    }
    if (target.name === "legacy_routes_enabled" && target.closest('[data-form="settings"]')) updateSettingsPreview(target.closest("form"));
    if (target.name === "database" && target.closest('[data-form="3x-ui-import"]')) {
      state.import3XUI.file = target.files?.[0] || null;
      state.import3XUI.error = "";
      invalidate3XUIImportReport();
      const fileState = document.getElementById("import-file-state");
      if (fileState) fileState.textContent = state.import3XUI.file ? `${state.import3XUI.file.name} · ${formatBytes(state.import3XUI.file.size)}` : "最大 256 MiB；檔案只會送往目前的 Core。";
    }
  });

  app.addEventListener("keydown", (event) => {
    const tab = event.target.closest?.('.user-dialog-tab[role="tab"]');
    if (!tab || !["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    const tabs = [...tab.parentElement.querySelectorAll('.user-dialog-tab[role="tab"]')];
    const current = tabs.indexOf(tab);
    const next = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : (current + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length;
    event.preventDefault();
    tabs[next]?.click();
  });

  app.addEventListener("click", async (event) => {
    const actionElement = event.target.closest("[data-action], [data-user-status]");
    if (!actionElement) {
      if (event.target.matches("dialog") && !state.dialog?.submitting) closeDialog();
      return;
    }
    const action = actionElement.dataset.action;
    event.preventDefault();

    if (action === "toggle-theme") toggleTheme();
    if (action === "retry") retryCurrent();
    if (action === "refresh-overview") loadOverview();
    if (action === "refresh-servers") loadServerData();
    if (action === "refresh-users") loadUsers();
    if (action === "refresh-connections") loadConnections();
    if (action === "refresh-settings") loadSettings({ force: true });
    if (action === "new-server") openServerDialog();
    if (action === "new-user") openUserDialog();
    if (action === "dialog-cancel") closeDialog();
    if (action === "reload-core") reloadCore();
    if (action === "apply-3x-ui-import") {
      const form = actionElement.closest('[data-form="3x-ui-import"]');
      if (form) submit3XUIImport(form, true);
    }

    if (action === "reset-settings-paths") {
      const form = actionElement.closest('[data-form="settings"]');
      if (form) {
        form.elements.subscription_path.value = DEFAULT_SUBSCRIPTION_PATH;
        form.elements.profile_page_path.value = DEFAULT_PROFILE_PATH;
        form.elements.legacy_routes_enabled.checked = true;
        applyFieldErrors(form, {});
        const formError = document.getElementById("settings-form-error");
        if (formError) formError.hidden = true;
        updateSettingsPreview(form);
        form.elements.subscription_path.focus();
      }
    }

    if (action === "logout") {
      state.epoch += 1;
      state.authenticated = false;
      clearToken();
      stopPolling();
      resetData();
      renderLogin("Token 已從這個分頁移除。", "info");
    }

    if (action === "toggle-token") {
      const input = document.getElementById("api-token");
      if (input) {
        const visible = input.type === "text";
        input.type = visible ? "password" : "text";
        actionElement.innerHTML = icon(visible ? "eye" : "eyeOff");
        actionElement.setAttribute("aria-label", visible ? "顯示 Token" : "隱藏 Token");
        actionElement.setAttribute("title", visible ? "顯示 Token" : "隱藏 Token");
      }
    }

    if (action === "clear-user-search") {
      state.filters.userSearch = "";
      const input = document.querySelector('[data-filter="user-search"]');
      if (input) input.value = "";
      actionElement.hidden = true;
      renderUserResults();
      input?.focus();
    }

    if (action === "clear-connection-search") {
      state.filters.connectionSearch = "";
      const input = document.querySelector('[data-filter="connection-search"]');
      if (input) input.value = "";
      actionElement.hidden = true;
      renderConnectionResults();
      input?.focus();
    }

    if (action === "clear-server-search") {
      state.filters.serverSearch = "";
      const input = document.querySelector('[data-filter="server-search"]');
      if (input) input.value = "";
      actionElement.hidden = true;
      renderServerResults();
      input?.focus();
    }

    if (action === "reset-user-filters") {
      state.filters.userSearch = "";
      state.filters.userInbound = "";
      state.filters.userStatus = "all";
      renderUsers();
      document.querySelector('[data-filter="user-search"]')?.focus();
    }

    if (action === "reset-connection-filters") {
      state.filters.connectionSearch = "";
      state.filters.connectionInbound = "";
      state.filters.connectionNetwork = "";
      renderConnections();
      document.querySelector('[data-filter="connection-search"]')?.focus();
    }

    if (action === "reset-server-filters") {
      state.filters.serverSearch = "";
      state.filters.serverCategory = "";
      state.filters.serverStatus = "";
      renderServers();
      document.querySelector('[data-filter="server-search"]')?.focus();
    }

    if (actionElement.dataset.userStatus) {
      state.filters.userStatus = actionElement.dataset.userStatus;
      document.querySelectorAll("[data-user-status]").forEach((button) => button.setAttribute("aria-pressed", String(button === actionElement)));
      renderUserResults();
    }

    if (action === "generate-uuid") {
      try {
        const field = document.getElementById(`uuid_${actionElement.dataset.index}`);
        if (field) {
          field.value = secureUUID();
          clearFieldError(field);
          field.focus();
          field.select();
        }
      } catch (error) {
        showToast(error.message, "error");
      }
    }

    if (action === "select-user-tab" && state.dialog?.type === "user") {
      const form = actionElement.closest("form");
      state.dialog.draft = captureUserDraft(form);
      state.dialog.activeTab = actionElement.dataset.tab;
      renderUserDialog();
      window.setTimeout(() => document.getElementById(`user-tab-${state.dialog.activeTab}`)?.focus(), 0);
    }

    if (action === "select-user-membership" && state.dialog?.type === "user") {
      const form = actionElement.closest("form");
      state.dialog.draft = captureUserDraft(form);
      state.dialog.activeMembership = Number(actionElement.dataset.index);
      renderUserDialog();
      window.setTimeout(() => document.querySelector(`.membership-card[data-membership-index="${state.dialog.activeMembership}"] input:not([type=hidden])`)?.focus(), 0);
    }

    if (action === "generate-password") {
      try {
        const index = Number(actionElement.dataset.index);
        const field = document.getElementById(`password_${index}`);
        if (field) {
          field.value = securePasswordForInbound(inboundFor(state.dialog?.draft.memberships[index]?.inbound));
          clearFieldError(field);
          field.focus();
          field.select();
          showToast("已產生安全隨機密碼", "success");
        }
      } catch (error) {
        showToast(error.message, "error");
      }
    }

    if (action === "toggle-user-password") {
      const index = Number(actionElement.dataset.index);
      const field = document.getElementById(`password_${index}`);
      if (field && state.dialog?.type === "user") {
        const visible = field.type === "password";
        state.dialog.draft.memberships[index].passwordVisible = visible;
        field.type = visible ? "text" : "password";
        actionElement.innerHTML = icon(visible ? "eyeOff" : "eye");
        actionElement.setAttribute("aria-label", visible ? "隱藏密碼" : "顯示密碼");
        actionElement.setAttribute("title", visible ? "隱藏密碼" : "顯示密碼");
      }
    }

    if (action === "add-user-membership" && state.dialog?.type === "user") {
      const form = actionElement.closest("form");
      state.dialog.draft = captureUserDraft(form);
      const inbound = inboundFor(document.getElementById("add-user-inbound")?.value);
      if (inbound?.managed) state.dialog.draft.memberships.push(newMembershipDraft(inbound));
      state.dialog.activeMembership = state.dialog.draft.memberships.length - 1;
      renderUserDialog();
      window.setTimeout(() => document.querySelector(".membership-card:not([hidden]) input:not([type=hidden])")?.focus(), 0);
    }

    if (action === "remove-user-membership" && state.dialog?.type === "user") {
      const form = actionElement.closest("form");
      state.dialog.draft = captureUserDraft(form);
      state.dialog.draft.memberships.splice(Number(actionElement.dataset.index), 1);
      state.dialog.activeMembership = Math.min(state.dialog.activeMembership, state.dialog.draft.memberships.length - 1);
      renderUserDialog();
      window.setTimeout(() => document.querySelector(".user-node-option[aria-pressed=true]")?.focus(), 0);
    }

    if (action === "copy-subscription-url") {
      const value = document.getElementById("user-subscription-url")?.value;
      try {
        if (!value || !navigator.clipboard?.writeText) throw new APIError("此瀏覽器不支援剪貼簿存取");
        await navigator.clipboard.writeText(value);
        showToast("已複製訂閱連結", "success");
      } catch (error) {
        showToast(error.message || "無法複製訂閱連結", "error");
      }
    }

    if (action === "toggle-subscription-url") {
      const field = document.getElementById("user-subscription-url");
      if (field) {
        const visible = field.type === "text";
        field.type = visible ? "password" : "text";
        actionElement.innerHTML = icon(visible ? "eye" : "eyeOff");
        actionElement.setAttribute("aria-label", visible ? "顯示訂閱連結" : "隱藏訂閱連結");
        actionElement.setAttribute("title", visible ? "顯示訂閱連結" : "隱藏訂閱連結");
      }
    }

    if (action === "edit-user") {
      const group = findUserGroup(actionElement.dataset.name);
      if (!group) {
        showToast("找不到此用戶，請重新整理", "error");
      } else {
        const requestID = ++state.detailRequest;
        const epoch = state.epoch;
        const route = state.route;
        try {
          const detail = await api(`/user-groups?name=${encodeURIComponent(group.name)}`);
          if (requestID !== state.detailRequest || epoch !== state.epoch || route !== state.route || !state.authenticated) return;
          openUserDialog(detail);
        } catch (error) {
          if (error.status !== 401) showToast(error.message, "error");
        }
      }
    }

    if (action === "edit-server") {
      const server = findServerByTag(actionElement.dataset.tag);
      if (!server) {
        showToast("找不到此節點，請重新整理", "error");
      } else {
        const requestID = ++state.detailRequest;
        const epoch = state.epoch;
        const route = state.route;
        try {
          const detail = await api(`/servers/${encodeURIComponent(server.tag)}`);
          if (requestID !== state.detailRequest || epoch !== state.epoch || route !== state.route || !state.authenticated) return;
          openServerDialog(detail);
        } catch (error) {
          if (error.status !== 401) showToast(error.message, "error");
        }
      }
    }

    if (action === "delete-server") {
      const server = findServerByTag(actionElement.dataset.tag);
      if (state.reloading || state.loading.protocols || state.loading.servers || !server || server.source !== "dashboard" || !server.editable || server.status === "pending_delete") return;
      openConfirm({
        title: "刪除此節點？",
        message: `「${server.tag}」將排入待刪除變更；正在運行的節點會持續服務，直到重新載入 Core。`,
        confirmLabel: "刪除節點",
        successMessage: "節點刪除變更已儲存",
        action: async () => {
          await api(`/servers/${encodeURIComponent(server.tag)}?revision=${encodeURIComponent(server.revision)}`, { method: "DELETE" });
          await loadServerData({ silent: true, force: true });
        },
      });
    }

    if (action === "delete-user") {
      const group = findUserGroup(actionElement.dataset.name);
      if (!group) return;
      const revisions = Object.fromEntries(group.memberships.map((membership) => [membership.inbound, membership.revision]));
      openConfirm({
        title: "刪除此用戶？",
        message: `「${group.name}」在 ${group.memberships.length} 個節點上的 membership 將全部刪除，並立即無法建立新連線。`,
        confirmLabel: "刪除用戶",
        successMessage: "用戶已刪除",
        action: async () => {
          await api(`/user-groups?name=${encodeURIComponent(group.name)}`, { method: "DELETE", body: JSON.stringify({ account_revision: Number(group.account?.revision) || 0, revisions }) });
          await Promise.all([loadUsers({ silent: true, force: true }), loadOverview({ silent: true, force: true })]);
        },
      });
    }

    if (action === "reset-user-traffic") {
      const group = findUserGroup(actionElement.dataset.name);
      if (!group) return;
      openConfirm({
        title: "將流量統計歸零？",
        message: `「${group.name}」目前跨節點累積 ${formatBytes(group.upload_bytes + group.download_bytes)}。所有 membership 歸零後，額度將從零重新計算。`,
        confirmLabel: "確認歸零",
        successMessage: "用戶流量已歸零",
        danger: false,
        action: async () => {
          await api(`/user-groups/reset-traffic?name=${encodeURIComponent(group.name)}`, { method: "POST" });
          await loadUsers({ silent: true, force: true });
        },
      });
    }

    if (action === "close-connection") {
      const connection = findConnection(actionElement.dataset.id);
      if (!connection) return;
      openConfirm({
        title: "關閉這條連線？",
        message: `${connection.user || "未識別用戶"} 至 ${connection.destination || "未知目的地"} 的工作階段將立即中止。`,
        confirmLabel: "關閉連線",
        successMessage: "連線已關閉",
        action: async () => {
          await api(`/connections/${encodeURIComponent(connection.id)}`, { method: "DELETE" });
          await Promise.all([loadConnections({ silent: true, force: true }), loadOverview({ silent: true, force: true })]);
        },
      });
    }

    if (action === "close-all-connections") {
      openConfirm({
        title: "關閉全部連線？",
        message: `目前 ${formatInteger(state.connections.length)} 條工作階段將立即中止，用戶可能會自動重新連線。`,
        confirmLabel: "全部關閉",
        successMessage: "所有連線已關閉",
        action: async () => {
          await api("/connections", { method: "DELETE" });
          await Promise.all([loadConnections({ silent: true, force: true }), loadOverview({ silent: true, force: true })]);
        },
      });
    }
  });

  window.addEventListener("hashchange", () => {
    const requested = window.location.hash.replace(/^#\//, "");
    if (!Object.hasOwn(ROUTES, requested)) {
      window.history.replaceState(null, "", "#/overview");
    }
    if (state.authenticated) activateRoute();
    else state.route = routeFromHash();
  });

  window.addEventListener("online", () => {
    setOffline(false);
    if (state.authenticated) retryCurrent();
  });

  window.addEventListener("offline", () => setOffline(true));

  window.addEventListener("resize", () => {
    if (responsiveRenderFrame || mobileDataListMode === null) return;
    responsiveRenderFrame = window.requestAnimationFrame(() => {
      responsiveRenderFrame = 0;
      const nextMode = currentMobileDataListMode();
      if (nextMode === mobileDataListMode) return;
      mobileDataListMode = nextMode;
      if (state.route === "users" || state.route === "connections" || state.route === "servers") scheduleResultRender(state.route);
    });
  });

  document.addEventListener("visibilitychange", () => {
    if (!document.hidden && state.authenticated) {
      loadOverview({ silent: true });
      if (state.route === "connections") loadConnections({ silent: true });
    }
  });

  if (!Object.hasOwn(ROUTES, window.location.hash.replace(/^#\//, ""))) {
    window.history.replaceState(null, "", "#/overview");
    state.route = "overview";
  }
  applyTheme();
  bootstrap();
})();
