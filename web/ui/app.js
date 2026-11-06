(function () {
  "use strict";

  const {
    esc,
    formatTime,
    formatBytes,
    qualityLabel,
    dayBucket,
    coverURL,
    resolvedCoverURL,
    CoverHydrator,
    matchesQuery,
    normalizeDrama,
    mergeDrama,
    releaseLabel,
    toast
  } = window.ZGJUtils;
  const API = window.ZGJApi;
  const Player = window.ZGJPlayer;

  const THEME_COLORS = [
    { id: "orange", label: "暖橙", hex: "#ff664f" },
    { id: "rose", label: "玫红", hex: "#c25485" },
    { id: "amber", label: "琥珀", hex: "#d48806" },
    { id: "teal", label: "青绿", hex: "#009c84" },
    { id: "blue", label: "海蓝", hex: "#0089d0" },
    { id: "violet", label: "蓝紫", hex: "#7970d5" }
  ];

  const SETTINGS_TABS = [
    { id: "appearance", label: "外观与布局" },
    { id: "playback", label: "播放偏好" },
    { id: "download", label: "离线下载" },
    { id: "sources", label: "站源管理", adminOnly: true },
    { id: "proxy", label: "网络代理", adminOnly: true },
    { id: "users", label: "多用户与权限" },
    { id: "about", label: "关于应用" },
    { id: "diagnostics", label: "备份与诊断" }
  ];

  const TV_KEYS = [
    "A", "B", "C", "D", "E", "F", "G", "H", "I",
    "J", "K", "L", "M", "N", "O", "P", "Q", "R",
    "S", "T", "U", "V", "W", "X", "Y", "Z",
    "1", "2", "3", "4", "5", "6", "7", "8", "9", "0"
  ];

  function normalizeCategoryLabel(name) {
    const compact = String(name || "").trim().replace(/\s+/g, "");
    switch (compact) {
      case "AI成人短剧":
      case "AI短剧":
        return "AI 短剧";
      case "AI成人漫剧":
      case "AI漫剧":
        return "AI 漫剧";
      case "AI换脸":
        return "AI 换脸";
      case "AI魔改":
        return "AI 魔改";
      case "AI剧":
        return "AI 剧";
      case "":
        return "未分类";
      default:
        return String(name || "").trim();
    }
  }

  class ZGJWebApp {
    constructor() {
      this.version = "";
      this.disableImages = false;
      this.hongguoReady = true;
      this.ffmpegReady = false;
      this.state = null;
      this.sidebarCollapsed = localStorage.getItem("zgj_sidebar_collapsed") === "1";

      this.view = "explore";
      this.settingsTab = "appearance";

      this.exploreSource = "";
      this.exploreTabs = [];
      this.exploreTabId = "";
      this.exploreItems = [];
      this.exploreRanks = new Map();
      this.exploreLibrary = new Map();
      this.explorePage = 1;
      this.exploreHasMore = false;
      this.exploreLoading = false;
      this.exploreWarning = "";
      this.exploreError = "";
      this.exploreEpoch = 0;

      this.searchQuery = "";
      this.searchSuggestions = [];
      this.searchItems = [];
      this.searchPage = 1;
      this.searchHasMore = false;
      this.searchLoading = false;
      this.searchWarning = "";
      this.searchSubmitted = false;
      this.searchEpoch = 0;
      this.suggestTimer = null;

      this.followFilter = "";
      this.followSourceFilter = "";
      this.followBatchMode = false;
      this.followSelected = new Set();
      this.followChecking = false;

      this.historyFilter = "";
      this.historySourceFilter = "";
      this.historyBatchMode = false;
      this.historySelected = new Set();

      this.downloadTab = "active";
      this.downloadJobs = [];
      this.downloadPollTimer = null;
      this.storageInfo = null;
      this.resourceSettings = null;
      this.logLines = [];

      this.detailOpen = false;
      this.detailDrama = null;
      this.detailEpisodes = [];
      this.detailWarning = "";
      this.detailLoading = false;
      this.detailError = "";
      this.detailChunkIndex = 0;
      this.detailEpoch = 0;
      this.detailPreloaded = null;

      this.deferredInstallPrompt = null;
      this.pwaInstalled =
        !!(window.matchMedia && window.matchMedia("(display-mode: standalone)").matches) ||
        window.navigator.standalone === true;

      window.addEventListener("beforeinstallprompt", (ev) => {
        ev.preventDefault();
        this.deferredInstallPrompt = ev;
        if (this.view === "settings" && this.settingsTab === "about") {
          this.render();
        }
      });
      window.addEventListener("appinstalled", () => {
        this.deferredInstallPrompt = null;
        this.pwaInstalled = true;
        if (this.view === "settings" && this.settingsTab === "about") {
          this.render();
        }
      });

      this.mediaDark = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)") : null;
      if (this.mediaDark && this.mediaDark.addEventListener) {
        this.mediaDark.addEventListener("change", () => this.applyAppearance());
      }
      window.addEventListener("resize", () => this.applyAppearance());
      window.addEventListener("keydown", (e) => this._handleGlobalKeys(e));
      CoverHydrator.configure({
        dramaLookup: (id) => this._findDramaById(id),
        dramaUpdater: (fresh) => this._applyDramaMetadataUpdate(fresh)
      });
    }

    async boot() {
      if (window.isSecureContext && "serviceWorker" in navigator) {
        navigator.serviceWorker.register("/sw.js", { scope: "/" }).catch(() => {});
      }
      try {
        const boot = await API.bootstrap();
        this.version = boot.version || "";
        this.disableImages = !!boot.disableImages;
        CoverHydrator.configure({
          disableImages: this.disableImages,
          dramaLookup: (id) => this._findDramaById(id),
          dramaUpdater: (fresh) => this._applyDramaMetadataUpdate(fresh)
        });
        this.hongguoReady = boot.hongguoReady !== false;
        this.ffmpegReady = !!boot.ffmpegReady;
        this.setState(boot.state);

        if (!this.state.locked) {
          await this._afterUnlockedInit();
        }
      } catch (err) {
        const appRoot = document.getElementById("app");
        appRoot.innerHTML = `
          <div class="boot-screen">
            <img src="/icon.svg" alt="" class="boot-logo" />
            <div style="font-weight:700;font-size:16px;">连接网页服务失败</div>
            <div class="boot-label">${esc(err.message)}</div>
            <button class="btn btn-primary" id="boot-retry-btn">重新连接</button>
          </div>
        `;
        document.getElementById("boot-retry-btn").onclick = () => this.boot();
      }
    }

    setState(nextState) {
      if (!nextState) return;
      const prevSource = this.state ? this.state.source : "";
      const wasLocked = this.state ? this.state.locked : true;
      this.state = nextState;
      document.title = `${this.state.appName || "真果鉴"} 网页版`;
      this.applyAppearance();

      if (this.state.locked) {
        this._stopDownloadsPolling();
        this.render();
        return;
      }

      if (wasLocked || prevSource !== this.state.source) {
        this._initExploreForSource(this.state.source);
        if (!wasLocked && prevSource && this.view === "search" && this.searchQuery.trim()) {
          this.runSearch(this.searchQuery, 1);
          return;
        }
      }
      if (this.state.canDownload) {
        this._refreshDownloadsOnce();
      }
      this.render();
    }

    async _afterUnlockedInit() {
      this._initExploreForSource(this.state.source);
      if (this.state.canDownload) {
        await this._refreshDownloadsOnce();
      }
      if (!this.hongguoReady && this._sourceEnabled("hongguo")) {
        this._openHongguoDeviceModal();
      }
      if (this.state.shareUsage && this.version) {
        API.rpc("reportUsage", { version: this.version }).catch(() => {});
      }
      if (this.state.checkUpdates && this.version) {
        API.rpc("checkUpdate", { version: this.version })
          .then((res) => {
            if (res && res.available && res.version && res.version !== this.state.ignoredUpdate) {
              this._openUpdateModal(res.version, res.url);
            }
          })
          .catch(() => {});
      }
    }

    _sourceEnabled(id) {
      return !!(this.state && Array.isArray(this.state.sources) && this.state.sources.some((s) => s.id === id));
    }

    _sourceObj(id) {
      if (!this.state) return { id, name: id };
      const all = this.state.knownSources || this.state.sources || [];
      return all.find((s) => s.id === id) || { id, name: id };
    }

    effectiveLayout() {
      const dm = (this.state && this.state.displayMode) || "auto";
      if (dm !== "auto") return dm;
      const ua = navigator.userAgent || "";
      if (/TV|AFT|BRAVIA|MiBOX|Dangbei|Changhong|Hisense|TCL|Konka|Skyworth|Web0S|Tizen/i.test(ua)) {
        return "television";
      }
      const w = window.innerWidth || 1024;
      if (w < 680) return "phone";
      if (w < 1040) return "pad";
      return "window";
    }

    applyAppearance() {
      const html = document.documentElement;
      const tm = (this.state && this.state.themeMode) || "system";
      const dark = tm === "dark" || (tm === "system" && (!this.mediaDark || this.mediaDark.matches));
      const nextTheme = dark ? "dark" : "light";
      const nextColor = (this.state && this.state.themeColor) || "orange";
      const prevLayout = html.getAttribute("data-layout");
      const nextLayout = this.effectiveLayout();

      html.setAttribute("data-theme", nextTheme);
      html.setAttribute("data-color", nextColor);
      html.setAttribute("data-layout", nextLayout);

      const metaTheme = document.querySelector('meta[name="theme-color"]');
      if (metaTheme) {
        metaTheme.setAttribute("content", dark ? "#0f1218" : "#f4f6fa");
      }
      if (prevLayout && prevLayout !== nextLayout && this.state) {
        this.render();
      }
    }

    followUpdateCount() {
      if (!this.state || !Array.isArray(this.state.favorites)) return 0;
      const states = this.state.followStates || {};
      let count = 0;
      for (const d of this.state.favorites) {
        if (this._hasUnreadUpdate(d, states[d.id])) count++;
      }
      return count;
    }

    _hasUnreadUpdate(drama, st) {
      if (!drama || !st) return false;
      const eps = Number(drama.episodes) || 0;
      const known = Math.max(Number(st.knownEpisodes) || 0, eps);
      const read = st.readEpisodes == null ? 0 : Number(st.readEpisodes) || 0;
      return known > 0 && read < known && st.readEpisodes != null;
    }

    _watchEntryFor(dramaId) {
      if (!this.state || !Array.isArray(this.state.history)) return null;
      return this.state.history.find((h) => h.drama && h.drama.id === dramaId) || null;
    }

    _isFollowed(dramaId) {
      if (!this.state || !Array.isArray(this.state.favorites)) return false;
      return this.state.favorites.some((d) => d && d.id === dramaId);
    }

    _downloadedEpisodeSet(dramaId) {
      const set = new Set();
      for (const j of this.downloadJobs) {
        if (j.state === "completed" && j.drama && j.drama.id === dramaId) {
          set.add(Number(j.index) || 0);
        }
      }
      return set;
    }

    async _openHongguoDeviceModal() {
      const modal = document.getElementById("modal-root");
      modal.classList.remove("hidden");
      const renderRegistering = (errMsg) => {
        modal.innerHTML = `
          <div class="modal-card">
            <div class="modal-header">
              <h3 class="modal-title">初始化红果访问设备</h3>
            </div>
            <div class="modal-body">
              ${
                errMsg
                  ? `<div class="warning-banner"><span>${esc(errMsg)}</span></div>
                     <div style="font-size:13px;color:var(--text-secondary);">红果接口需要首次注册虚拟设备参数，若当前网络受限，可配置网络代理后重试。</div>`
                  : `<div style="display:flex;align-items:center;gap:12px;padding:12px 0;">
                       <span class="inline-spinner"></span>
                       <span>正在向红果服务注册设备凭证，请稍候...</span>
                     </div>`
              }
            </div>
            ${
              errMsg
                ? `<div class="modal-footer">
                     <button class="btn" id="hg-proxy-btn">网络代理设置</button>
                     <button class="btn" id="hg-skip-btn">稍后再试</button>
                     <button class="btn btn-primary" id="hg-retry-btn">重新注册</button>
                   </div>`
                : ""
            }
          </div>
        `;
        if (errMsg) {
          modal.querySelector("#hg-retry-btn").onclick = runRegister;
          modal.querySelector("#hg-skip-btn").onclick = () => this._closeModal();
          modal.querySelector("#hg-proxy-btn").onclick = () => {
            this._closeModal();
            this.view = "settings";
            this.settingsTab = "proxy";
            this.render();
          };
        }
      };

      const runRegister = async () => {
        renderRegistering("");
        try {
          const res = await API.rpc("hongguoDevices", { command: "register" });
          if (res && res.ready) {
            this.hongguoReady = true;
            this._closeModal();
            toast("红果设备初始化完成");
            if (this.state.source === "hongguo") {
              this._reloadExplore(true);
            }
          } else {
            renderRegistering("未能完成红果设备注册，请检查网络连接");
          }
        } catch (err) {
          renderRegistering(err.message || "注册请求失败");
        }
      };

      await runRegister();
    }

    _openUpdateModal(newVer, downloadUrl) {
      const modal = document.getElementById("modal-root");
      modal.classList.remove("hidden");
      modal.innerHTML = `
        <div class="modal-card">
          <div class="modal-header">
            <h3 class="modal-title">发现新版本 ${esc(newVer)}</h3>
            <button class="icon-btn" id="upd-close">✕</button>
          </div>
          <div class="modal-body">
            <div>当前版本：${esc(this.version)}，检测到可用更新版本 <strong>${esc(newVer)}</strong>。</div>
          </div>
          <div class="modal-footer">
            <button class="btn" id="upd-ignore">忽略此版本</button>
            ${downloadUrl ? `<a class="btn btn-primary" href="${esc(downloadUrl)}" target="_blank" rel="noopener noreferrer">前往下载</a>` : ""}
          </div>
        </div>
      `;
      modal.querySelector("#upd-close").onclick = () => this._closeModal();
      modal.querySelector("#upd-ignore").onclick = async () => {
        this._closeModal();
        try {
          const res = await API.updateGlobalSettings({ ignoredUpdate: newVer });
          if (res.state) this.setState(res.state);
        } catch (_) {}
      };
    }

    _closeModal() {
      const modal = document.getElementById("modal-root");
      modal.classList.add("hidden");
      modal.innerHTML = "";
    }

    async _initExploreForSource(sourceId) {
      this.exploreSource = sourceId || "";
      this.exploreTabs = [];
      this.exploreItems = [];
      this.exploreRanks.clear();
      this.exploreLibrary.clear();
      this.explorePage = 1;
      this.exploreHasMore = false;
      this.exploreWarning = "";
      this.exploreError = "";

      if (!this.exploreSource) {
        this.render();
        return;
      }

      const isHG = this.exploreSource === "hongguo";
      this.exploreTabs = isHG
        ? [{ id: "app:recommend", name: "推荐" }, { id: "", name: "全部" }]
        : [{ id: "", name: "全部" }];
      this.exploreTabId = this.exploreTabs[0].id;

      this._reloadExplore(false);
      this._loadExploreCategories(false);
    }

    async _loadExploreCategories(force) {
      const src = this.exploreSource;
      if (!src) return;
      try {
        const [boardsRes, catsRes, cachedRes] = await Promise.all([
          API.rpc("rankingBoards").catch(() => ({ items: [] })),
          API.rpc("categories", { source: src, force: !!force, session: `categories-${src}` }).catch(() => ({ items: [] })),
          API.rpc("cached", { source: src, category: "" }).catch(() => ({ items: [] }))
        ]);
        if (src !== this.exploreSource) return;

        for (const row of (cachedRes && cachedRes.items) || []) {
          const d = normalizeDrama(row);
          if (d.id) this.exploreLibrary.set(d.id, d);
        }

        const boards = ((boardsRes && boardsRes.items) || []).filter((b) => b.source === src);
        const remoteCats = [];
        const seenNames = new Set();
        for (const c of (catsRes && catsRes.items) || []) {
          if (c && c.id) {
            const label = normalizeCategoryLabel(c.name);
            if (!seenNames.has(label)) {
              seenNames.add(label);
              remoteCats.push({ id: c.id, name: label, local: false });
            }
          }
        }

        const hasTopic = remoteCats.some((c) => c.id.startsWith("tag:"));
        const localCats = [];
        if (!hasTopic && remoteCats.length === 0) {
          const localSet = new Set();
          for (const d of this.exploreLibrary.values()) {
            const label = normalizeCategoryLabel(d.category);
            if (label && label !== "未分类" && !seenNames.has(label)) {
              localSet.add(label);
            }
          }
          [...localSet].sort().forEach((name) => {
            localCats.push({ id: `local:${name}`, name, local: true });
          });
        }

        const tabs = [];
        if (src === "hongguo") tabs.push({ id: "app:recommend", name: "推荐" });
        tabs.push({ id: "", name: "全部" });
        for (const b of boards) {
          tabs.push({ id: `app:rank:${b.id}`, name: b.name, boardId: b.id });
        }
        tabs.push(...remoteCats, ...localCats);
        this.exploreTabs = tabs;
        if (!tabs.some((t) => t.id === this.exploreTabId)) {
          this.exploreTabId = tabs[0].id;
        }
        if (this.view === "explore") this.render();
      } catch (_) {}
    }

    selectExploreTab(tabId) {
      if (this.exploreTabId === tabId && !this.exploreError) return;
      this.exploreTabId = tabId;
      this.exploreItems = [];
      this.exploreRanks.clear();
      this.explorePage = 1;
      this.exploreHasMore = false;
      this.exploreWarning = "";
      this.exploreError = "";
      this._reloadExplore(false);
    }

    async _reloadExplore(force) {
      await this._fetchExplorePage(1, !!force);
    }

    async _loadMoreExplore() {
      if (this.exploreLoading || !this.exploreHasMore) return;
      await this._fetchExplorePage(this.explorePage + 1, false);
    }

    async _fetchExplorePage(page, force) {
      const src = this.exploreSource;
      if (!src) return;
      const epoch = ++this.exploreEpoch;
      this.exploreLoading = true;
      this.exploreError = "";
      if (page === 1 && !force && this.exploreTabId !== "app:recommend" && !this.exploreTabId.startsWith("app:rank:")) {
        try {
          const catParam = this.exploreTabId.startsWith("local:") ? "" : this.exploreTabId;
          const cached = await API.rpc("cached", { source: src, category: catParam });
          if (epoch === this.exploreEpoch && cached && Array.isArray(cached.items) && cached.items.length > 0 && this.exploreItems.length === 0) {
            this.exploreItems = this._filterLocalCat(cached.items.map(normalizeDrama));
            this.render();
          }
        } catch (_) {}
      } else if (page === 1 && !force && this.exploreTabId === "app:recommend") {
        try {
          const cachedRec = await API.rpc("cachedRecommendations", { category: "find_drama" });
          if (epoch === this.exploreEpoch && cachedRec && Array.isArray(cachedRec.items) && cachedRec.items.length > 0 && this.exploreItems.length === 0) {
            this.exploreItems = cachedRec.items.map(normalizeDrama);
            this.render();
          }
        } catch (_) {}
      }
      this.render();

      try {
        let res = null;
        if (this.exploreTabId === "app:recommend") {
          res = await API.rpc("recommendations", {
            category: "find_drama",
            command: page > 1 ? "more" : "",
            force: !!force,
            session: "recommendations-find_drama"
          });
          if (epoch !== this.exploreEpoch) return;
          const list = (res.items || []).map(normalizeDrama);
          list.forEach((d) => this.exploreLibrary.set(d.id, d));
          this.exploreItems = list;
          this.exploreHasMore = !!res.hasMore;
          this.explorePage = page;
          this.exploreWarning = res.warning || "";
        } else if (this.exploreTabId.startsWith("app:rank:")) {
          const boardId = this.exploreTabId.slice("app:rank:".length);
          res = await API.rpc("rankings", {
            board: boardId,
            page,
            force: !!force
          });
          if (epoch !== this.exploreEpoch) return;
          if (page === 1) this.exploreRanks.clear();
          const dramas = [];
          for (const row of res.items || []) {
            if (row && row.drama) {
              const d = normalizeDrama(row.drama);
              this.exploreRanks.set(d.id, Number(row.rank) || 0);
              this.exploreLibrary.set(d.id, d);
              dramas.push(d);
            }
          }
          this.exploreItems = this._mergeUniqueDramas(page === 1 ? [] : this.exploreItems, dramas);
          this.exploreHasMore = !!res.hasMore;
          this.explorePage = page;
          this.exploreWarning = res.warning || "";
        } else {
          const catParam = this.exploreTabId.startsWith("local:") ? "" : this.exploreTabId;
          res = await API.rpc("catalog", {
            source: src,
            category: catParam,
            page,
            query: "",
            force: !!force,
            session: `catalog-${src}`
          });
          if (epoch !== this.exploreEpoch) return;
          const dramas = (res.items || []).map(normalizeDrama);
          dramas.forEach((d) => this.exploreLibrary.set(d.id, d));
          const filtered = this._filterLocalCat(dramas);
          this.exploreItems = this._mergeUniqueDramas(page === 1 ? [] : this.exploreItems, filtered);
          this.exploreHasMore = !!res.hasMore;
          this.explorePage = page;
          this.exploreWarning = res.warning || "";
        }
      } catch (err) {
        if (epoch !== this.exploreEpoch) return;
        this.exploreError = err.message || "加载内容失败";
      } finally {
        if (epoch === this.exploreEpoch) {
          this.exploreLoading = false;
          this.render();
        }
      }
    }

    _filterLocalCat(dramas) {
      if (!this.exploreTabId.startsWith("local:")) return dramas;
      const target = this.exploreTabId.slice("local:".length);
      const out = [];
      for (const d of this.exploreLibrary.values()) {
        if (normalizeCategoryLabel(d.category) === target) {
          out.push(d);
        }
      }
      return out.length > 0 ? out : dramas.filter((d) => normalizeCategoryLabel(d.category) === target);
    }

    _mergeUniqueDramas(existing, incoming) {
      const map = new Map();
      for (const d of existing) map.set(d.id, d);
      for (const d of incoming) {
        map.set(d.id, map.has(d.id) ? mergeDrama(map.get(d.id), d) : d);
      }
      return [...map.values()];
    }

    async runSearch(query, page) {
      const raw = String(query != null ? query : this.searchQuery).trim();
      const q = raw === "[object Object]" ? "" : raw;
      this.searchQuery = q;
      clearTimeout(this.suggestTimer);
      const epoch = ++this.searchEpoch;
      if (!q) {
        this.searchSubmitted = false;
        this.searchSuggestions = [];
        this.searchItems = [];
        this.searchLoading = false;
        this.searchWarning = "";
        this.render();
        return;
      }
      const p = Number(page) > 0 ? Number(page) : 1;
      this.searchSubmitted = true;
      this.searchSuggestions = [];
      this.searchLoading = true;
      this.searchWarning = "";
      if (p === 1) this.searchItems = [];
      this.render();

      API.updateUserSettings({ rememberSearch: q })
        .then((res) => {
          if (res && res.state) this.state = res.state;
        })
        .catch(() => {});

      const src = this.state.source;
      const srcInfo = this._sourceObj(src);
      try {
        const res = await API.rpc("catalog", {
          source: src,
          query: q,
          page: p,
          category: "",
          force: false,
          session: `catalog-${src}-search`
        });
        if (epoch !== this.searchEpoch) return;
        let items = (res.items || []).map(normalizeDrama);
        if (!srcInfo.onlineSearch && items.length === 0 && this.exploreLibrary.size > 0) {
          items = [...this.exploreLibrary.values()].filter((d) => matchesQuery(d, q));
        }
        this.searchItems = this._mergeUniqueDramas(p === 1 ? [] : this.searchItems, items);
        this.searchPage = p;
        this.searchHasMore = !!(srcInfo.onlineSearch && res.hasMore);
        this.searchWarning = res.warning || "";
      } catch (err) {
        if (epoch !== this.searchEpoch) return;
        const localFallback = [...this.exploreLibrary.values()].filter((d) => matchesQuery(d, q));
        if (localFallback.length > 0) {
          this.searchItems = localFallback;
          this.searchWarning = `在线搜索受限，已显示本地缓存匹配结果 (${err.message})`;
        } else {
          this.searchWarning = err.message || "搜索失败";
        }
        this.searchHasMore = false;
      } finally {
        if (epoch === this.searchEpoch) {
          this.searchLoading = false;
          this.render();
        }
      }
    }

    _triggerSuggestions(q) {
      clearTimeout(this.suggestTimer);
      const trimmed = String(q || "").trim();
      if (!trimmed || this.state.source !== "hongguo") {
        this.searchSuggestions = [];
        this._renderSuggestionsDOM();
        return;
      }
      this.suggestTimer = setTimeout(async () => {
        try {
          const res = await API.rpc("suggestions", {
            query: trimmed,
            session: "suggestions"
          });
          if (this.searchQuery.trim() === trimmed && !this.searchSubmitted) {
            const rawItems = Array.isArray(res.items) ? res.items : [];
            this.searchSuggestions = rawItems
              .map((item) => (typeof item === "string" ? item : (item && item.name) || ""))
              .map((s) => String(s).trim())
              .filter((s) => s.length > 0 && s !== "[object Object]")
              .slice(0, 8);
            this._renderSuggestionsDOM();
          }
        } catch (_) {}
      }, 220);
    }

    _renderSuggestionsDOM() {
      const box = document.getElementById("search-suggestions-slot");
      if (!box) return;
      if (this.searchSuggestions.length === 0) {
        box.innerHTML = "";
        return;
      }
      box.innerHTML = `
        <div class="suggestions-panel">
          ${this.searchSuggestions
            .map((s) => `<button class="suggestion-item" data-sug="${esc(s)}">🔍 <span>${esc(s)}</span></button>`)
            .join("")}
        </div>
      `;
      box.querySelectorAll("[data-sug]").forEach((btn) => {
        btn.onclick = () => this.runSearch(btn.getAttribute("data-sug"), 1);
      });
    }

    _releaseDetailPreload() {
      if (this.detailPreloaded && this.detailPreloaded.plan && this.detailPreloaded.plan.session) {
        API.rpc("release", { session: this.detailPreloaded.plan.session }).catch(() => {});
      }
      this.detailPreloaded = null;
    }

    async openDetail(rawDrama) {
      const epoch = ++this.detailEpoch;
      this._releaseDetailPreload();
      const base = normalizeDrama(rawDrama);
      this.detailOpen = true;
      this.detailDrama = base;
      this.detailEpisodes = [];
      this.detailWarning = "";
      this.detailLoading = true;
      this.detailError = "";
      this.detailChunkIndex = 0;
      this._renderDetailOverlay();

      if (this._isFollowed(base.id)) {
        API.markFollowRead(base.id, base.episodes)
          .then((r) => {
            if (r && r.state) {
              this.state = r.state;
              this.render();
            }
          })
          .catch(() => {});
      }

      try {
        const res = await API.rpc("detail", { drama: base });
        if (epoch !== this.detailEpoch || !this.detailOpen) return;
        const freshDrama = res.drama ? mergeDrama(base, res.drama) : base;
        const chapters = Array.isArray(res.chapters) ? res.chapters : [];
        const eps = chapters.map((ch, i) => {
          const num = Number(ch.currentEpisode) > 0 ? Number(ch.currentEpisode) : i + 1;
          return {
            raw: ch,
            id: String(ch.id || ""),
            title: String(ch.title || `第${num}集`),
            number: num,
            vip: ch.vip === true,
            vertical: typeof ch.vertical === "boolean" ? ch.vertical : null
          };
        });
        this.detailDrama = freshDrama;
        this.detailEpisodes = eps;
        this.detailWarning = res.warning || "";
        this.detailLoading = false;

        const watch = this._watchEntryFor(freshDrama.id);
        const resumeEp = watch ? Number(watch.episode) || 1 : 1;
        const epIdx = Math.max(0, eps.findIndex((e) => e.number === resumeEp));
        this.detailChunkIndex = Math.floor(epIdx / 30);
        this._renderDetailOverlay();

        const pbPrefs = (this.state && this.state.playback) || {};
        const qPref = Number(pbPrefs.quality) >= 0 ? Number(pbPrefs.quality) : 0;
        const shouldPreopen = pbPrefs.preopen != null ? !!pbPrefs.preopen : this.effectiveLayout() !== "television";
        if (shouldPreopen && eps[epIdx]) {
          const preloadKey = `${freshDrama.id}:${eps[epIdx].number}:${qPref}`;
          API.rpc("preload", {
            drama: freshDrama,
            chapter: eps[epIdx].raw,
            index: eps[epIdx].number,
            quality: qPref
          })
            .then((plan) => {
              if (!plan) return;
              if (epoch === this.detailEpoch && this.detailOpen && !Player.active) {
                this._releaseDetailPreload();
                this.detailPreloaded = { key: preloadKey, plan };
              } else if (plan.session) {
                API.rpc("release", { session: plan.session }).catch(() => {});
              }
            })
            .catch(() => {});
        }
      } catch (err) {
        if (epoch !== this.detailEpoch || !this.detailOpen) return;
        this.detailLoading = false;
        const dlSet = this._downloadedEpisodeSet(base.id);
        if (dlSet.size > 0) {
          this.detailEpisodes = [...dlSet]
            .sort((a, b) => a - b)
            .map((num) => ({
              raw: { title: `第${num}集`, currentEpisode: num },
              id: `local-${num}`,
              title: `第${num}集 (离线)`,
              number: num,
              vip: false,
              vertical: null
            }));
          this.detailWarning = `在线详情读取失败 (${err.message})，已展示本地已下载剧集`;
        } else {
          this.detailError = err.message || "获取剧集详情失败";
        }
        this._renderDetailOverlay();
      }
    }

    closeDetail() {
      this.detailEpoch++;
      this._releaseDetailPreload();
      this.detailOpen = false;
      const root = document.getElementById("detail-root");
      root.classList.add("hidden");
      root.innerHTML = "";
    }

    _renderDetailOverlay() {
      const root = document.getElementById("detail-root");
      if (!this.detailOpen || !this.detailDrama) {
        root.classList.add("hidden");
        root.innerHTML = "";
        return;
      }
      root.classList.remove("hidden");
      const d = this.detailDrama;
      const cover = coverURL(d, false);
      const resolvedCover = resolvedCoverURL(d);
      const followed = this._isFollowed(d.id);
      const watch = this._watchEntryFor(d.id);
      const resumeEp = watch ? Number(watch.episode) || 1 : 1;
      const resumePos = watch ? Number(watch.position) || 0 : 0;
      const dlSet = this._downloadedEpisodeSet(d.id);
      const srcName = this._sourceObj(d.source).name;
      const statusText = releaseLabel(d.releaseStatus);

      const tags = [];
      if (srcName) tags.push(srcName);
      if (d.category) tags.push(d.category);
      if (statusText) tags.push(statusText);
      if (d.episodes > 0) tags.push(`共 ${d.episodes} 集`);
      if (d.heat) tags.push(`热度 ${d.heat}`);
      if (d.views) tags.push(`${d.views} 播放`);
      if (d.onlineDate) tags.push(d.onlineDate);
      for (const t of d.tags || []) {
        if (!tags.includes(t)) tags.push(t);
      }

      const totalChunks = Math.max(1, Math.ceil(this.detailEpisodes.length / 30));
      const chunkIdx = Math.min(totalChunks - 1, Math.max(0, this.detailChunkIndex));
      const chunkEps = this.detailEpisodes.slice(chunkIdx * 30, (chunkIdx + 1) * 30);

      let chunkTabsHtml = "";
      if (totalChunks > 1) {
        chunkTabsHtml = `<div class="chip-row" style="margin-bottom:14px;">`;
        for (let i = 0; i < totalChunks; i++) {
          const startEp = this.detailEpisodes[i * 30].number;
          const endEp = this.detailEpisodes[Math.min(this.detailEpisodes.length - 1, (i + 1) * 30 - 1)].number;
          chunkTabsHtml += `<button class="chip ${i === chunkIdx ? "active" : ""}" data-chunk="${i}" data-focusable>${startEp}-${endEp}集</button>`;
        }
        chunkTabsHtml += `</div>`;
      }

      root.innerHTML = `
        <div class="detail-page">
          <div class="detail-scroll">
            <div class="detail-hero">
              <div class="detail-hero-bg" style="background-image:url('${esc(resolvedCover)}');" data-cover-bg="${esc(cover)}" data-drama-id="${esc(d.id)}" data-cover-priority="1"></div>
              <div class="detail-hero-inner">
                <div class="detail-top-nav">
                  <button class="btn" id="detail-back-btn" data-focusable>← 返回列表</button>
                  <button class="btn btn-sm" id="detail-refresh-btn" data-focusable>刷新详情</button>
                </div>
                <div class="detail-main">
                  <img class="detail-poster" src="${esc(resolvedCover)}" data-cover-url="${esc(cover)}" data-drama-id="${esc(d.id)}" data-cover-priority="1" alt="" />
                  <div class="detail-info">
                    <h1 class="detail-title">${esc(d.title)}</h1>
                    <div class="detail-tags">
                      ${tags.slice(0, 10).map((t) => `<span class="detail-tag">${esc(t)}</span>`).join("")}
                    </div>
                    <div class="detail-desc">${esc(d.description || "暂无剧情简介")}</div>
                    <div class="detail-actions">
                      <button class="btn btn-primary" id="detail-play-btn" ${this.detailEpisodes.length === 0 ? "disabled" : ""} data-focusable>
                        ▶ ${watch ? `继续观看 第${resumeEp}集` : "立即播放"}
                      </button>
                      <button class="btn" id="detail-follow-btn" data-focusable>
                        ${followed ? "★ 已追剧" : "☆ 追剧"}
                      </button>
                      ${
                        this.state.canDownload && this.detailEpisodes.length > 0
                          ? `<button class="btn" id="detail-dl-btn" data-focusable>⬇ 下载选集</button>`
                          : ""
                      }
                    </div>
                  </div>
                </div>
              </div>
            </div>

            <div class="detail-episodes-wrap">
              ${this.detailWarning ? `<div class="warning-banner"><span>${esc(this.detailWarning)}</span></div>` : ""}
              ${
                this.detailLoading
                  ? `<div class="empty-state"><div class="boot-spinner"></div><div>正在获取剧集目录...</div></div>`
                  : this.detailError
                  ? `<div class="empty-state">
                       <div>${esc(this.detailError)}</div>
                       <button class="btn btn-primary" id="detail-err-retry">重试</button>
                     </div>`
                  : `
                    <div class="section-header">
                      <h2 class="section-title">选集播放 (${this.detailEpisodes.length})</h2>
                      ${watch ? `<span style="font-size:12px;color:var(--text-muted);">上次看到第 ${resumeEp} 集 ${formatTime(resumePos)}</span>` : ""}
                    </div>
                    ${chunkTabsHtml}
                    <div class="episode-grid">
                      ${chunkEps
                        .map(
                          (ep) => `
                        <button class="ep-btn ${ep.number === resumeEp && watch ? "current" : ""}" data-ep-num="${ep.number}" data-focusable>
                          <span>第${ep.number}集</span>
                          ${ep.title && ep.title !== `第${ep.number}集` ? `<span class="ep-btn-sub">${esc(ep.title)}</span>` : ""}
                          ${
                            dlSet.has(ep.number)
                              ? `<span class="ep-corner local">已下</span>`
                              : ep.vip
                              ? `<span class="ep-corner vip">VIP</span>`
                              : ""
                          }
                        </button>
                      `
                        )
                        .join("")}
                    </div>
                  `
              }
            </div>
          </div>
        </div>
      `;

      root.querySelector("#detail-back-btn").onclick = () => this.closeDetail();
      root.querySelector("#detail-refresh-btn").onclick = () => {
        if (this.detailDrama) {
          CoverHydrator.forceRefresh(coverURL(this.detailDrama, false), this.detailDrama.id);
        }
        this.openDetail(this.detailDrama);
      };
      const retryBtn = root.querySelector("#detail-err-retry");
      if (retryBtn) retryBtn.onclick = () => this.openDetail(this.detailDrama);

      const playBtn = root.querySelector("#detail-play-btn");
      if (playBtn) {
        playBtn.onclick = () => {
          const targetEp = this.detailEpisodes.some((e) => e.number === resumeEp) ? resumeEp : (this.detailEpisodes[0] && this.detailEpisodes[0].number) || 1;
          this._startPlayback(this.detailDrama, this.detailEpisodes, targetEp, targetEp === resumeEp ? resumePos : 0, false);
        };
      }

      root.querySelector("#detail-follow-btn").onclick = async () => {
        try {
          const res = await API.toggleFollow(this.detailDrama, !followed);
          if (res.state) {
            this.state = res.state;
            toast(!followed ? "已加入追剧列表" : "已取消追剧");
            this._renderDetailOverlay();
            this.render();
          }
        } catch (err) {
          toast(err.message);
        }
      };

      const dlBtn = root.querySelector("#detail-dl-btn");
      if (dlBtn) {
        dlBtn.onclick = () => this._openDownloadPickerModal(this.detailDrama, this.detailEpisodes);
      }

      root.querySelectorAll("[data-chunk]").forEach((b) => {
        b.onclick = () => {
          this.detailChunkIndex = Number(b.getAttribute("data-chunk")) || 0;
          this._renderDetailOverlay();
        };
      });

      root.querySelectorAll("[data-ep-num]").forEach((b) => {
        b.onclick = () => {
          const num = Number(b.getAttribute("data-ep-num")) || 1;
          const pos = watch && Number(watch.episode) === num ? resumePos : 0;
          this._startPlayback(this.detailDrama, this.detailEpisodes, num, pos, false);
        };
      });
      CoverHydrator.scan(root);
    }

    _startPlayback(drama, episodes, epNum, startPos, forceLocal) {
      const pbPrefs = (this.state && this.state.playback) || {};
      const qPref = Number(pbPrefs.quality) >= 0 ? Number(pbPrefs.quality) : 0;
      const targetKey = `${drama.id}:${epNum}:${qPref}`;
      let initialPlan = null;
      if (!forceLocal && this.detailPreloaded && this.detailPreloaded.key === targetKey) {
        initialPlan = this.detailPreloaded.plan;
        this.detailPreloaded = null;
      } else {
        this._releaseDetailPreload();
      }
      Player.open({
        drama,
        episodes,
        episodeNumber: epNum,
        startPosition: startPos,
        forceLocal: !!forceLocal,
        initialPlan,
        onStateUpdate: (nextState) => {
          this.state = nextState;
          if (this.detailOpen) this._renderDetailOverlay();
          this.render();
        },
        onEpisodeChange: () => {
          if (this.detailOpen) this._renderDetailOverlay();
        }
      });
    }

    _openDownloadPickerModal(drama, episodes) {
      const dlSet = this._downloadedEpisodeSet(drama.id);
      const selected = new Set();
      let rangeStart = null;
      let rangeMode = false;
      const dlPrefs = (this.state && this.state.downloadPreferences) || {};
      let chosenQuality = Number(dlPrefs.quality) || 0;

      const modal = document.getElementById("modal-root");
      modal.classList.remove("hidden");

      const draw = () => {
        modal.innerHTML = `
          <div class="modal-card wide">
            <div class="modal-header">
              <h3 class="modal-title">下载选集 · ${esc(drama.title)}</h3>
              <button class="icon-btn" id="dlp-close">✕</button>
            </div>
            <div class="modal-body">
              <div style="display:flex;align-items:center;justify-content:space-between;gap:10px;flex-wrap:wrap;">
                <div style="display:flex;gap:8px;flex-wrap:wrap;">
                  <button class="btn btn-sm ${rangeMode ? "btn-primary" : ""}" id="dlp-range">${rangeMode ? "连选模式：点起止集" : "区间连选"}</button>
                  <button class="btn btn-sm" id="dlp-all">全选未下</button>
                  <button class="btn btn-sm" id="dlp-free">仅选免费集</button>
                  <button class="btn btn-sm" id="dlp-clear">清空选择</button>
                </div>
                <select class="text-input" id="dlp-quality" style="flex:0 0 auto;width:auto;padding:6px 12px;">
                  ${[0, 1080, 720, 540, 480].map((q) => `<option value="${q}" ${q === chosenQuality ? "selected" : ""}>画质：${qualityLabel(q)}</option>`).join("")}
                </select>
              </div>
              <div class="episode-grid" style="max-height:360px;overflow-y:auto;padding:4px 2px;">
                ${episodes
                  .map((ep) => {
                    const done = dlSet.has(ep.number);
                    const sel = selected.has(ep.number);
                    return `
                      <button class="ep-btn ${sel ? "current" : ""}" data-dl-ep="${ep.number}" ${done ? "disabled" : ""}>
                        <span>第${ep.number}集</span>
                        ${done ? `<span class="ep-corner local">已下</span>` : ep.vip ? `<span class="ep-corner vip">VIP</span>` : ""}
                      </button>
                    `;
                  })
                  .join("")}
              </div>
            </div>
            <div class="modal-footer">
              <span style="margin-right:auto;align-self:center;font-size:13px;color:var(--text-secondary);">已选 ${selected.size} 集</span>
              <button class="btn" id="dlp-cancel">取消</button>
              <button class="btn btn-primary" id="dlp-submit" ${selected.size === 0 ? "disabled" : ""}>加入下载队列</button>
            </div>
          </div>
        `;

        modal.querySelector("#dlp-close").onclick = () => this._closeModal();
        modal.querySelector("#dlp-cancel").onclick = () => this._closeModal();
        modal.querySelector("#dlp-quality").onchange = (e) => {
          chosenQuality = Number(e.target.value) || 0;
        };
        modal.querySelector("#dlp-range").onclick = () => {
          rangeMode = !rangeMode;
          rangeStart = null;
          draw();
        };
        modal.querySelector("#dlp-all").onclick = () => {
          episodes.forEach((ep) => {
            if (!dlSet.has(ep.number)) selected.add(ep.number);
          });
          draw();
        };
        modal.querySelector("#dlp-free").onclick = () => {
          selected.clear();
          episodes.forEach((ep) => {
            if (!dlSet.has(ep.number) && !ep.vip) selected.add(ep.number);
          });
          draw();
        };
        modal.querySelector("#dlp-clear").onclick = () => {
          selected.clear();
          draw();
        };
        modal.querySelectorAll("[data-dl-ep]").forEach((btn) => {
          btn.onclick = () => {
            const num = Number(btn.getAttribute("data-dl-ep"));
            if (rangeMode) {
              if (rangeStart == null) {
                rangeStart = num;
                selected.add(num);
              } else {
                const lo = Math.min(rangeStart, num);
                const hi = Math.max(rangeStart, num);
                episodes.forEach((ep) => {
                  if (ep.number >= lo && ep.number <= hi && !dlSet.has(ep.number)) {
                    selected.add(ep.number);
                  }
                });
                rangeStart = null;
              }
            } else {
              if (selected.has(num)) selected.delete(num);
              else selected.add(num);
            }
            draw();
          };
        });

        modal.querySelector("#dlp-submit").onclick = async () => {
          const chosenEps = episodes.filter((ep) => selected.has(ep.number));
          if (chosenEps.length === 0) return;
          try {
            API.updateUserSettings({ downloadPreferences: { quality: chosenQuality } }).catch(() => {});
            const res = await API.rpc("enqueueDownloads", {
              drama,
              quality: chosenQuality,
              entries: chosenEps.map((e) => ({ chapter: e.raw, index: e.number }))
            });
            this._closeModal();
            toast(`已添加 ${res.added || chosenEps.length} 集离线下载任务`);
            await this._refreshDownloadsOnce();
          } catch (err) {
            toast(err.message || "添加下载失败");
          }
        };
      };

      draw();
    }

    async _refreshDownloadsOnce() {
      if (!this.state || this.state.locked || !this.state.canDownload) return;
      try {
        const res = await API.rpc("downloads");
        this.downloadJobs = Array.isArray(res.jobs) ? res.jobs : [];
        const hasRunning = this.downloadJobs.some((j) =>
          ["queued", "downloading", "converting", "removing"].includes(j.state)
        );
        if (hasRunning || this.view === "downloads") {
          this._startDownloadsPolling();
        } else {
          this._stopDownloadsPolling();
        }
        if (this.view === "downloads") this.render();
        if (this.detailOpen) this._renderDetailOverlay();
      } catch (_) {}
    }

    _startDownloadsPolling() {
      if (this.downloadPollTimer) return;
      this.downloadPollTimer = setInterval(() => {
        if (!this.state || this.state.locked || !this.state.canDownload) {
          this._stopDownloadsPolling();
          return;
        }
        this._refreshDownloadsOnce();
      }, 1600);
    }

    _stopDownloadsPolling() {
      if (this.downloadPollTimer) {
        clearInterval(this.downloadPollTimer);
        this.downloadPollTimer = null;
      }
    }

    _openSourceSwitcherModal() {
      const sources = (this.state && this.state.sources) || [];
      if (sources.length === 0) {
        toast("当前没有已启用的站源");
        return;
      }
      const groups = new Map();
      for (const s of sources) {
        const gid = s.groupId || s.id;
        if (!groups.has(gid)) groups.set(gid, []);
        groups.get(gid).push(s);
      }
      const modal = document.getElementById("modal-root");
      modal.classList.remove("hidden");
      modal.innerHTML = `
        <div class="modal-card">
          <div class="modal-header">
            <h3 class="modal-title">切换内容站源</h3>
            <button class="icon-btn" id="src-modal-close">✕</button>
          </div>
          <div class="modal-body">
            ${[...groups.values()]
              .map((items) =>
                items
                  .map(
                    (s) => `
                  <button class="list-card" data-pick-src="${esc(s.id)}" style="width:100%;text-align:left;justify-content:space-between;border-color:${s.id === this.state.source ? "var(--accent)" : "var(--border)"};">
                    <div>
                      <div style="font-weight:700;">${esc(s.name)}</div>
                      <div style="font-size:12px;color:var(--text-muted);">${esc(s.description || "")}</div>
                    </div>
                    ${s.id === this.state.source ? `<span class="brand-tag">当前</span>` : ""}
                  </button>
                `
                  )
                  .join("")
              )
              .join("")}
          </div>
        </div>
      `;
      modal.querySelector("#src-modal-close").onclick = () => this._closeModal();
      modal.querySelectorAll("[data-pick-src]").forEach((btn) => {
        btn.onclick = async () => {
          const target = btn.getAttribute("data-pick-src");
          this._closeModal();
          if (target === this.state.source) return;
          try {
            const res = await API.updateUserSettings({ source: target });
            if (res.state) {
              this.setState(res.state);
              if (target === "hongguo" && !this.hongguoReady) {
                this._openHongguoDeviceModal();
              }
            }
          } catch (err) {
            toast(err.message);
          }
        };
      });
    }

    _openUserSwitcherModal() {
      const profiles = (this.state && this.state.profiles) || [];
      const curId = this.state && this.state.profile ? this.state.profile.id : "";
      const modal = document.getElementById("modal-root");
      modal.classList.remove("hidden");
      modal.innerHTML = `
        <div class="modal-card">
          <div class="modal-header">
            <h3 class="modal-title">切换用户空间</h3>
            <button class="icon-btn" id="usr-modal-close">✕</button>
          </div>
          <div class="modal-body">
            ${profiles
              .map(
                (p) => `
              <button class="list-card" data-switch-prof="${esc(p.id)}" data-prof-prot="${p.protected ? "1" : "0"}" style="width:100%;text-align:left;justify-content:space-between;border-color:${p.id === curId ? "var(--accent)" : "var(--border)"};">
                <div style="display:flex;align-items:center;gap:12px;">
                  <div class="user-avatar">${esc(String(p.name || "U").slice(0, 1))}</div>
                  <div>
                    <div style="font-weight:700;">${esc(p.name)} ${p.protected ? "🔒" : ""}</div>
                    <div style="font-size:12px;color:var(--text-muted);">${p.admin ? "管理员" : "普通成员"}${p.download || p.admin ? " · 可下载" : " · 仅在线"}</div>
                  </div>
                </div>
                ${p.id === curId ? `<span class="brand-tag">当前</span>` : `<span class="btn btn-sm">切换</span>`}
              </button>
            `
              )
              .join("")}
            <div id="usr-pin-slot"></div>
          </div>
          <div class="modal-footer">
            ${
              this.state.profile && this.state.profile.protected
                ? `<button class="btn btn-danger" id="usr-lock-now">立即锁定当前用户</button>`
                : ""
            }
            <button class="btn" id="usr-manage-btn">用户权限设置</button>
          </div>
        </div>
      `;
      modal.querySelector("#usr-modal-close").onclick = () => this._closeModal();
      const lockBtn = modal.querySelector("#usr-lock-now");
      if (lockBtn) {
        lockBtn.onclick = async () => {
          this._closeModal();
          const res = await API.lockProfile();
          if (res.state) this.setState(res.state);
        };
      }
      modal.querySelector("#usr-manage-btn").onclick = () => {
        this._closeModal();
        this.view = "settings";
        this.settingsTab = "users";
        this.render();
      };
      modal.querySelectorAll("[data-switch-prof]").forEach((btn) => {
        btn.onclick = async () => {
          const id = btn.getAttribute("data-switch-prof");
          const prot = btn.getAttribute("data-prof-prot") === "1";
          if (!prot) {
            try {
              const res = await API.switchProfile(id, "");
              this._closeModal();
              if (res.state) this.setState(res.state);
            } catch (err) {
              toast(err.message);
            }
            return;
          }
          const slot = modal.querySelector("#usr-pin-slot");
          slot.innerHTML = `
            <div style="display:flex;gap:8px;margin-top:8px;">
              <input type="password" class="text-input" id="usr-pin-input" placeholder="输入该用户的访问密码" autocomplete="off" />
              <button class="btn btn-primary" id="usr-pin-go">验证切换</button>
            </div>
          `;
          const inp = slot.querySelector("#usr-pin-input");
          inp.focus();
          const submit = async () => {
            try {
              const res = await API.switchProfile(id, inp.value);
              this._closeModal();
              if (res.state) this.setState(res.state);
            } catch (err) {
              toast(err.message);
            }
          };
          slot.querySelector("#usr-pin-go").onclick = submit;
          inp.onkeydown = (ev) => {
            if (ev.key === "Enter") submit();
          };
        };
      });
    }

    _handleGlobalKeys(e) {
      if (Player.active) return;
      if (e.key === "Escape") {
        const modal = document.getElementById("modal-root");
        if (!modal.classList.contains("hidden")) {
          this._closeModal();
          return;
        }
        if (this.detailOpen) {
          this.closeDetail();
          return;
        }
      }
      if (this.effectiveLayout() === "television" && ["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight"].includes(e.key)) {
        const tag = (e.target && e.target.tagName) || "";
        if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;
        this._moveSpatialFocus(e);
      }
    }

    _moveSpatialFocus(e) {
      const scope = !document.getElementById("modal-root").classList.contains("hidden")
        ? document.getElementById("modal-root")
        : this.detailOpen
        ? document.getElementById("detail-root")
        : document.getElementById("app");
      const items = [...scope.querySelectorAll("[data-focusable]:not(:disabled)")].filter((el) => el.offsetParent !== null);
      if (items.length === 0) return;
      const cur = document.activeElement;
      if (!cur || !items.includes(cur)) {
        e.preventDefault();
        items[0].focus();
        return;
      }
      const r0 = cur.getBoundingClientRect();
      const cx0 = r0.left + r0.width / 2;
      const cy0 = r0.top + r0.height / 2;
      let best = null;
      let bestScore = Infinity;

      for (const cand of items) {
        if (cand === cur) continue;
        const r = cand.getBoundingClientRect();
        const cx = r.left + r.width / 2;
        const cy = r.top + r.height / 2;
        const dx = cx - cx0;
        const dy = cy - cy0;

        if (e.key === "ArrowRight" && dx <= 6) continue;
        if (e.key === "ArrowLeft" && dx >= -6) continue;
        if (e.key === "ArrowDown" && dy <= 6) continue;
        if (e.key === "ArrowUp" && dy >= -6) continue;

        const primary = e.key === "ArrowLeft" || e.key === "ArrowRight" ? Math.abs(dx) : Math.abs(dy);
        const cross = e.key === "ArrowLeft" || e.key === "ArrowRight" ? Math.abs(dy) : Math.abs(dx);
        const score = primary + cross * 2.6;
        if (score < bestScore) {
          bestScore = score;
          best = cand;
        }
      }

      if (best) {
        e.preventDefault();
        best.focus();
        best.scrollIntoView({ block: "nearest", behavior: "smooth" });
      }
    }

    navigate(targetView) {
      this.view = targetView;
      if (targetView === "downloads" && this.state && this.state.canDownload) {
        this._refreshDownloadsOnce();
      }
      if (targetView === "settings") {
        this._loadSettingsAuxiliary();
      }
      this.render();
    }

    async _loadSettingsAuxiliary() {
      if (!this.state || this.state.locked) return;
      if (this.state.profile && this.state.profile.admin) {
        API.rpc("resourceSettings")
          .then((r) => {
            this.resourceSettings = r;
            if (this.view === "settings") this.render();
          })
          .catch(() => {});
      }
      if (this.state.canDownload) {
        API.rpc("storage")
          .then((s) => {
            this.storageInfo = s;
            if (this.view === "settings") this.render();
          })
          .catch(() => {});
      }
      if (this.settingsTab === "diagnostics") {
        API.getLogs()
          .then((l) => {
            this.logLines = l.lines || [];
            if (this.view === "settings" && this.settingsTab === "diagnostics") this.render();
          })
          .catch(() => {});
      }
    }

    render() {
      const appRoot = document.getElementById("app");
      if (!this.state) return;

      if (this.state.locked) {
        this._renderLockScreen(appRoot);
        return;
      }

      const layout = this.effectiveLayout();
      const updCount = this.followUpdateCount();
      const activeJobsCount = this.downloadJobs.filter((j) =>
        ["queued", "downloading", "converting"].includes(j.state)
      ).length;
      const curSrc = this._sourceObj(this.state.source);
      const prof = this.state.profile || { name: "管理员", admin: true };

      const navItems = [
        { id: "explore", icon: "🧭", label: "发现", badge: 0 },
        { id: "search", icon: "🔍", label: "搜索", badge: 0 },
        { id: "follows", icon: "⭐", label: "追剧", badge: updCount },
        { id: "history", icon: "🕒", label: "观看记录", badge: 0 },
        ...(this.state.canDownload ? [{ id: "downloads", icon: "⬇", label: "离线下载", badge: activeJobsCount }] : []),
        { id: "settings", icon: "⚙", label: "设置", badge: 0 }
      ];

      let sidebarHtml = "";
      if (layout === "pad" || layout === "window") {
        sidebarHtml = `
          <aside class="sidebar ${this.sidebarCollapsed ? "collapsed" : ""}">
            <div class="brand-bar">
              <img src="/icon.svg" alt="" class="brand-icon" />
              <span class="brand-title">${esc(this.state.appName || "真果鉴")}</span>
              <span class="brand-tag">Web</span>
            </div>
            ${navItems
              .map(
                (n) => `
              <button class="nav-item ${this.view === n.id ? "active" : ""}" data-nav="${n.id}" title="${esc(n.label)}">
                <span class="nav-icon">${n.icon}</span>
                <span class="nav-label">${esc(n.label)}</span>
                ${n.badge > 0 ? `<span class="nav-badge">${n.badge}</span>` : ""}
              </button>
            `
              )
              .join("")}
            <div class="sidebar-spacer"></div>
            <div class="sidebar-footer">
              <button class="user-pill" id="sidebar-user-btn" title="切换用户">
                <span class="user-avatar">${esc(String(prof.name || "U").slice(0, 1))}</span>
                <div class="sidebar-user-meta">
                  <div class="sidebar-user-name">${esc(prof.name)}</div>
                  <div class="sidebar-user-role">${prof.admin ? "管理员" : "普通用户"}</div>
                </div>
              </button>
              <button class="nav-item" id="sidebar-collapse-btn" title="折叠导航栏">
                <span class="nav-icon">${this.sidebarCollapsed ? "»" : "«"}</span>
                <span class="nav-label">收起侧栏</span>
              </button>
            </div>
          </aside>
        `;
      }

      let topbarHtml = "";
      if (layout === "television") {
        topbarHtml = `
          <header class="topbar">
            <img src="/icon.svg" alt="" class="brand-icon" />
            <span class="brand-title">${esc(this.state.appName || "真果鉴")}</span>
            <button class="source-btn" id="top-source-btn" data-focusable>站源：${esc(curSrc.name)} ▾</button>
            <nav class="tv-nav-tabs">
              ${navItems
                .map(
                  (n) => `
                <button class="tv-nav-tab ${this.view === n.id ? "active" : ""}" data-nav="${n.id}" data-focusable>
                  ${esc(n.label)}${n.badge > 0 ? ` (${n.badge})` : ""}
                </button>
              `
                )
                .join("")}
            </nav>
            <div class="topbar-actions">
              <button class="btn btn-sm" id="top-user-btn" data-focusable>👤 ${esc(prof.name)}</button>
            </div>
          </header>
        `;
      } else {
        topbarHtml = `
          <header class="topbar">
            ${layout === "phone" ? `<img src="/icon.svg" alt="" style="width:26px;height:26px;border-radius:7px;flex-shrink:0;" />` : ""}
            <button class="source-btn" id="top-source-btn">${esc(curSrc.name)} ▾</button>
            ${
              this.view !== "search"
                ? `<button class="topbar-search-trigger" id="top-search-jump">
                     <span style="flex-shrink:0;">🔍</span>
                     <span class="topbar-search-label">${layout === "phone" ? `搜 ${esc(curSrc.name)}...` : `搜索 ${esc(curSrc.name)} 剧集或首字母...`}</span>
                   </button>`
                : `<div style="flex:1;min-width:0;font-weight:700;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">搜索 · ${esc(curSrc.name)}</div>`
            }
            <div class="topbar-actions">
              ${layout === "phone" ? `<button class="icon-btn" data-nav="history" title="观看记录">🕒</button>` : ""}
              <button class="icon-btn" id="top-refresh-btn" title="刷新当前页">↻</button>
              ${layout === "phone" ? `<button class="icon-btn" id="top-user-btn" title="切换用户">👤</button>` : ""}
            </div>
          </header>
        `;
      }

      let bottomNavHtml = "";
      if (layout === "phone") {
        const phoneNav = [
          { id: "explore", icon: "🧭", label: "发现", badge: 0 },
          { id: "follows", icon: "⭐", label: "追剧", badge: updCount },
          ...(this.state.canDownload ? [{ id: "downloads", icon: "⬇", label: "下载", badge: activeJobsCount }] : []),
          { id: "settings", icon: "⚙", label: "我的", badge: 0 }
        ];
        bottomNavHtml = `
          <nav class="bottom-nav">
            ${phoneNav
              .map(
                (n) => `
              <button class="bottom-nav-item ${this.view === n.id ? "active" : ""}" data-nav="${n.id}">
                <span style="font-size:17px;">${n.icon}</span>
                <span>${esc(n.label)}</span>
                ${n.badge > 0 ? `<span class="bottom-nav-dot">${n.badge}</span>` : ""}
              </button>
            `
              )
              .join("")}
          </nav>
        `;
      }

      appRoot.innerHTML = `
        <div class="shell">
          ${sidebarHtml}
          <div class="main-column">
            ${topbarHtml}
            <main class="page-scroll" id="page-scroll">${this._renderCurrentView()}</main>
            ${bottomNavHtml}
          </div>
        </div>
      `;

      this._bindShellEvents(appRoot);
      this._bindViewEvents(appRoot.querySelector("#page-scroll"));
      CoverHydrator.scan(appRoot);
    }

    _renderLockScreen(appRoot) {
      const profiles = this.state.profiles || [];
      const cur = this.state.profile || profiles[0] || { id: "default", name: "管理员" };
      appRoot.innerHTML = `
        <div style="width:100%;height:100%;display:flex;padding:18px;">
          <div class="lock-screen">
            <img src="/icon.svg" alt="" style="width:60px;height:60px;margin:0 auto;border-radius:15px;" />
            <div style="font-size:20px;font-weight:800;">${esc(this.state.appName || "真果鉴")} 已锁定</div>
            <div style="font-size:13px;color:var(--text-secondary);">选择用户并输入访问密码以进入个人剧库</div>
            <select class="text-input" id="lock-user-sel">
              ${profiles.map((p) => `<option value="${esc(p.id)}" ${p.id === cur.id ? "selected" : ""}>${esc(p.name)}${p.protected ? " (需密码)" : ""}</option>`).join("")}
            </select>
            <input type="password" class="text-input" id="lock-pin-input" placeholder="输入访问密码（无密码可直接进入）" autocomplete="off" />
            <button class="btn btn-primary" id="lock-submit-btn" style="width:100%;padding:11px;">解锁进入</button>
          </div>
        </div>
      `;
      const sel = appRoot.querySelector("#lock-user-sel");
      const inp = appRoot.querySelector("#lock-pin-input");
      inp.focus();
      const unlock = async () => {
        try {
          const res = await API.switchProfile(sel.value, inp.value);
          if (res.state) {
            this.setState(res.state);
            if (!res.state.locked) await this._afterUnlockedInit();
          }
        } catch (err) {
          toast(err.message || "密码验证失败");
        }
      };
      appRoot.querySelector("#lock-submit-btn").onclick = unlock;
      inp.onkeydown = (e) => {
        if (e.key === "Enter") unlock();
      };
    }

    _bindShellEvents(appRoot) {
      appRoot.querySelectorAll("[data-nav]").forEach((btn) => {
        btn.onclick = () => this.navigate(btn.getAttribute("data-nav"));
      });
      const srcBtn = appRoot.querySelector("#top-source-btn");
      if (srcBtn) srcBtn.onclick = () => this._openSourceSwitcherModal();

      const searchJump = appRoot.querySelector("#top-search-jump");
      if (searchJump) {
        searchJump.onclick = () => this.navigate("search");
      }

      const refreshBtn = appRoot.querySelector("#top-refresh-btn");
      if (refreshBtn) {
        refreshBtn.onclick = () => {
          if (this.view === "explore") {
            this._loadExploreCategories(true);
            this._reloadExplore(true);
          } else if (this.view === "search" && this.searchQuery) {
            this.runSearch(this.searchQuery, 1);
          } else if (this.view === "downloads") {
            this._refreshDownloadsOnce();
          } else {
            API.getState().then((r) => r.state && this.setState(r.state)).catch(() => {});
          }
        };
      }

      const usrBtn = appRoot.querySelector("#sidebar-user-btn") || appRoot.querySelector("#top-user-btn");
      if (usrBtn) usrBtn.onclick = () => this._openUserSwitcherModal();
      const topUsr = appRoot.querySelector("#top-user-btn");
      if (topUsr) topUsr.onclick = () => this._openUserSwitcherModal();

      const colBtn = appRoot.querySelector("#sidebar-collapse-btn");
      if (colBtn) {
        colBtn.onclick = () => {
          this.sidebarCollapsed = !this.sidebarCollapsed;
          localStorage.setItem("zgj_sidebar_collapsed", this.sidebarCollapsed ? "1" : "0");
          this.render();
        };
      }
    }

    _renderDramaCard(drama, opts) {
      const o = opts || {};
      const cover = coverURL(drama, false);
      const resolvedCover = resolvedCoverURL(drama);
      const rank = o.rank || 0;
      const showSource = !!o.showSource;
      const hasUpdate = !!o.hasUpdate;
      const watch = o.watch || this._watchEntryFor(drama.id);
      const selectable = !!o.selectable;
      const selected = !!o.selected;
      const status = releaseLabel(drama.releaseStatus);

      let progressPct = 0;
      if (watch && Number(watch.duration) > 1) {
        progressPct = Math.min(100, Math.max(3, Math.round((Number(watch.position) / Number(watch.duration)) * 100)));
      }

      const subParts = [];
      if (watch) {
        subParts.push(`看到第${watch.episode}集`);
      } else if (drama.category) {
        subParts.push(drama.category);
      }
      if (drama.heat) subParts.push(`🔥 ${drama.heat}`);
      else if (status) subParts.push(status);

      return `
        <button class="drama-card ${selected ? "selected" : ""}" data-card-id="${esc(drama.id)}" data-focusable>
          <div class="poster-wrap">
            <img class="poster-img" decoding="async" src="${esc(resolvedCover)}" data-cover-url="${esc(cover)}" data-drama-id="${esc(drama.id)}" alt="" />
            <div class="badge-top-left">
              ${rank > 0 ? `<span class="pill-badge pill-rank ${rank <= 3 ? `rank-${rank}` : ""}">TOP ${rank}</span>` : ""}
              ${hasUpdate ? `<span class="pill-badge pill-update">有更新</span>` : ""}
              ${showSource ? `<span class="pill-badge pill-source">${esc(this._sourceObj(drama.source).name)}</span>` : ""}
            </div>
            <div class="badge-top-right">
              ${drama.vip ? `<span class="pill-badge pill-vip">VIP</span>` : ""}
            </div>
            ${selectable ? `<div class="card-check">${selected ? "✓" : ""}</div>` : ""}
            <div class="badge-bottom">
              <span>${drama.episodes > 0 ? `${drama.episodes}集` : status || ""}</span>
              <span>${drama.views ? esc(drama.views) : ""}</span>
            </div>
            ${progressPct > 0 ? `<div class="card-progress-track"><div class="card-progress-bar" style="width:${progressPct}%;"></div></div>` : ""}
          </div>
          <div class="card-body">
            <div class="card-title">${esc(drama.title)}</div>
            <div class="card-sub">${esc(subParts.join(" · ") || "点击查看详情")}</div>
          </div>
        </button>
      `;
    }

    _renderCurrentView() {
      switch (this.view) {
        case "explore":
          return this._renderExploreView();
        case "search":
          return this._renderSearchView();
        case "follows":
          return this._renderFollowsView();
        case "history":
          return this._renderHistoryView();
        case "downloads":
          return this._renderDownloadsView();
        case "settings":
          return this._renderSettingsView();
        default:
          return this._renderExploreView();
      }
    }

    _renderExploreView() {
      if (!this.exploreSource) {
        return `<div class="empty-state"><div>当前用户未开启任何可用站源，请前往设置启用站源。</div></div>`;
      }
      const tabsHtml = `
        <div class="chip-row">
          ${this.exploreTabs
            .map(
              (t) => `<button class="chip ${t.id === this.exploreTabId ? "active" : ""}" data-exp-tab="${esc(t.id)}" data-focusable>${esc(t.name)}</button>`
            )
            .join("")}
        </div>
      `;

      const isFirstTab = this.exploreTabs.length > 0 && this.exploreTabId === this.exploreTabs[0].id;
      const resumeEntry = isFirstTab
        ? (this.state.history || []).find((h) => h.drama && h.drama.source === this.exploreSource)
        : null;
      const featuredDrama = !resumeEntry && this.exploreTabId === "app:recommend" && this.exploreItems.length > 0
        ? this.exploreItems[0]
        : null;
      const gridItems = featuredDrama ? this.exploreItems.slice(1) : this.exploreItems;

      let heroHtml = "";
      if (resumeEntry && resumeEntry.drama) {
        const rd = normalizeDrama(resumeEntry.drama);
        const cov = coverURL(rd, false);
        const resCov = resolvedCoverURL(rd);
        heroHtml = `
          <div class="hero-banner" data-hero-id="${esc(rd.id)}" tabindex="0" data-focusable>
            <div class="hero-backdrop" style="background-image:url('${esc(resCov)}');" data-cover-bg="${esc(cov)}" data-drama-id="${esc(rd.id)}" data-cover-priority="1"></div>
            <div class="hero-content">
              <img class="hero-poster" src="${esc(resCov)}" data-cover-url="${esc(cov)}" data-drama-id="${esc(rd.id)}" data-cover-priority="1" alt="" />
              <div class="hero-meta">
                <span class="hero-eyebrow">继续观看 · 第 ${resumeEntry.episode} 集 ${formatTime(resumeEntry.position)}</span>
                <h2 class="hero-title">${esc(rd.title)}</h2>
                <div class="hero-desc">${esc(rd.description || "上次观看记录，点击继续播放")}</div>
                <span class="btn btn-primary btn-sm">▶ 立即续播</span>
              </div>
            </div>
          </div>
        `;
      } else if (featuredDrama) {
        const cov = coverURL(featuredDrama, false);
        const resCov = resolvedCoverURL(featuredDrama);
        heroHtml = `
          <div class="hero-banner" data-hero-id="${esc(featuredDrama.id)}" tabindex="0" data-focusable>
            <div class="hero-backdrop" style="background-image:url('${esc(resCov)}');" data-cover-bg="${esc(cov)}" data-drama-id="${esc(featuredDrama.id)}" data-cover-priority="1"></div>
            <div class="hero-content">
              <img class="hero-poster" src="${esc(resCov)}" data-cover-url="${esc(cov)}" data-drama-id="${esc(featuredDrama.id)}" data-cover-priority="1" alt="" />
              <div class="hero-meta">
                <span class="hero-eyebrow">精选热播推荐</span>
                <h2 class="hero-title">${esc(featuredDrama.title)}</h2>
                <div class="hero-desc">${esc(featuredDrama.description || featuredDrama.category || "热门精选剧集")}</div>
                <span class="btn btn-primary btn-sm">▶ 查看剧集</span>
              </div>
            </div>
          </div>
        `;
      }

      return `
        ${tabsHtml}
        ${this.exploreWarning ? `<div class="warning-banner"><span>${esc(this.exploreWarning)}</span></div>` : ""}
        ${heroHtml}
        ${
          this.exploreError && gridItems.length === 0
            ? `<div class="empty-state">
                 <div>${esc(this.exploreError)}</div>
                 <button class="btn btn-primary" id="exp-retry-btn" data-focusable>重新加载</button>
               </div>`
            : gridItems.length === 0 && this.exploreLoading
            ? `<div class="empty-state"><div class="boot-spinner"></div><div>正在加载 ${esc(this._sourceObj(this.exploreSource).name)} 内容...</div></div>`
            : gridItems.length === 0
            ? `<div class="empty-state"><div>暂无可展示的剧集</div></div>`
            : `
              <div class="drama-grid">
                ${gridItems.map((d) => this._renderDramaCard(d, { rank: this.exploreRanks.get(d.id) || 0 })).join("")}
              </div>
              <div class="load-more-row" id="exp-sentinel">
                ${
                  this.exploreLoading
                    ? `<span class="inline-spinner"></span>`
                    : this.exploreHasMore
                    ? `<button class="btn" id="exp-more-btn" data-focusable>加载更多剧集</button>`
                    : `<span style="font-size:12px;color:var(--text-muted);">已显示全部 ${gridItems.length} 部剧集</span>`
                }
              </div>
            `
        }
      `;
    }

    _renderSearchView() {
      const showKeyboard = this.effectiveLayout() === "television" || !!(this.state && this.state.televisionKeyboard);
      const recent = ((this.state && this.state.recentSearches) || []).filter((q) => q && q !== "[object Object]");

      return `
        <form class="search-box-row" id="search-form" action="javascript:void(0)">
          <input type="search" class="text-input" id="search-input" value="${esc(this.searchQuery)}" placeholder="输入剧名、演员、题材或拼音首字母..." enterkeyhint="search" autocomplete="off" />
          <button type="submit" class="btn btn-primary" id="search-go-btn" data-focusable>搜索</button>
          ${this.searchQuery ? `<button type="button" class="btn" id="search-clear-btn" data-focusable>清空</button>` : ""}
        </form>
        <div id="search-suggestions-slot"></div>
        ${
          showKeyboard
            ? `<div class="tv-keyboard">
                 ${TV_KEYS.map((k) => `<button class="tv-key" data-tv-key="${k}" data-focusable>${k}</button>`).join("")}
                 <button class="tv-key" data-tv-key="BACK" style="grid-column:span 2;" data-focusable>⌫ 退格</button>
                 <button class="tv-key" data-tv-key="CLEAR" style="grid-column:span 2;" data-focusable>清空</button>
               </div>`
            : ""
        }
        ${
          !this.searchSubmitted && recent.length > 0
            ? `
              <div class="section-header">
                <span class="setting-label">最近搜索</span>
                <button class="btn btn-sm" id="clear-recent-btn" data-focusable>清除历史</button>
              </div>
              <div class="chip-row" style="flex-wrap:wrap;">
                ${recent.map((q) => `<button class="chip" data-recent-q="${esc(q)}" data-focusable>${esc(q)}</button>`).join("")}
              </div>
            `
            : ""
        }
        ${this.searchWarning ? `<div class="warning-banner"><span>${esc(this.searchWarning)}</span></div>` : ""}
        ${
          this.searchLoading && this.searchItems.length === 0
            ? `<div class="empty-state"><div class="boot-spinner"></div><div>正在搜索 "${esc(this.searchQuery)}"...</div></div>`
            : this.searchSubmitted && this.searchItems.length === 0
            ? `<div class="empty-state"><div>未找到与 "${esc(this.searchQuery)}" 相关的剧集</div></div>`
            : this.searchItems.length > 0
            ? `
              <div class="drama-grid">
                ${this.searchItems.map((d) => this._renderDramaCard(d, { showSource: true })).join("")}
              </div>
              <div class="load-more-row">
                ${
                  this.searchLoading
                    ? `<span class="inline-spinner"></span>`
                    : this.searchHasMore
                    ? `<button class="btn" id="search-more-btn" data-focusable>加载下一页搜索结果</button>`
                    : ""
                }
              </div>
            `
            : ""
        }
      `;
    }

    _renderFollowsView() {
      const favs = (this.state.favorites || []).map(normalizeDrama);
      const states = this.state.followStates || {};
      const filtered = favs.filter((d) => {
        if (this.followSourceFilter && d.source !== this.followSourceFilter) return false;
        return matchesQuery(d, this.followFilter);
      });

      const updatedList = filtered.filter((d) => this._hasUnreadUpdate(d, states[d.id]));
      const normalList = filtered.filter((d) => !this._hasUnreadUpdate(d, states[d.id]));

      return `
        <div class="section-header">
          <h2 class="section-title">我的追剧 (${favs.length})</h2>
          <div style="display:flex;gap:8px;flex-wrap:wrap;">
            <button class="btn btn-sm" id="fol-check-btn" ${this.followChecking ? "disabled" : ""} data-focusable>
              ${this.followChecking ? "检查更新中..." : "↻ 检查更新"}
            </button>
            ${
              favs.length > 0
                ? `<button class="btn btn-sm ${this.followBatchMode ? "btn-primary" : ""}" id="fol-batch-toggle" data-focusable>
                     ${this.followBatchMode ? "完成管理" : "批量管理"}
                   </button>`
                : ""
            }
            ${
              this.followBatchMode && this.followSelected.size > 0
                ? `<button class="btn btn-sm btn-danger" id="fol-unfollow-sel" data-focusable>取消追剧 (${this.followSelected.size})</button>`
                : ""
            }
          </div>
        </div>
        ${
          favs.length > 0
            ? `<div style="display:flex;gap:10px;margin-bottom:16px;flex-wrap:wrap;">
                 <input type="search" class="text-input" id="fol-filter-inp" value="${esc(this.followFilter)}" placeholder="筛选追剧名称或首字母..." style="max-width:320px;" />
                 <select class="text-input" id="fol-src-sel" style="flex:0 0 auto;width:auto;">
                   <option value="">全部站源</option>
                   ${(this.state.sources || []).map((s) => `<option value="${esc(s.id)}" ${this.followSourceFilter === s.id ? "selected" : ""}>${esc(s.name)}</option>`).join("")}
                 </select>
               </div>`
            : ""
        }
        ${
          filtered.length === 0
            ? `<div class="empty-state"><div>${favs.length === 0 ? "还没有追剧内容，在剧集详情中点击「追剧」即可收藏并追踪更新" : "没有符合筛选条件的追剧"}</div></div>`
            : `
              ${
                updatedList.length > 0
                  ? `<div class="setting-label" style="margin:8px 0 10px;color:var(--accent);">有更新 (${updatedList.length})</div>
                     <div class="drama-grid" style="margin-bottom:22px;">
                       ${updatedList
                         .map((d) =>
                           this._renderDramaCard(d, {
                             showSource: true,
                             hasUpdate: true,
                             selectable: this.followBatchMode,
                             selected: this.followSelected.has(d.id)
                           })
                         )
                         .join("")}
                     </div>`
                  : ""
              }
              ${
                normalList.length > 0
                  ? `${updatedList.length > 0 ? `<div class="setting-label" style="margin:8px 0 10px;">追剧中 (${normalList.length})</div>` : ""}
                     <div class="drama-grid">
                       ${normalList
                         .map((d) =>
                           this._renderDramaCard(d, {
                             showSource: true,
                             selectable: this.followBatchMode,
                             selected: this.followSelected.has(d.id)
                           })
                         )
                         .join("")}
                     </div>`
                  : ""
              }
            `
        }
      `;
    }

    _renderHistoryView() {
      const history = this.state.history || [];
      const filtered = history.filter((entry) => {
        const d = normalizeDrama(entry.drama);
        if (this.historySourceFilter && d.source !== this.historySourceFilter) return false;
        return matchesQuery(d, this.historyFilter);
      });

      const buckets = { 今天: [], 昨天: [], 更早: [] };
      for (const entry of filtered) {
        buckets[dayBucket(entry.updatedAt)].push(entry);
      }

      return `
        <div class="section-header">
          <h2 class="section-title">观看记录 (${history.length})</h2>
          <div style="display:flex;gap:8px;flex-wrap:wrap;">
            ${
              filtered.length > 0
                ? `
                  <button class="btn btn-sm ${this.historyBatchMode ? "btn-primary" : ""}" id="hist-batch-toggle" data-focusable>
                    ${this.historyBatchMode ? "退出选择" : "批量选择"}
                  </button>
                  ${
                    this.historyBatchMode && this.historySelected.size > 0
                      ? `<button class="btn btn-sm btn-danger" id="hist-del-sel" data-focusable>删除选中 (${this.historySelected.size})</button>`
                      : `<button class="btn btn-sm btn-danger" id="hist-clear-listed" data-focusable>清空当前列表</button>`
                  }
                `
                : ""
            }
          </div>
        </div>
        ${
          history.length > 0
            ? `<div style="display:flex;gap:10px;margin-bottom:16px;flex-wrap:wrap;">
                 <input type="search" class="text-input" id="hist-filter-inp" value="${esc(this.historyFilter)}" placeholder="搜索观看历史..." style="max-width:320px;" />
                 <select class="text-input" id="hist-src-sel" style="flex:0 0 auto;width:auto;">
                   <option value="">全部站源</option>
                   ${(this.state.sources || []).map((s) => `<option value="${esc(s.id)}" ${this.historySourceFilter === s.id ? "selected" : ""}>${esc(s.name)}</option>`).join("")}
                 </select>
               </div>`
            : ""
        }
        ${
          filtered.length === 0
            ? `<div class="empty-state"><div>暂无观看记录</div></div>`
            : ["今天", "昨天", "更早"]
                .filter((k) => buckets[k].length > 0)
                .map(
                  (k) => `
                  <div class="setting-label" style="margin:12px 0 10px;">${k} (${buckets[k].length})</div>
                  <div class="drama-grid" style="margin-bottom:20px;">
                    ${buckets[k]
                      .map((entry) => {
                        const d = normalizeDrama(entry.drama);
                        return this._renderDramaCard(d, {
                          showSource: true,
                          watch: entry,
                          selectable: this.historyBatchMode,
                          selected: this.historySelected.has(d.id)
                        });
                      })
                      .join("")}
                  </div>
                `
                )
                .join("")
        }
      `;
    }

    _renderDownloadsView() {
      const activeJobs = this.downloadJobs.filter((j) => j.state !== "completed");
      const completedJobs = this.downloadJobs.filter((j) => j.state === "completed");

      const dramaGroups = new Map();
      for (const j of completedJobs) {
        const d = normalizeDrama(j.drama);
        if (!dramaGroups.has(d.id)) {
          dramaGroups.set(d.id, { drama: d, jobs: [] });
        }
        dramaGroups.get(d.id).jobs.push(j);
      }
      for (const g of dramaGroups.values()) {
        g.jobs.sort((a, b) => (Number(a.index) || 0) - (Number(b.index) || 0));
      }

      const stateLabel = (st) => {
        switch (st) {
          case "queued":
            return "等待下载";
          case "downloading":
            return "正在下载";
          case "converting":
            return "解密转码中";
          case "paused":
            return "已暂停";
          case "failed":
            return "下载失败";
          case "removing":
            return "正在移除";
          default:
            return st;
        }
      };

      return `
        <div class="section-header">
          <div class="chip-row" style="margin:0;padding:0;">
            <button class="chip ${this.downloadTab === "active" ? "active" : ""}" data-dl-tab="active">进行中 (${activeJobs.length})</button>
            <button class="chip ${this.downloadTab === "completed" ? "active" : ""}" data-dl-tab="completed">已下载合集 (${dramaGroups.size})</button>
          </div>
          ${
            this.downloadTab === "active" && activeJobs.length > 0
              ? `<div style="display:flex;gap:8px;">
                   <button class="btn btn-sm" data-dl-batch="pauseAll">全部暂停</button>
                   <button class="btn btn-sm" data-dl-batch="resumeAll">全部继续</button>
                 </div>`
              : ""
          }
        </div>

        ${
          this.downloadTab === "active"
            ? activeJobs.length === 0
              ? `<div class="empty-state"><div>当前没有进行中的下载任务</div></div>`
                : activeJobs
                  .map((j) => {
                    const d = normalizeDrama(j.drama);
                    const dCov = coverURL(d, false);
                    const pct = Math.round(Math.max(0, Math.min(1, Number(j.progress) || 0)) * 100);
                    return `
                      <div class="dl-item">
                        <img src="${esc(resolvedCoverURL(d))}" data-cover-url="${esc(dCov)}" data-drama-id="${esc(d.id)}" alt="" style="width:52px;height:68px;border-radius:8px;object-fit:cover;flex-shrink:0;" />
                        <div style="flex:1;min-width:0;">
                          <div style="font-weight:700;">${esc(d.title)} · 第 ${j.index} 集</div>
                          <div style="font-size:12px;color:var(--text-muted);">
                            ${stateLabel(j.state)} · ${pct}% ${j.bytes > 0 ? `(${formatBytes(j.bytes)}${j.total > 0 ? " / " + formatBytes(j.total) : ""})` : ""}
                            ${j.error ? ` · <span style="color:var(--danger);">${esc(j.error)}</span>` : ""}
                          </div>
                          <div class="dl-progress-bar"><div class="dl-progress-fill" style="width:${pct}%;"></div></div>
                        </div>
                        <div style="display:flex;gap:6px;flex-shrink:0;">
                          ${
                            j.state === "downloading" || j.state === "queued"
                              ? `<button class="btn btn-sm" data-job-ctl="pause" data-job-id="${esc(j.id)}">暂停</button>`
                              : j.state === "paused" || j.state === "failed"
                              ? `<button class="btn btn-sm btn-primary" data-job-ctl="resume" data-job-id="${esc(j.id)}">继续</button>`
                              : ""
                          }
                          <button class="btn btn-sm btn-danger" data-job-ctl="remove" data-job-id="${esc(j.id)}">删除</button>
                        </div>
                      </div>
                    `;
                  })
                  .join("")
            : dramaGroups.size === 0
            ? `<div class="empty-state"><div>暂无已下载视频，可在剧集详情点击「下载选集」离线缓存</div></div>`
            : [...dramaGroups.values()]
                .map(({ drama, jobs }) => {
                  const totalBytes = jobs.reduce((acc, j) => acc + (Number(j.bytes) || 0), 0);
                  const gCov = coverURL(drama, false);
                  return `
                    <div class="settings-card" style="margin-bottom:14px;">
                      <div style="display:flex;gap:14px;align-items:center;flex-wrap:wrap;margin-bottom:12px;">
                        <img src="${esc(resolvedCoverURL(drama))}" data-cover-url="${esc(gCov)}" data-drama-id="${esc(drama.id)}" alt="" style="width:58px;height:76px;border-radius:10px;object-fit:cover;" />
                        <div style="flex:1;min-width:180px;">
                          <div style="font-weight:700;font-size:16px;">${esc(drama.title)}</div>
                          <div style="font-size:12px;color:var(--text-muted);">
                            已下载 ${jobs.length}${drama.episodes > 0 ? ` / ${drama.episodes}` : ""} 集 · 共 ${formatBytes(totalBytes)}
                          </div>
                        </div>
                        <div style="display:flex;gap:8px;flex-wrap:wrap;">
                          <button class="btn btn-sm btn-primary" data-col-open="${esc(drama.id)}">剧集详情 / 补全</button>
                          ${
                            jobs.length >= 2 && this.ffmpegReady
                              ? `<button class="btn btn-sm" data-col-merge="${esc(drama.id)}">合并导出 MP4</button>`
                              : ""
                          }
                          <button class="btn btn-sm btn-danger" data-col-del="${esc(drama.id)}">删除整部</button>
                        </div>
                      </div>
                      <div class="episode-grid">
                        ${jobs
                          .map(
                            (j) => `
                          <div class="ep-btn" style="flex-direction:row;justify-content:space-between;padding:8px 10px;">
                            <button style="font-weight:600;flex:1;text-align:left;" data-local-play="${esc(drama.id)}" data-local-ep="${j.index}">▶ 第${j.index}集</button>
                            <a href="${esc(API.downloadFileURL(j.id))}" download title="保存 MP4 到本机" style="text-decoration:none;font-size:13px;color:var(--accent);font-weight:700;">⬇</a>
                          </div>
                        `
                          )
                          .join("")}
                      </div>
                    </div>
                  `;
                })
                .join("")
        }
      `;
    }

    _renderSettingsView() {
      const isAdmin = !!(this.state.profile && this.state.profile.admin);
      const visibleTabs = SETTINGS_TABS.filter((t) => !t.adminOnly || isAdmin);
      if (!visibleTabs.some((t) => t.id === this.settingsTab)) {
        this.settingsTab = visibleTabs[0].id;
      }

      return `
        <div class="settings-layout">
          <div class="settings-nav">
            ${visibleTabs
              .map(
                (t) => `
              <button class="nav-item ${this.settingsTab === t.id ? "active" : ""}" data-set-tab="${t.id}" style="white-space:nowrap;">
                <span>${esc(t.label)}</span>
              </button>
            `
              )
              .join("")}
          </div>
          <div>${this._renderSettingsSection()}</div>
        </div>
      `;
    }

    _renderSettingsSection() {
      const st = this.state;
      const pb = st.playback || {};
      const rs = this.resourceSettings || {
        proxyMode: "auto",
        proxyUrl: "",
        catalogConcurrency: 3,
        catalogIntervalMs: 250,
        downloadConcurrency: 2,
        downloadBySource: false
      };

      switch (this.settingsTab) {
        case "appearance":
          return `
            <div class="settings-card">
              <div class="setting-row">
                <div>
                  <div class="setting-label">界面布局模式</div>
                  <div class="setting-desc">自动适配手机竖屏、平板/电脑侧栏与大屏电视遥控焦点布局</div>
                </div>
                <select class="text-input" data-gset="displayMode" style="width:auto;flex:0 0 auto;">
                  ${[
                    ["auto", "自动适配"],
                    ["phone", "手机竖屏"],
                    ["pad", "平板布局"],
                    ["window", "电脑桌面"],
                    ["television", "电视大屏"]
                  ]
                    .map(([v, l]) => `<option value="${v}" ${st.displayMode === v ? "selected" : ""}>${l}</option>`)
                    .join("")}
                </select>
              </div>
              <div class="setting-row">
                <div>
                  <div class="setting-label">深浅色外观</div>
                  <div class="setting-desc">切换夜间深色、日间浅色或跟随操作系统</div>
                </div>
                <select class="text-input" data-gset="themeMode" style="width:auto;flex:0 0 auto;">
                  ${[
                    ["system", "跟随系统"],
                    ["dark", "深色模式"],
                    ["light", "浅色模式"]
                  ]
                    .map(([v, l]) => `<option value="${v}" ${st.themeMode === v ? "selected" : ""}>${l}</option>`)
                    .join("")}
                </select>
              </div>
              <div class="setting-row">
                <div>
                  <div class="setting-label">主题强调色</div>
                  <div class="setting-desc">应用按钮、焦点边框与进度条配色方案</div>
                </div>
                <div class="color-swatch-row">
                  ${THEME_COLORS.map(
                    (c) => `<button class="color-swatch ${st.themeColor === c.id ? "active" : ""}" style="background:${c.hex};" title="${c.label}" data-color-pick="${c.id}"></button>`
                  ).join("")}
                </div>
              </div>
              <div class="setting-row">
                <div>
                  <div class="setting-label">搜索页显示首字母软键盘</div>
                  <div class="setting-desc">方便电视遥控器或触屏单手快速输入拼音首字母检索</div>
                </div>
                <button class="switch ${st.televisionKeyboard ? "on" : ""}" data-gtoggle="televisionKeyboard"></button>
              </div>
            </div>
          `;

        case "playback":
          return `
            <div class="settings-card">
              <div class="setting-row">
                <div>
                  <div class="setting-label">默认画质偏好</div>
                  <div class="setting-desc">在线播放时优先匹配的清晰度档位</div>
                </div>
                <select class="text-input" data-pb-num="quality" style="width:auto;flex:0 0 auto;">
                  ${[0, 1080, 720, 540, 480, 360]
                    .map((q) => `<option value="${q}" ${(Number(pb.quality) || 0) === q ? "selected" : ""}>${qualityLabel(q)}</option>`)
                    .join("")}
                </select>
              </div>
              <div class="setting-row">
                <div>
                  <div class="setting-label">默认播放倍速</div>
                  <div class="setting-desc">打开视频时的初始播放速度</div>
                </div>
                <select class="text-input" data-pb-num="speed" style="width:auto;flex:0 0 auto;">
                  ${[0.5, 0.75, 1.0, 1.25, 1.5, 2.0, 3.0]
                    .map((s) => `<option value="${s}" ${(Number(pb.speed) || 1) === s ? "selected" : ""}>${s}x</option>`)
                    .join("")}
                </select>
              </div>
              <div class="setting-row">
                <div>
                  <div class="setting-label">自动连播下一集</div>
                  <div class="setting-desc">当前集播放结束后自动切换至下一集</div>
                </div>
                <button class="switch ${pb.autoAdvance !== false ? "on" : ""}" data-pb-bool="autoAdvance"></button>
              </div>
              <div class="setting-row">
                <div>
                  <div class="setting-label">默认开启红果弹幕</div>
                  <div class="setting-desc">播放红果在线剧集时滚动展示互动弹幕</div>
                </div>
                <button class="switch ${pb.danmaku !== false ? "on" : ""}" data-pb-bool="danmaku"></button>
              </div>
              <div class="setting-row">
                <div>
                  <div class="setting-label">后台预加载下一集</div>
                  <div class="setting-desc">播放当前集时提前解析并预热下一集首段媒体</div>
                </div>
                <button class="switch ${pb.preload !== false ? "on" : ""}" data-pb-bool="preload"></button>
              </div>
              <div class="setting-row">
                <div>
                  <div class="setting-label">双击画面左右侧快进/快退 10 秒</div>
                  <div class="setting-desc">双击左侧后退 10 秒，双击右侧前进 10 秒</div>
                </div>
                <button class="switch ${pb.doubleTapSeek !== false ? "on" : ""}" data-pb-bool="doubleTapSeek"></button>
              </div>
              <div class="setting-row">
                <div>
                  <div class="setting-label">触屏边缘上下滑动调节亮度与音量</div>
                  <div class="setting-desc">移动端左侧上下滑动调亮度，右侧上下滑动调音量</div>
                </div>
                <button class="switch ${pb.swipeLevels !== false ? "on" : ""}" data-pb-bool="swipeLevels"></button>
              </div>
            </div>
          `;

        case "download": {
          const dir = (this.storageInfo && this.storageInfo.directory) || "";
          const used = (this.storageInfo && this.storageInfo.bytes) || 0;
          return `
            <div class="settings-card">
              <div class="setting-row">
                <div>
                  <div class="setting-label">服务端下载目录</div>
                  <div class="setting-desc">${esc(dir || "正在读取...")} · 已占用 ${formatBytes(used)}</div>
                </div>
                ${st.profile && st.profile.admin ? `<button class="btn btn-sm" id="dl-move-dir-btn">迁移目录</button>` : ""}
              </div>
              ${
                st.profile && st.profile.admin
                  ? `
                    <div class="setting-row">
                      <div>
                        <div class="setting-label">同时下载集数上限</div>
                        <div class="setting-desc">控制后台并行下载分片的剧集任务数量</div>
                      </div>
                      <select class="text-input" id="rs-dl-conc" style="width:auto;flex:0 0 auto;">
                        ${[1, 2, 3, 4, 5].map((n) => `<option value="${n}" ${(Number(rs.downloadConcurrency) || 2) === n ? "selected" : ""}>${n} 个任务</option>`).join("")}
                      </select>
                    </div>
                    <div class="setting-row">
                      <div>
                        <div class="setting-label">按站源独立计算并发</div>
                        <div class="setting-desc">开启后不同站源可同时各自跑满并发上限</div>
                      </div>
                      <button class="switch ${rs.downloadBySource ? "on" : ""}" id="rs-dl-bysrc"></button>
                    </div>
                  `
                  : ""
              }
              <div class="setting-row">
                <div>
                  <div class="setting-label">FFmpeg 合并与兼容支持</div>
                  <div class="setting-desc">${this.ffmpegReady ? "已检测到系统 FFmpeg，支持多集无损合并导出 MP4 与 H.265 兼容转码" : "未检测到系统 FFmpeg（单集纯 Go 解密与 HLS 封装仍可正常离线下载）"}</div>
                </div>
                <span class="brand-tag">${this.ffmpegReady ? "已就绪" : "纯 Go 模式"}</span>
              </div>
            </div>
          `;
        }

        case "sources":
          return `
            <div class="settings-card">
              <div class="setting-label" style="margin-bottom:10px;">站源总开关（共 ${(st.knownSources || []).length} 个内置站源）</div>
              ${(st.knownSources || [])
                .map(
                  (s) => `
                <div class="setting-row">
                  <div>
                    <div class="setting-label">${esc(s.name)} ${s.restricted ? `<span style="font-size:11px;color:var(--warning);">[扩展源]</span>` : ""}</div>
                    <div class="setting-desc">${esc(s.description || s.id)}</div>
                  </div>
                  <button class="switch ${st.sourceSwitches && st.sourceSwitches[s.id] ? "on" : ""}" data-src-sw="${esc(s.id)}"></button>
                </div>
              `
                )
                .join("")}
            </div>
            <div class="settings-card">
              <div class="setting-row">
                <div>
                  <div class="setting-label">目录抓取并发度</div>
                  <div class="setting-desc">站源列表请求并发连接数</div>
                </div>
                <select class="text-input" id="rs-cat-conc" style="width:auto;flex:0 0 auto;">
                  ${[1, 2, 3, 4, 5, 6].map((n) => `<option value="${n}" ${(Number(rs.catalogConcurrency) || 3) === n ? "selected" : ""}>${n}</option>`).join("")}
                </select>
              </div>
              <div class="setting-row">
                <div>
                  <div class="setting-label">请求最小间隔 (毫秒)</div>
                  <div class="setting-desc">防止触发站源频率风控</div>
                </div>
                <select class="text-input" id="rs-cat-intv" style="width:auto;flex:0 0 auto;">
                  ${[0, 100, 250, 500, 1000].map((n) => `<option value="${n}" ${(Number(rs.catalogIntervalMs) || 250) === n ? "selected" : ""}>${n} ms</option>`).join("")}
                </select>
              </div>
            </div>
          `;

        case "proxy":
          return `
            <div class="settings-card">
              ${rs.warning ? `<div class="warning-banner"><span>${esc(rs.warning)}</span></div>` : ""}
              <div class="setting-row">
                <div>
                  <div class="setting-label">网络代理策略</div>
                  <div class="setting-desc">用于站源请求、红果设备注册与媒体下载转发</div>
                </div>
                <select class="text-input" id="proxy-mode-sel" style="width:auto;flex:0 0 auto;">
                  <option value="auto" ${rs.proxyMode === "auto" ? "selected" : ""}>跟随系统环境变量</option>
                  <option value="direct" ${rs.proxyMode === "direct" ? "selected" : ""}>强制直连 (不使用代理)</option>
                  <option value="custom" ${rs.proxyMode === "custom" ? "selected" : ""}>手动指定代理地址</option>
                </select>
              </div>
              <div class="setting-row" style="flex-direction:column;align-items:stretch;">
                <div class="setting-label">代理服务器地址 (HTTP / HTTPS / SOCKS5)</div>
                <div style="display:flex;gap:10px;margin-top:6px;flex-wrap:wrap;">
                  <input type="text" class="text-input" id="proxy-url-inp" value="${esc(rs.proxyUrl || "")}" placeholder="例如 http://127.0.0.1:7890 或 socks5://127.0.0.1:1080" />
                  <button class="btn btn-primary" id="proxy-save-btn">保存代理设置</button>
                </div>
              </div>
            </div>
          `;

        case "users":
          return `
            <div class="settings-card">
              <div class="setting-row">
                <div>
                  <div class="setting-label">新浏览器会话要求密码登录</div>
                  <div class="setting-desc">开启后新设备首次打开网页需先输入已设密用户的访问密码</div>
                </div>
                <button class="switch ${st.forceLogin ? "on" : ""}" data-gtoggle="forceLogin" ${!(st.profile && st.profile.admin) ? "disabled" : ""}></button>
              </div>
            </div>
            <div class="settings-card">
              <div class="section-header" style="margin-top:0;">
                <span class="setting-label">用户列表 (${(st.profiles || []).length})</span>
                ${st.profile && st.profile.admin ? `<button class="btn btn-sm btn-primary" id="usr-create-btn">+ 新建成员用户</button>` : ""}
              </div>
              ${(st.profiles || [])
                .map(
                  (p) => `
                <div class="setting-row">
                  <div>
                    <div class="setting-label">${esc(p.name)} ${p.admin ? `<span class="brand-tag">管理员</span>` : ""} ${p.protected ? "🔒" : ""}</div>
                    <div class="setting-desc">
                      ${p.admin ? "拥有全部站源与系统设置权限" : `允许 ${(p.sources || []).length} 个站源 · ${p.download ? "允许离线下载" : "仅在线观看"}`}
                    </div>
                  </div>
                  <div style="display:flex;gap:8px;">
                    ${
                      st.profile && (st.profile.admin || st.profile.id === p.id)
                        ? `<button class="btn btn-sm" data-edit-prof="${esc(p.id)}">编辑 / 密码</button>`
                        : ""
                    }
                    ${
                      st.profile && st.profile.admin && !p.admin
                        ? `<button class="btn btn-sm btn-danger" data-del-prof="${esc(p.id)}">删除</button>`
                        : ""
                    }
                  </div>
                </div>
              `
                )
                .join("")}
            </div>
          `;

        case "about":
          return `
            <div class="settings-card">
              <div style="display:flex;align-items:center;gap:16px;padding-bottom:14px;border-bottom:1px solid var(--border);">
                <img src="/icon.svg" alt="" style="width:56px;height:56px;border-radius:14px;" />
                <div>
                  <div style="font-size:19px;font-weight:800;">${esc(st.appName || "真果鉴")} 网页版</div>
                  <div style="font-size:13px;color:var(--text-secondary);">版本 v${esc(this.version)} · 全站源独立服务端</div>
                </div>
              </div>
              <div class="setting-row">
                <div>
                  <div class="setting-label">开源仓库</div>
                  <div class="setting-desc">github.com/wzdyf1992/ZhenGuoJian-Web</div>
                </div>
                <a class="btn btn-sm" href="https://github.com/wzdyf1992/ZhenGuoJian-Web" target="_blank" rel="noopener noreferrer">访问 GitHub</a>
              </div>
              <div class="setting-row">
                <div>
                  <div class="setting-label">安装到桌面 / 主屏幕 (PWA)</div>
                  <div class="setting-desc">${
                    this.pwaInstalled
                      ? "当前已作为独立网页应用运行"
                      : !window.isSecureContext
                      ? "当前为 HTTP 明文地址；浏览器安全策略要求通过 HTTPS 安全连接访问时才允许安装独立网页应用"
                      : this.deferredInstallPrompt
                      ? "支持一键安装为独立窗口应用（无浏览器地址栏）"
                      : "Safari 可点底部分享按钮选「添加到主屏幕」；Chrome / Edge 可在菜单选「安装应用」"
                  }</div>
                </div>
                ${
                  this.pwaInstalled
                    ? `<span class="brand-tag">已安装</span>`
                    : this.deferredInstallPrompt
                    ? `<button class="btn btn-sm btn-primary" id="about-pwa-install">安装应用</button>`
                    : ""
                }
              </div>
              <div class="setting-row">
                <div>
                  <div class="setting-label">红果虚拟设备状态</div>
                  <div class="setting-desc">${this.hongguoReady ? "设备凭证已注册就绪" : "尚未注册设备凭证"}</div>
                </div>
                <button class="btn btn-sm" id="about-hg-reg">${this.hongguoReady ? "重新检测" : "立即注册"}</button>
              </div>
              <div class="setting-row">
                <div>
                  <div class="setting-label">启动时自动检查新版本</div>
                  <div class="setting-desc">检测到更新时提示版本升级</div>
                </div>
                <div style="display:flex;align-items:center;gap:10px;">
                  <button class="btn btn-sm" id="about-check-upd">立即检查</button>
                  <button class="switch ${st.checkUpdates ? "on" : ""}" data-gtoggle="checkUpdates"></button>
                </div>
              </div>
              <div class="setting-row">
                <div>
                  <div class="setting-label">匿名版本统计</div>
                  <div class="setting-desc">仅上报应用版本号，不包含任何剧库或个人记录</div>
                </div>
                <button class="switch ${st.shareUsage ? "on" : ""}" data-gtoggle="shareUsage"></button>
              </div>
            </div>
          `;

        case "diagnostics":
          return `
            <div class="settings-card">
              <div class="setting-row">
                <div>
                  <div class="setting-label">数据备份与跨端迁移</div>
                  <div class="setting-desc">备份文件格式 (schema 1) 与真果鉴移动端客户端完全互通</div>
                </div>
                ${
                  st.profile && st.profile.admin
                    ? `<div style="display:flex;gap:8px;">
                         <a class="btn btn-sm" href="/api/store/backup" download>导出备份 (.json)</a>
                         <label class="btn btn-sm btn-primary" style="cursor:pointer;">
                           导入恢复
                           <input type="file" id="diag-backup-file" accept=".json,application/json" class="hidden" />
                         </label>
                       </div>`
                    : `<span class="setting-desc">仅管理员可导入导出完整备份</span>`
                }
              </div>
            </div>
            <div class="settings-card">
              <div class="section-header" style="margin-top:0;">
                <span class="setting-label">运行与核心诊断日志</span>
                <div style="display:flex;gap:8px;">
                  <button class="btn btn-sm" id="diag-refresh-log">刷新</button>
                  <button class="btn btn-sm" id="diag-copy-log">复制日志</button>
                  <button class="btn btn-sm btn-danger" id="diag-clear-log">清空内存日志</button>
                </div>
              </div>
              <pre class="log-console" id="diag-log-pre">${esc(this.logLines.length > 0 ? this.logLines.join("\n") : "暂无诊断日志记录")}</pre>
            </div>
          `;

        default:
          return "";
      }
    }

    _openProfileEditorModal(editId) {
      const profiles = this.state.profiles || [];
      const existing = editId ? profiles.find((p) => p.id === editId) : null;
      const isAdminActor = !!(this.state.profile && this.state.profile.admin);
      const editingAdmin = !!(existing && existing.admin);
      const knownSources = this.state.knownSources || [];

      const selectedSources = new Set(
        existing ? existing.sources || [] : knownSources.filter((s) => !s.restricted).map((s) => s.id)
      );
      let allowDownload = existing ? !!existing.download : true;

      const modal = document.getElementById("modal-root");
      modal.classList.remove("hidden");
      modal.innerHTML = `
        <div class="modal-card">
          <div class="modal-header">
            <h3 class="modal-title">${existing ? `编辑用户 · ${esc(existing.name)}` : "新建成员用户"}</h3>
            <button class="icon-btn" id="pe-close">✕</button>
          </div>
          <div class="modal-body">
            <div>
              <div class="setting-label" style="margin-bottom:6px;">用户名称</div>
              <input type="text" class="text-input" id="pe-name" value="${esc(existing ? existing.name : "")}" placeholder="例如：客厅电视、家人" style="width:100%;" />
            </div>
            <div>
              <div class="setting-label" style="margin-bottom:6px;">访问密码 ${existing && existing.protected ? "(留空保持原密码)" : "(可选，创建多用户前需先为管理员设密码)"}</div>
              <div style="display:flex;gap:8px;align-items:center;">
                <input type="password" class="text-input" id="pe-pin" minlength="3" maxlength="32" placeholder="设置 3-32 位访问密码" autocomplete="new-password" />
                ${existing && existing.protected ? `<label style="display:flex;align-items:center;gap:6px;white-space:nowrap;font-size:13px;padding:6px 4px;cursor:pointer;"><input type="checkbox" id="pe-clear-pin" /> 清除密码</label>` : ""}
              </div>
            </div>
            ${
              !editingAdmin && isAdminActor
                ? `
                  <label style="display:flex;align-items:center;justify-content:space-between;cursor:pointer;">
                    <span class="setting-label">允许使用离线下载与本地视频导出</span>
                    <input type="checkbox" id="pe-dl" ${allowDownload ? "checked" : ""} />
                  </label>
                  <div>
                    <div class="setting-label" style="margin-bottom:8px;">允许访问的站源</div>
                    <div style="display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px;">
                      ${knownSources
                        .map(
                          (s) => `
                        <label style="display:flex;align-items:center;gap:6px;font-size:13px;cursor:pointer;">
                          <input type="checkbox" data-pe-src="${esc(s.id)}" ${selectedSources.has(s.id) ? "checked" : ""} />
                          <span>${esc(s.name)}</span>
                        </label>
                      `
                        )
                        .join("")}
                    </div>
                  </div>
                `
                : ""
            }
          </div>
          <div class="modal-footer">
            <button class="btn" id="pe-cancel">取消</button>
            <button class="btn btn-primary" id="pe-save">保存用户</button>
          </div>
        </div>
      `;

      modal.querySelector("#pe-close").onclick = () => this._closeModal();
      modal.querySelector("#pe-cancel").onclick = () => this._closeModal();
      modal.querySelector("#pe-save").onclick = async () => {
        const name = modal.querySelector("#pe-name").value.trim();
        const pinVal = modal.querySelector("#pe-pin").value;
        const clearPin = modal.querySelector("#pe-clear-pin") && modal.querySelector("#pe-clear-pin").checked;
        const dlChk = modal.querySelector("#pe-dl");
        if (dlChk) allowDownload = dlChk.checked;

        const srcs = [];
        modal.querySelectorAll("[data-pe-src]").forEach((chk) => {
          if (chk.checked) srcs.push(chk.getAttribute("data-pe-src"));
        });

        const payload = {
          id: existing ? existing.id : "",
          name,
          download: editingAdmin ? true : allowDownload,
          sources: editingAdmin ? [] : srcs
        };
        if (clearPin) {
          payload.pin = "";
        } else if (pinVal !== "") {
          const pinLen = Array.from(pinVal).length;
          if (pinLen < 3 || pinLen > 32) {
            toast("密码需要 3 至 32 个字符");
            return;
          }
          payload.pin = pinVal;
        }

        try {
          const res = await API.saveProfile(payload);
          this._closeModal();
          if (res.state) this.setState(res.state);
          toast("用户配置已保存");
        } catch (err) {
          toast(err.message);
        }
      };
    }

    _applyDramaMetadataUpdate(fresh) {
      if (!fresh || !fresh.id) return;
      if (this.detailDrama && this.detailDrama.id === fresh.id) {
        this.detailDrama = mergeDrama(this.detailDrama, fresh);
      }
      if (this.exploreLibrary && this.exploreLibrary.has(fresh.id)) {
        this.exploreLibrary.set(fresh.id, mergeDrama(this.exploreLibrary.get(fresh.id), fresh));
      }
      for (let i = 0; i < this.exploreItems.length; i++) {
        if (this.exploreItems[i].id === fresh.id) {
          this.exploreItems[i] = mergeDrama(this.exploreItems[i], fresh);
        }
      }
      for (let i = 0; i < this.searchItems.length; i++) {
        if (this.searchItems[i].id === fresh.id) {
          this.searchItems[i] = mergeDrama(this.searchItems[i], fresh);
        }
      }
    }

    _findDramaById(id) {
      if (this.detailDrama && this.detailDrama.id === id) return this.detailDrama;
      if (this.exploreLibrary.has(id)) return this.exploreLibrary.get(id);
      for (const d of this.exploreItems) if (d.id === id) return d;
      for (const d of this.searchItems) if (d.id === id) return d;
      for (const d of (this.state && this.state.favorites) || []) if (d.id === id) return normalizeDrama(d);
      for (const h of (this.state && this.state.history) || []) if (h.drama && h.drama.id === id) return normalizeDrama(h.drama);
      for (const j of this.downloadJobs) if (j.drama && j.drama.id === id) return normalizeDrama(j.drama);
      return null;
    }

    _bindViewEvents(container) {
      if (!container) return;

      container.querySelectorAll("[data-card-id]").forEach((card) => {
        card.onclick = () => {
          const id = card.getAttribute("data-card-id");
          if (this.view === "follows" && this.followBatchMode) {
            if (this.followSelected.has(id)) this.followSelected.delete(id);
            else this.followSelected.add(id);
            this.render();
            return;
          }
          if (this.view === "history" && this.historyBatchMode) {
            if (this.historySelected.has(id)) this.historySelected.delete(id);
            else this.historySelected.add(id);
            this.render();
            return;
          }
          const d = this._findDramaById(id);
          if (d) this.openDetail(d);
        };
      });

      container.querySelectorAll("[data-hero-id]").forEach((hero) => {
        const trigger = () => {
          const d = this._findDramaById(hero.getAttribute("data-hero-id"));
          if (d) this.openDetail(d);
        };
        hero.onclick = trigger;
        hero.onkeydown = (e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            trigger();
          }
        };
      });

      container.querySelectorAll("[data-exp-tab]").forEach((b) => {
        b.onclick = () => this.selectExploreTab(b.getAttribute("data-exp-tab"));
      });
      const expRetry = container.querySelector("#exp-retry-btn");
      if (expRetry) expRetry.onclick = () => this._reloadExplore(true);
      const expMore = container.querySelector("#exp-more-btn");
      if (expMore) expMore.onclick = () => this._loadMoreExplore();

      if (this.view === "explore" && this.exploreHasMore && !this.exploreLoading) {
        container.onscroll = () => {
          if (container.scrollTop + container.clientHeight >= container.scrollHeight - 280) {
            this._loadMoreExplore();
          }
        };
      } else {
        container.onscroll = null;
      }

      const searchForm = container.querySelector("#search-form");
      const searchInp = container.querySelector("#search-input");
      if (searchInp) {
        if (searchForm) {
          searchForm.onsubmit = (e) => {
            e.preventDefault();
            searchInp.blur();
            this.runSearch(searchInp.value, 1);
          };
        }
        searchInp.oninput = () => {
          this.searchQuery = searchInp.value;
          this.searchSubmitted = false;
          this._triggerSuggestions(this.searchQuery);
        };
        searchInp.onkeydown = (e) => {
          if (e.key === "Enter" && !e.isComposing) {
            e.preventDefault();
            searchInp.blur();
            this.runSearch(searchInp.value, 1);
          }
        };
        const clrBtn = container.querySelector("#search-clear-btn");
        if (clrBtn) clrBtn.onclick = () => this.runSearch("", 1);
      }
      container.querySelectorAll("[data-tv-key]").forEach((k) => {
        k.onclick = () => {
          const val = k.getAttribute("data-tv-key");
          if (val === "BACK") {
            this.searchQuery = this.searchQuery.slice(0, -1);
          } else if (val === "CLEAR") {
            this.searchQuery = "";
          } else {
            this.searchQuery += val.toLowerCase();
          }
          this.runSearch(this.searchQuery, 1);
        };
      });
      container.querySelectorAll("[data-recent-q]").forEach((b) => {
        b.onclick = () => this.runSearch(b.getAttribute("data-recent-q"), 1);
      });
      const clrRecent = container.querySelector("#clear-recent-btn");
      if (clrRecent) {
        clrRecent.onclick = async () => {
          const res = await API.updateUserSettings({ clearRecentSearches: true });
          if (res.state) this.setState(res.state);
        };
      }
      const searchMore = container.querySelector("#search-more-btn");
      if (searchMore) searchMore.onclick = () => this.runSearch(this.searchQuery, this.searchPage + 1);

      const folCheck = container.querySelector("#fol-check-btn");
      if (folCheck) {
        folCheck.onclick = async () => {
          this.followChecking = true;
          this.render();
          try {
            const res = await API.checkFollowUpdates();
            if (res.state) this.state = res.state;
            toast(res.updated > 0 ? `发现 ${res.updated} 部追剧有新集数更新` : "所有追剧均已是最新状态");
          } catch (err) {
            toast(err.message);
          } finally {
            this.followChecking = false;
            this.render();
          }
        };
      }
      const folBatch = container.querySelector("#fol-batch-toggle");
      if (folBatch) {
        folBatch.onclick = () => {
          this.followBatchMode = !this.followBatchMode;
          this.followSelected.clear();
          this.render();
        };
      }
      const folUnf = container.querySelector("#fol-unfollow-sel");
      if (folUnf) {
        folUnf.onclick = async () => {
          const ids = [...this.followSelected];
          const res = await API.unfollowBatch(ids);
          this.followSelected.clear();
          this.followBatchMode = false;
          if (res.state) this.setState(res.state);
          toast(`已取消 ${ids.length} 部追剧`);
        };
      }
      const folInp = container.querySelector("#fol-filter-inp");
      if (folInp) {
        folInp.onchange = () => {
          this.followFilter = folInp.value;
          this.render();
        };
      }
      const folSrc = container.querySelector("#fol-src-sel");
      if (folSrc) {
        folSrc.onchange = () => {
          this.followSourceFilter = folSrc.value;
          this.render();
        };
      }

      const histBatch = container.querySelector("#hist-batch-toggle");
      if (histBatch) {
        histBatch.onclick = () => {
          this.historyBatchMode = !this.historyBatchMode;
          this.historySelected.clear();
          this.render();
        };
      }
      const histDelSel = container.querySelector("#hist-del-sel");
      if (histDelSel) {
        histDelSel.onclick = async () => {
          const res = await API.deleteHistory([...this.historySelected]);
          this.historySelected.clear();
          this.historyBatchMode = false;
          if (res.state) this.setState(res.state);
        };
      }
      const histClr = container.querySelector("#hist-clear-listed");
      if (histClr) {
        histClr.onclick = async () => {
          const listedIds = [...container.querySelectorAll("[data-card-id]")].map((el) => el.getAttribute("data-card-id"));
          if (listedIds.length === 0) return;
          const res = await API.deleteHistory(listedIds);
          if (res.state) this.setState(res.state);
          toast("已清空当前列表记录");
        };
      }
      const histInp = container.querySelector("#hist-filter-inp");
      if (histInp) {
        histInp.onchange = () => {
          this.historyFilter = histInp.value;
          this.render();
        };
      }
      const histSrc = container.querySelector("#hist-src-sel");
      if (histSrc) {
        histSrc.onchange = () => {
          this.historySourceFilter = histSrc.value;
          this.render();
        };
      }

      container.querySelectorAll("[data-dl-tab]").forEach((b) => {
        b.onclick = () => {
          this.downloadTab = b.getAttribute("data-dl-tab");
          this.render();
        };
      });
      container.querySelectorAll("[data-dl-batch]").forEach((b) => {
        b.onclick = async () => {
          const cmd = b.getAttribute("data-dl-batch");
          await API.rpc("controlDownloads", { command: cmd }).catch((e) => toast(e.message));
          await this._refreshDownloadsOnce();
        };
      });
      container.querySelectorAll("[data-job-ctl]").forEach((b) => {
        b.onclick = async () => {
          const cmd = b.getAttribute("data-job-ctl");
          const id = b.getAttribute("data-job-id");
          await API.rpc("controlDownloads", { jobId: id, command: cmd }).catch((e) => toast(e.message));
          await this._refreshDownloadsOnce();
        };
      });
      container.querySelectorAll("[data-col-open]").forEach((b) => {
        b.onclick = () => {
          const d = this._findDramaById(b.getAttribute("data-col-open"));
          if (d) this.openDetail(d);
        };
      });
      container.querySelectorAll("[data-col-merge]").forEach((b) => {
        b.onclick = async () => {
          const dramaId = b.getAttribute("data-col-merge");
          const jobs = this.downloadJobs
            .filter((j) => j.state === "completed" && j.drama && j.drama.id === dramaId)
            .sort((x, y) => (Number(x.index) || 0) - (Number(y.index) || 0));
          if (jobs.length < 2) return;
          toast(`正在无损合并 ${jobs.length} 集视频，请稍候...`);
          try {
            await API.mergeDownloads(jobs.map((j) => j.id));
            toast("合并完成，已触发浏览器下载");
          } catch (err) {
            toast(err.message);
          }
        };
      });
      container.querySelectorAll("[data-col-del]").forEach((b) => {
        b.onclick = async () => {
          const dramaId = b.getAttribute("data-col-del");
          const jobIds = this.downloadJobs.filter((j) => j.drama && j.drama.id === dramaId).map((j) => j.id);
          if (jobIds.length === 0) return;
          await API.rpc("controlDownloadBatch", { jobIds, command: "remove" }).catch((e) => toast(e.message));
          await this._refreshDownloadsOnce();
          toast("已删除离线剧集");
        };
      });
      container.querySelectorAll("[data-local-play]").forEach((b) => {
        b.onclick = () => {
          const dramaId = b.getAttribute("data-local-play");
          const epNum = Number(b.getAttribute("data-local-ep")) || 1;
          const jobs = this.downloadJobs
            .filter((j) => j.state === "completed" && j.drama && j.drama.id === dramaId)
            .sort((x, y) => (Number(x.index) || 0) - (Number(y.index) || 0));
          const d = this._findDramaById(dramaId);
          if (!d) return;
          const eps = jobs.map((j) => ({
            raw: j.chapter || { title: `第${j.index}集`, currentEpisode: j.index },
            number: Number(j.index) || 1,
            title: `第${j.index}集`,
            vip: false
          }));
          this._startPlayback(d, eps, epNum, 0, true);
        };
      });

      container.querySelectorAll("[data-set-tab]").forEach((b) => {
        b.onclick = () => {
          this.settingsTab = b.getAttribute("data-set-tab");
          this._loadSettingsAuxiliary();
          this.render();
        };
      });
      container.querySelectorAll("[data-gset]").forEach((sel) => {
        sel.onchange = async () => {
          const key = sel.getAttribute("data-gset");
          const res = await API.updateGlobalSettings({ [key]: sel.value });
          if (res.state) this.setState(res.state);
        };
      });
      container.querySelectorAll("[data-gtoggle]").forEach((sw) => {
        sw.onclick = async () => {
          const key = sw.getAttribute("data-gtoggle");
          try {
            const res = await API.updateGlobalSettings({ [key]: !this.state[key] });
            if (res.state) this.setState(res.state);
          } catch (err) {
            toast(err.message);
          }
        };
      });
      container.querySelectorAll("[data-color-pick]").forEach((sw) => {
        sw.onclick = async () => {
          const res = await API.updateGlobalSettings({ themeColor: sw.getAttribute("data-color-pick") });
          if (res.state) this.setState(res.state);
        };
      });
      container.querySelectorAll("[data-pb-num]").forEach((sel) => {
        sel.onchange = async () => {
          const key = sel.getAttribute("data-pb-num");
          const res = await API.updateUserSettings({ playback: { [key]: Number(sel.value) } });
          if (res.state) this.setState(res.state);
        };
      });
      container.querySelectorAll("[data-pb-bool]").forEach((sw) => {
        sw.onclick = async () => {
          const key = sw.getAttribute("data-pb-bool");
          const cur = (this.state.playback || {})[key] !== false;
          const res = await API.updateUserSettings({ playback: { [key]: !cur } });
          if (res.state) this.setState(res.state);
        };
      });
      container.querySelectorAll("[data-src-sw]").forEach((sw) => {
        sw.onclick = async () => {
          const id = sw.getAttribute("data-src-sw");
          const cur = !!(this.state.sourceSwitches && this.state.sourceSwitches[id]);
          try {
            const res = await API.updateGlobalSettings({ sourceSwitches: { [id]: !cur } });
            if (res.state) this.setState(res.state);
          } catch (err) {
            toast(err.message);
          }
        };
      });

      const saveResPatch = async (patch) => {
        const cur = this.resourceSettings || {
          proxyMode: "auto",
          proxyUrl: "",
          catalogConcurrency: 3,
          catalogIntervalMs: 250,
          downloadConcurrency: 2,
          downloadBySource: false
        };
        try {
          this.resourceSettings = await API.rpc("saveResourceSettings", {
            settings: { ...cur, ...patch }
          });
          toast("设置已保存");
          this.render();
        } catch (err) {
          toast(err.message);
        }
      };

      const dlConc = container.querySelector("#rs-dl-conc");
      if (dlConc) dlConc.onchange = () => saveResPatch({ downloadConcurrency: Number(dlConc.value) });
      const dlBySrc = container.querySelector("#rs-dl-bysrc");
      if (dlBySrc) dlBySrc.onclick = () => saveResPatch({ downloadBySource: !(this.resourceSettings && this.resourceSettings.downloadBySource) });
      const catConc = container.querySelector("#rs-cat-conc");
      if (catConc) catConc.onchange = () => saveResPatch({ catalogConcurrency: Number(catConc.value) });
      const catIntv = container.querySelector("#rs-cat-intv");
      if (catIntv) catIntv.onchange = () => saveResPatch({ catalogIntervalMs: Number(catIntv.value) });

      const proxySave = container.querySelector("#proxy-save-btn");
      if (proxySave) {
        proxySave.onclick = () => {
          const mode = container.querySelector("#proxy-mode-sel").value;
          const url = container.querySelector("#proxy-url-inp").value.trim();
          saveResPatch({ proxyMode: mode, proxyUrl: url });
        };
      }

      const moveDirBtn = container.querySelector("#dl-move-dir-btn");
      if (moveDirBtn) {
        moveDirBtn.onclick = () => {
          const curDir = (this.storageInfo && this.storageInfo.directory) || "";
          const nextDir = window.prompt("输入服务端新的下载存储绝对路径：", curDir);
          if (!nextDir || nextDir.trim() === curDir) return;
          toast("正在迁移已下载视频目录，请稍候...");
          API.rpc("moveDownloads", { directory: nextDir.trim() })
            .then(() => {
              toast("下载目录迁移完成");
              this._loadSettingsAuxiliary();
            })
            .catch((e) => toast(e.message));
        };
      }

      const createUsr = container.querySelector("#usr-create-btn");
      if (createUsr) createUsr.onclick = () => this._openProfileEditorModal("");
      container.querySelectorAll("[data-edit-prof]").forEach((b) => {
        b.onclick = () => this._openProfileEditorModal(b.getAttribute("data-edit-prof"));
      });
      container.querySelectorAll("[data-del-prof]").forEach((b) => {
        b.onclick = async () => {
          const id = b.getAttribute("data-del-prof");
          if (!window.confirm("确定要删除该用户及其追剧和观看历史吗？")) return;
          try {
            const res = await API.deleteProfile(id);
            if (res.state) this.setState(res.state);
            toast("已删除用户");
          } catch (err) {
            toast(err.message);
          }
        };
      });

      const pwaInstallBtn = container.querySelector("#about-pwa-install");
      if (pwaInstallBtn) {
        pwaInstallBtn.onclick = async () => {
          const promptEvt = this.deferredInstallPrompt;
          if (!promptEvt) return;
          this.deferredInstallPrompt = null;
          try {
            promptEvt.prompt();
            await promptEvt.userChoice;
          } catch (_) {}
          this.render();
        };
      }
      const hgRegBtn = container.querySelector("#about-hg-reg");
      if (hgRegBtn) hgRegBtn.onclick = () => this._openHongguoDeviceModal();
      const chkUpdBtn = container.querySelector("#about-check-upd");
      if (chkUpdBtn) {
        chkUpdBtn.onclick = async () => {
          try {
            const res = await API.rpc("checkUpdate", { version: this.version });
            if (res && res.available) {
              this._openUpdateModal(res.version, res.url);
            } else {
              toast("当前已是最新版本");
            }
          } catch (err) {
            toast(err.message);
          }
        };
      }

      const backupFile = container.querySelector("#diag-backup-file");
      if (backupFile) {
        backupFile.onchange = async () => {
          const f = backupFile.files && backupFile.files[0];
          if (!f) return;
          try {
            const text = await f.text();
            const res = await API.importBackup(text);
            if (res.state) this.setState(res.state);
            toast("备份数据恢复成功");
          } catch (err) {
            toast(err.message || "备份导入失败");
          }
        };
      }

      const refLog = container.querySelector("#diag-refresh-log");
      if (refLog) refLog.onclick = () => this._loadSettingsAuxiliary();
      const cpyLog = container.querySelector("#diag-copy-log");
      if (cpyLog) {
        cpyLog.onclick = () => {
          navigator.clipboard
            .writeText(this.logLines.join("\n"))
            .then(() => toast("已复制诊断日志到剪贴板"))
            .catch(() => toast("复制失败，请手动选中复制"));
        };
      }
      const clrLog = container.querySelector("#diag-clear-log");
      if (clrLog) {
        clrLog.onclick = async () => {
          await API.clearLogs().catch(() => {});
          this._loadSettingsAuxiliary();
          toast("已清空内存日志");
        };
      }
    }
  }

  window.ZGJApp = new ZGJWebApp();
  window.addEventListener("DOMContentLoaded", () => {
    window.ZGJApp.boot();
  });
})();
