(function () {
  "use strict";

  const PINYIN_BOUNDARIES = [
    ["阿", "a"], ["芭", "b"], ["擦", "c"], ["搭", "d"], ["蛾", "e"],
    ["发", "f"], ["噶", "g"], ["哈", "h"], ["讥", "j"], ["喀", "k"],
    ["垃", "l"], ["妈", "m"], ["拿", "n"], ["哦", "o"], ["啪", "p"],
    ["期", "q"], ["然", "r"], ["撒", "s"], ["塌", "t"], ["挖", "w"],
    ["昔", "x"], ["压", "y"], ["匝", "z"]
  ];

  let zhCollator = null;
  try {
    zhCollator = new Intl.Collator("zh-Hans-u-co-pinyin");
  } catch (_) {
    zhCollator = null;
  }

  function charInitial(ch) {
    const code = ch.charCodeAt(0);
    if ((code >= 48 && code <= 57) || (code >= 65 && code <= 90) || (code >= 97 && code <= 122)) {
      return ch.toLowerCase();
    }
    if (code < 0x4e00 || code > 0x9fa5 || !zhCollator) {
      return "";
    }
    if (zhCollator.compare(ch, PINYIN_BOUNDARIES[0][0]) < 0) {
      return "";
    }
    for (let i = PINYIN_BOUNDARIES.length - 1; i >= 0; i--) {
      if (zhCollator.compare(ch, PINYIN_BOUNDARIES[i][0]) >= 0) {
        return PINYIN_BOUNDARIES[i][1];
      }
    }
    return "";
  }

  const initialsCache = new Map();

  function pinyinInitials(text) {
    const raw = String(text || "");
    if (!raw) return "";
    if (initialsCache.has(raw)) return initialsCache.get(raw);
    let out = "";
    for (const ch of raw) {
      out += charInitial(ch);
    }
    if (initialsCache.size > 4000) {
      initialsCache.clear();
    }
    initialsCache.set(raw, out);
    return out;
  }

  function esc(val) {
    return String(val == null ? "" : val)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  function formatTime(seconds) {
    const s = Math.max(0, Math.floor(Number(seconds) || 0));
    const hrs = Math.floor(s / 3600);
    const mins = Math.floor((s % 3600) / 60);
    const secs = s % 60;
    const mm = String(mins).padStart(2, "0");
    const ss = String(secs).padStart(2, "0");
    return hrs > 0 ? `${hrs}:${mm}:${ss}` : `${mm}:${ss}`;
  }

  function formatBytes(bytes) {
    const n = Number(bytes) || 0;
    if (n <= 0) return "0 B";
    if (n < 1024) return `${n} B`;
    if (n < 1048576) return `${(n / 1024).toFixed(1)} KB`;
    if (n < 1073741824) return `${(n / 1048576).toFixed(1)} MB`;
    return `${(n / 1073741824).toFixed(2)} GB`;
  }

  function qualityLabel(q) {
    const n = Number(q) || 0;
    return n <= 0 ? "自动" : `${n}P`;
  }

  function dayBucket(isoStr) {
    if (!isoStr) return "更早";
    const dt = new Date(isoStr);
    if (Number.isNaN(dt.getTime())) return "更早";
    const now = new Date();
    const todayStart = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
    const targetStart = new Date(dt.getFullYear(), dt.getMonth(), dt.getDate()).getTime();
    const diffDays = Math.round((todayStart - targetStart) / 86400000);
    if (diffDays <= 0) return "今天";
    if (diffDays === 1) return "昨天";
    return "更早";
  }

  const PLACEHOLDER_COVER_URL = "/api/cover?placeholder=1";

  function coverURL(drama, force) {
    if (!drama || !drama.id || !drama.source) {
      return PLACEHOLDER_COVER_URL;
    }
    const q = new URLSearchParams({
      id: String(drama.id),
      source: String(drama.source),
      cover: String(drama.cover || ""),
      title: String(drama.title || "")
    });
    if (force) q.set("force", "1");
    return `/api/cover?${q.toString()}`;
  }

  function matchesQuery(drama, query) {
    const q = String(query || "").trim().toLowerCase();
    if (!q) return true;
    const title = String(drama.title || "").toLowerCase();
    if (title.includes(q)) return true;
    const initials = pinyinInitials(drama.title || "");
    if (initials.includes(q)) return true;
    const cat = String(drama.category || "").toLowerCase();
    if (cat.includes(q)) return true;
    const desc = String(drama.description || "").toLowerCase();
    if (desc.includes(q)) return true;
    if (Array.isArray(drama.tags) && drama.tags.some((t) => String(t).toLowerCase().includes(q))) {
      return true;
    }
    return false;
  }

  function normalizeDrama(raw) {
    if (!raw || typeof raw !== "object") {
      return {
        metadataSchema: 1,
        id: "",
        source: "hongguo",
        sourceId: "",
        title: "短剧",
        description: "",
        cover: "",
        episodes: 0,
        category: "",
        vip: null,
        heat: "",
        views: "",
        onlineDate: "",
        tags: [],
        releaseStatus: ""
      };
    }
    return {
      metadataSchema: 1,
      id: String(raw.id || ""),
      source: String(raw.source || "hongguo"),
      sourceId: String(raw.sourceId || ""),
      title: String(raw.title || "短剧"),
      description: String(raw.description || ""),
      cover: String(raw.cover || ""),
      episodes: Number(raw.episodes) || 0,
      category: String(raw.category || ""),
      vip: raw.vip === true ? true : raw.vip === false ? false : null,
      heat: raw.heat != null ? String(raw.heat) : "",
      views: raw.views != null ? String(raw.views) : "",
      onlineDate: String(raw.onlineDate || ""),
      tags: Array.isArray(raw.tags) ? raw.tags.map(String) : [],
      releaseStatus: String(raw.releaseStatus || "")
    };
  }

  function mergeDrama(base, fresh) {
    const a = normalizeDrama(base);
    const b = normalizeDrama(fresh);
    if (!a.id || a.id !== b.id) return b;
    const genericCats = new Set(["短剧", "真人剧", "漫剧", "AI剧", "AI 剧", "AI短剧", "AI 短剧", "AI漫剧", "AI 漫剧"]);
    return {
      metadataSchema: 1,
      id: a.id,
      source: b.source || a.source,
      sourceId: b.sourceId || a.sourceId,
      title: (!b.title || b.title === "短剧") ? a.title : b.title,
      description: b.description || a.description,
      cover: b.cover || a.cover,
      episodes: b.episodes > 0 ? b.episodes : a.episodes,
      category: (!b.category || (genericCats.has(b.category) && a.category && !genericCats.has(a.category))) ? a.category : b.category,
      vip: b.vip !== null ? b.vip : a.vip,
      heat: b.heat || a.heat,
      views: b.views || a.views,
      onlineDate: b.onlineDate || a.onlineDate,
      tags: b.tags.length > 0 ? b.tags : a.tags,
      releaseStatus: (!b.releaseStatus || b.releaseStatus === "unknown") ? a.releaseStatus : b.releaseStatus
    };
  }

  function releaseLabel(status) {
    if (status === "finished" || status === "completed") return "已完结";
    if (status === "ongoing") return "连载中";
    return "";
  }

  function toast(message, durationMs) {
    const root = document.getElementById("toast-root");
    if (!root || !message) return;
    const el = document.createElement("div");
    el.className = "toast";
    el.textContent = String(message);
    root.appendChild(el);
    setTimeout(() => {
      el.style.opacity = "0";
      el.style.transition = "opacity 0.2s";
      setTimeout(() => el.remove(), 220);
    }, durationMs || 2600);
  }

  async function requestJSON(url, options) {
    const resp = await fetch(url, {
      credentials: "same-origin",
      ...options
    });
    let body = null;
    try {
      body = await resp.json();
    } catch (_) {
      body = null;
    }
    if (!resp.ok || !body || body.ok === false) {
      const msg = (body && body.error) ? body.error : `请求失败 (${resp.status})`;
      const err = new Error(msg);
      err.code = (body && body.code) || "";
      err.status = resp.status;
      throw err;
    }
    return body;
  }

  const ZGJApi = {
    bootstrap() {
      return requestJSON("/api/bootstrap", { method: "GET" });
    },

    async rpc(action, params, signal) {
      const body = await requestJSON("/api/rpc", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ...params, action }),
        signal
      });
      return body.data;
    },

    cancelRead(sessionScope) {
      return fetch("/api/rpc", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action: "cancelRead", session: sessionScope })
      }).catch(() => {});
    },

    getState() {
      return requestJSON("/api/store/state", { method: "GET" });
    },

    switchProfile(id, pin) {
      return requestJSON("/api/store/switch-profile", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ id, pin: pin || "" })
      });
    },

    lockProfile() {
      return requestJSON("/api/store/lock", { method: "POST", body: "{}" });
    },

    saveProfile(payload) {
      return requestJSON("/api/store/profile", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
      });
    },

    deleteProfile(id) {
      return requestJSON(`/api/store/profile?id=${encodeURIComponent(id)}`, {
        method: "DELETE"
      });
    },

    updateGlobalSettings(patch) {
      return requestJSON("/api/store/settings", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(patch)
      });
    },

    updateUserSettings(patch) {
      return requestJSON("/api/store/user-settings", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(patch)
      });
    },

    saveWatch(entry) {
      return requestJSON("/api/store/watch", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(entry)
      });
    },

    deleteHistory(ids) {
      return requestJSON("/api/store/history-delete", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ids })
      });
    },

    toggleFollow(drama, follow) {
      return requestJSON("/api/store/follow", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ drama: normalizeDrama(drama), follow: !!follow })
      });
    },

    unfollowBatch(ids) {
      return requestJSON("/api/store/follow", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ids })
      });
    },

    markFollowRead(id, episodes) {
      return requestJSON("/api/store/follow", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ markRead: id, episodes: Number(episodes) || 0 })
      });
    },

    checkFollowUpdates() {
      return requestJSON("/api/store/follow-check", {
        method: "POST",
        body: "{}"
      });
    },

    importBackup(rawText) {
      return requestJSON("/api/store/backup", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: rawText
      });
    },

    downloadFileURL(jobId) {
      return `/api/downloads/file/${encodeURIComponent(jobId)}`;
    },

    async mergeDownloads(jobIds) {
      const resp = await fetch("/api/downloads/merge", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ jobIds })
      });
      if (!resp.ok) {
        let msg = "合并导出失败";
        try {
          const errJson = await resp.json();
          if (errJson && errJson.error) msg = errJson.error;
        } catch (_) {}
        throw new Error(msg);
      }
      const cd = resp.headers.get("Content-Disposition") || "";
      let filename = "merged.mp4";
      const match = cd.match(/filename\*=UTF-8''([^;]+)/i);
      if (match && match[1]) {
        try {
          filename = decodeURIComponent(match[1]);
        } catch (_) {}
      }
      const blob = await resp.blob();
      const blobUrl = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = blobUrl;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(blobUrl), 10000);
    },

    getLogs() {
      return requestJSON("/api/logs", { method: "GET" });
    },

    addLog(message) {
      return fetch("/api/logs", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ message })
      }).catch(() => {});
    },

    clearLogs() {
      return requestJSON("/api/logs", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ clear: true })
      });
    }
  };

  function b64ToBytes(b64) {
    const bin = atob(b64 || "");
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) {
      out[i] = bin.charCodeAt(i);
    }
    return out;
  }

  function renderSourceWithTransforms(source, srcW, srcH, transforms) {
    const ops = Array.isArray(transforms) ? transforms : [];
    let curW = Math.max(1, Number(srcW) || 360);
    let curH = Math.max(1, Number(srcH) || 480);
    for (const op of ops) {
      if (op === "rot90ccw" || op === "rot90cw") {
        const tmp = curW;
        curW = curH;
        curH = tmp;
      }
    }
    const scale = Math.min(1, 800 / Math.max(1, curW, curH));
    const outW = Math.max(1, Math.round(curW * scale));
    const outH = Math.max(1, Math.round(curH * scale));
    const canvas = document.createElement("canvas");
    canvas.width = outW;
    canvas.height = outH;
    const ctx = canvas.getContext("2d");
    if (ops.length === 0) {
      ctx.drawImage(source, 0, 0, outW, outH);
      return canvas;
    }
    ctx.save();
    ctx.translate(outW / 2, outH / 2);
    ctx.scale(scale, scale);
    for (let i = ops.length - 1; i >= 0; i--) {
      switch (ops[i]) {
        case "hflip":
          ctx.scale(-1, 1);
          break;
        case "vflip":
          ctx.scale(1, -1);
          break;
        case "rot90cw":
          ctx.rotate(Math.PI / 2);
          break;
        case "rot90ccw":
          ctx.rotate(-Math.PI / 2);
          break;
        case "rot180":
          ctx.rotate(Math.PI);
          break;
      }
    }
    ctx.drawImage(source, -srcW / 2, -srcH / 2, srcW, srcH);
    ctx.restore();
    return canvas;
  }

  function canvasToJPEGBlob(canvas, quality) {
    return new Promise((resolve, reject) => {
      canvas.toBlob(
        (blob) => {
          if (blob && blob.size > 0) resolve(blob);
          else reject(new Error("海报导出失败"));
        },
        "image/jpeg",
        quality || 0.88
      );
    });
  }

  function uploadCompatJPEG(token, jpegBlob) {
    if (!token || !jpegBlob || jpegBlob.size < 16) return;
    fetch(`/api/cover-compatible?token=${encodeURIComponent(token)}`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "image/jpeg" },
      body: jpegBlob
    }).catch(() => {});
  }

  let videoDecodeActive = 0;
  const videoDecodeWaiters = [];

  function acquireVideoSlot() {
    if (videoDecodeActive < 2) {
      videoDecodeActive++;
      return Promise.resolve();
    }
    return new Promise((resolve) => videoDecodeWaiters.push(resolve));
  }

  function releaseVideoSlot() {
    const next = videoDecodeWaiters.shift();
    if (next) {
      next();
    } else {
      videoDecodeActive = Math.max(0, videoDecodeActive - 1);
    }
  }

  let heic2anyPromise = null;

  function loadHeic2Any() {
    if (typeof window.heic2any === "function") {
      return Promise.resolve(window.heic2any);
    }
    if (heic2anyPromise) return heic2anyPromise;
    const urls = [
      "https://registry.npmmirror.com/heic2any/0.0.4/files/dist/heic2any.min.js",
      "https://cdn.jsdelivr.net/npm/heic2any@0.0.4/dist/heic2any.min.js",
      "https://unpkg.com/heic2any@0.0.4/dist/heic2any.min.js"
    ];
    heic2anyPromise = (async () => {
      for (const src of urls) {
        try {
          await new Promise((resolve, reject) => {
            const s = document.createElement("script");
            s.src = src;
            s.async = true;
            const timer = setTimeout(() => {
              s.remove();
              reject(new Error("timeout"));
            }, 9000);
            s.onload = () => {
              clearTimeout(timer);
              resolve();
            };
            s.onerror = () => {
              clearTimeout(timer);
              s.remove();
              reject(new Error("load failed"));
            };
            document.head.appendChild(s);
          });
          if (typeof window.heic2any === "function") {
            return window.heic2any;
          }
        } catch (_) {}
      }
      heic2anyPromise = null;
      throw new Error("无法加载 HEIC 解码组件");
    })();
    return heic2anyPromise;
  }

  async function decodeHEICViaWebCodecs(fetchUrl, fallbackToken) {
    if (typeof window.VideoDecoder !== "function" || typeof window.EncodedVideoChunk !== "function") {
      return null;
    }
    const sep = fetchUrl.includes("?") ? "&" : "?";
    const info = await requestJSON(`${fetchUrl}${sep}format=hevc-frame`, {
      method: "GET",
      headers: { "X-Cover-Fetch": "1" }
    });
    const hvcc = b64ToBytes(info.hvcc);
    const sample = b64ToBytes(info.sample);
    const candidates = [info.codec, "hvc1.1.6.L93.B0", "hvc1.2.4.L120.B0"].filter(Boolean);
    let supportedCfg = null;
    for (const codec of candidates) {
      try {
        const check = await VideoDecoder.isConfigSupported({
          codec,
          description: hvcc,
          codedWidth: Number(info.width) || undefined,
          codedHeight: Number(info.height) || undefined
        });
        if (check && check.supported) {
          supportedCfg = check.config || { codec, description: hvcc };
          break;
        }
      } catch (_) {}
    }
    if (!supportedCfg) return null;

    const jpg = await new Promise((resolve, reject) => {
      let settled = false;
      let decoder = null;
      const timer = setTimeout(() => {
        if (settled) return;
        settled = true;
        try {
          if (decoder && decoder.state !== "closed") decoder.close();
        } catch (_) {}
        reject(new Error("WebCodecs timeout"));
      }, 6000);

      decoder = new VideoDecoder({
        output: async (vFrame) => {
          if (settled) {
            try { vFrame.close(); } catch (_) {}
            return;
          }
          settled = true;
          clearTimeout(timer);
          try {
            const w = vFrame.displayWidth || vFrame.codedWidth || Number(info.width) || 360;
            const h = vFrame.displayHeight || vFrame.codedHeight || Number(info.height) || 480;
            const canvas = renderSourceWithTransforms(vFrame, w, h, info.transforms || []);
            vFrame.close();
            try {
              if (decoder.state !== "closed") decoder.close();
            } catch (_) {}
            const blob = await canvasToJPEGBlob(canvas, 0.88);
            resolve(blob);
          } catch (err) {
            try { vFrame.close(); } catch (_) {}
            try {
              if (decoder.state !== "closed") decoder.close();
            } catch (_) {}
            reject(err);
          }
        },
        error: (err) => {
          if (settled) return;
          settled = true;
          clearTimeout(timer);
          try {
            if (decoder && decoder.state !== "closed") decoder.close();
          } catch (_) {}
          reject(err || new Error("WebCodecs error"));
        }
      });

      try {
        decoder.configure(supportedCfg);
        decoder.decode(new EncodedVideoChunk({ type: "key", timestamp: 0, data: sample }));
        decoder
          .flush()
          .then(() => {
            if (!settled) {
              settled = true;
              clearTimeout(timer);
              try {
                if (decoder.state !== "closed") decoder.close();
              } catch (_) {}
              reject(new Error("WebCodecs empty"));
            }
          })
          .catch((err) => {
            if (!settled) {
              settled = true;
              clearTimeout(timer);
              try {
                if (decoder.state !== "closed") decoder.close();
              } catch (_) {}
              reject(err);
            }
          });
      } catch (err) {
        if (!settled) {
          settled = true;
          clearTimeout(timer);
          try {
            if (decoder.state !== "closed") decoder.close();
          } catch (_) {}
          reject(err);
        }
      }
    });

    uploadCompatJPEG(info.compatToken || fallbackToken, jpg);
    return jpg;
  }

  async function decodeHEICViaVideoMP4(fetchUrl, fallbackToken) {
    if (typeof document === "undefined") return null;
    await acquireVideoSlot();
    let mp4Url = "";
    const vid = document.createElement("video");
    try {
      const sep = fetchUrl.includes("?") ? "&" : "?";
      const resp = await fetch(`${fetchUrl}${sep}format=hevc-mp4`, {
        credentials: "same-origin",
        headers: { "X-Cover-Fetch": "1" }
      });
      if (!resp.ok) return null;
      const compatToken = resp.headers.get("X-Cover-Compat-Token") || fallbackToken;
      const trHeader = resp.headers.get("X-HEIC-Transforms") || "";
      const transforms = trHeader
        ? trHeader
            .split(",")
            .map((s) => s.trim())
            .filter(Boolean)
        : [];
      const mp4Blob = await resp.blob();
      if (!mp4Blob || mp4Blob.size < 32) return null;
      mp4Url = URL.createObjectURL(mp4Blob);

      vid.muted = true;
      vid.playsInline = true;
      vid.preload = "auto";
      vid.style.position = "fixed";
      vid.style.width = "2px";
      vid.style.height = "2px";
      vid.style.opacity = "0";
      vid.style.pointerEvents = "none";
      vid.style.left = "-9999px";
      if (document.body) document.body.appendChild(vid);

      await new Promise((resolve, reject) => {
        let done = false;
        const timer = setTimeout(() => {
          if (done) return;
          done = true;
          reject(new Error("video mp4 decode timeout"));
        }, 6000);
        const finish = () => {
          if (done) return;
          done = true;
          clearTimeout(timer);
          resolve();
        };
        vid.onerror = () => {
          if (done) return;
          done = true;
          clearTimeout(timer);
          reject(new Error("video mp4 decode error"));
        };
        vid.onloadeddata = () => {
          if (vid.readyState >= 2 && vid.videoWidth > 0) {
            vid.onseeked = finish;
            try {
              vid.currentTime = 0.1;
            } catch (_) {
              finish();
            }
          }
        };
        vid.src = mp4Url;
        vid.load();
      });

      if (!vid.videoWidth || !vid.videoHeight) return null;
      const canvas = renderSourceWithTransforms(vid, vid.videoWidth, vid.videoHeight, transforms);
      const jpg = await canvasToJPEGBlob(canvas, 0.88);
      uploadCompatJPEG(compatToken, jpg);
      return jpg;
    } finally {
      try {
        vid.pause();
        vid.removeAttribute("src");
        vid.load();
        vid.remove();
      } catch (_) {}
      if (mp4Url) URL.revokeObjectURL(mp4Url);
      releaseVideoSlot();
    }
  }

  async function decodeHEICCover(fetchUrl, rawBlob, compatToken) {
    if (typeof createImageBitmap === "function") {
      try {
        const bmp = await createImageBitmap(rawBlob);
        try {
          const canvas = renderSourceWithTransforms(bmp, bmp.width, bmp.height, []);
          const jpg = await canvasToJPEGBlob(canvas, 0.88);
          uploadCompatJPEG(compatToken, jpg);
          return jpg;
        } finally {
          if (typeof bmp.close === "function") bmp.close();
        }
      } catch (_) {}
    }

    try {
      const wcJpg = await decodeHEICViaWebCodecs(fetchUrl, compatToken);
      if (wcJpg) return wcJpg;
    } catch (_) {}

    try {
      const mp4Jpg = await decodeHEICViaVideoMP4(fetchUrl, compatToken);
      if (mp4Jpg) return mp4Jpg;
    } catch (_) {}

    const conv = await loadHeic2Any();
    const res = await conv({ blob: rawBlob, toType: "image/jpeg", quality: 0.86 });
    const jpg = Array.isArray(res) ? res[0] : res;
    if (!jpg || jpg.size === 0) {
      throw new Error("HEIC 转换结果为空");
    }
    uploadCompatJPEG(compatToken, jpg);
    return jpg;
  }

  const CoverHydrator = (function () {
    const MAX_CONCURRENT = 4;
    const MAX_CACHE = 600;
    const FAIL_COOLDOWN_MS = 15000;

    const resolvedMap = new Map();
    const inflightMap = new Map();
    const failedAtMap = new Map();
    const pendingQueue = [];
    const queuedKeys = new Set();

    let activeWorkers = 0;
    let disableImages = false;
    let dramaLookup = null;
    let dramaUpdater = null;
    let observer = null;

    function isBlobReferenced(blobUrl) {
      for (const v of resolvedMap.values()) {
        if (v === blobUrl) return true;
      }
      const imgs = document.querySelectorAll("img[data-cover-url]");
      for (let i = 0; i < imgs.length; i++) {
        if (imgs[i].getAttribute("src") === blobUrl) return true;
      }
      return false;
    }

    function storeResolved(key, blobUrl) {
      if (!key || !blobUrl) return;
      if (resolvedMap.has(key)) {
        resolvedMap.delete(key);
      }
      resolvedMap.set(key, blobUrl);
      while (resolvedMap.size > MAX_CACHE) {
        const oldestKey = resolvedMap.keys().next().value;
        const oldestUrl = resolvedMap.get(oldestKey);
        resolvedMap.delete(oldestKey);
        if (oldestUrl && oldestUrl.startsWith("blob:") && !isBlobReferenced(oldestUrl)) {
          URL.revokeObjectURL(oldestUrl);
        }
      }
    }

    function getResolved(key) {
      if (!key || !resolvedMap.has(key)) return "";
      const val = resolvedMap.get(key);
      resolvedMap.delete(key);
      resolvedMap.set(key, val);
      return val;
    }

    function applyToDOM(key, url) {
      if (!key || !url) return;
      const imgs = document.querySelectorAll("img[data-cover-url]");
      for (let i = 0; i < imgs.length; i++) {
        const img = imgs[i];
        if (img.getAttribute("data-cover-url") === key) {
          if (img.getAttribute("src") !== url) {
            img.src = url;
          }
        }
      }
      const bgs = document.querySelectorAll("[data-cover-bg]");
      for (let i = 0; i < bgs.length; i++) {
        const el = bgs[i];
        if (el.getAttribute("data-cover-bg") === key) {
          el.style.backgroundImage = `url("${url}")`;
        }
      }
    }

    async function supplementCoverViaMetadata(dramaId) {
      if (!dramaId || typeof dramaLookup !== "function") return null;
      const d = dramaLookup(dramaId);
      if (!d || !d.id || !d.source) return null;
      try {
        const fresh = await ZGJApi.rpc("metadata", {
          drama: d,
          session: `cover-meta-${dramaId}`
        });
        if (fresh && fresh.cover) {
          const merged = mergeDrama(d, fresh);
          if (typeof dramaUpdater === "function") {
            dramaUpdater(merged);
          }
          return merged;
        }
      } catch (_) {}
      return null;
    }

    async function fetchAndResolve(key, dramaId, forceReload) {
      const u = new URL(key, window.location.origin);
      if (forceReload) {
        u.searchParams.set("force", "1");
      }
      let supplementedDrama = null;
      if (!u.searchParams.get("cover") && dramaId) {
        if (typeof dramaLookup === "function") {
          const localD = dramaLookup(dramaId);
          if (localD && localD.cover) {
            u.searchParams.set("cover", localD.cover);
          }
        }
        if (!u.searchParams.get("cover")) {
          supplementedDrama = await supplementCoverViaMetadata(dramaId);
          if (supplementedDrama && supplementedDrama.cover) {
            u.searchParams.set("cover", supplementedDrama.cover);
          }
        }
      }

      let fetchUrl = `${u.pathname}${u.search}`;
      let resp = null;
      try {
        resp = await fetch(fetchUrl, {
          credentials: "same-origin",
          headers: { "X-Cover-Fetch": "1" }
        });
      } catch (_) {
        resp = null;
      }

      if (!resp || !resp.ok) {
        await new Promise((r) => setTimeout(r, 550));
        if (!supplementedDrama && dramaId) {
          supplementedDrama = await supplementCoverViaMetadata(dramaId);
          if (supplementedDrama && supplementedDrama.cover) {
            u.searchParams.set("cover", supplementedDrama.cover);
          }
        }
        u.searchParams.set("force", "1");
        fetchUrl = `${u.pathname}${u.search}`;
        resp = await fetch(fetchUrl, {
          credentials: "same-origin",
          headers: { "X-Cover-Fetch": "1" }
        });
        if (!resp.ok) {
          throw new Error(`海报加载失败 (${resp.status})`);
        }
      }

      const ct = (resp.headers.get("Content-Type") || "").toLowerCase();
      const compatToken = resp.headers.get("X-Cover-Compat-Token") || "";
      const rawBlob = await resp.blob();
      if (!rawBlob || rawBlob.size === 0) {
        throw new Error("海报数据为空");
      }

      let finalBlob = rawBlob;
      if (ct.includes("image/heic") || ct.includes("image/heif")) {
        finalBlob = await decodeHEICCover(fetchUrl, rawBlob, compatToken);
      }

      const finalUrl = URL.createObjectURL(finalBlob);
      storeResolved(key, finalUrl);
      if (supplementedDrama) {
        const freshKey = coverURL(supplementedDrama, false);
        if (freshKey && freshKey !== key) {
          storeResolved(freshKey, finalUrl);
          applyToDOM(freshKey, finalUrl);
        }
      }
      failedAtMap.delete(key);
      applyToDOM(key, finalUrl);
      return finalUrl;
    }

    function hydrateKey(key, dramaId, forceReload) {
      if (!key || key === PLACEHOLDER_COVER_URL || disableImages) {
        return Promise.resolve(PLACEHOLDER_COVER_URL);
      }
      if (!forceReload) {
        const cached = getResolved(key);
        if (cached) {
          applyToDOM(key, cached);
          return Promise.resolve(cached);
        }
      }
      if (inflightMap.has(key)) {
        return inflightMap.get(key);
      }
      const p = fetchAndResolve(key, dramaId, !!forceReload)
        .catch(() => {
          failedAtMap.set(key, Date.now());
          applyToDOM(key, PLACEHOLDER_COVER_URL);
          return PLACEHOLDER_COVER_URL;
        })
        .finally(() => {
          inflightMap.delete(key);
        });
      inflightMap.set(key, p);
      return p;
    }

    function pumpQueue() {
      while (activeWorkers < MAX_CONCURRENT && pendingQueue.length > 0) {
        const item = pendingQueue.shift();
        queuedKeys.delete(item.key);
        if (!item.force) {
          const cached = getResolved(item.key);
          if (cached) {
            applyToDOM(item.key, cached);
            continue;
          }
          const lastFail = failedAtMap.get(item.key) || 0;
          if (lastFail > 0 && Date.now() - lastFail < FAIL_COOLDOWN_MS) {
            continue;
          }
        }
        activeWorkers++;
        hydrateKey(item.key, item.dramaId, item.force).finally(() => {
          activeWorkers = Math.max(0, activeWorkers - 1);
          pumpQueue();
        });
      }
    }

    function enqueue(key, dramaId, highPriority, force) {
      if (!key || key === PLACEHOLDER_COVER_URL || disableImages) return;
      if (!force) {
        const cached = getResolved(key);
        if (cached) {
          applyToDOM(key, cached);
          return;
        }
        const lastFail = failedAtMap.get(key) || 0;
        if (lastFail > 0 && Date.now() - lastFail < FAIL_COOLDOWN_MS) {
          return;
        }
      }
      if (inflightMap.has(key) || queuedKeys.has(key)) return;
      queuedKeys.add(key);
      const entry = { key, dramaId: dramaId || "", force: !!force };
      if (highPriority) {
        pendingQueue.unshift(entry);
      } else {
        pendingQueue.push(entry);
      }
      pumpQueue();
    }

    function ensureObserver() {
      if (observer || typeof IntersectionObserver !== "function") return observer;
      observer = new IntersectionObserver(
        (entries) => {
          for (const ent of entries) {
            if (!ent.isIntersecting) continue;
            const el = ent.target;
            observer.unobserve(el);
            const key = el.getAttribute("data-cover-url") || el.getAttribute("data-cover-bg") || "";
            const dramaId = el.getAttribute("data-drama-id") || "";
            const hi = el.getAttribute("data-cover-priority") === "1";
            enqueue(key, dramaId, hi, false);
          }
        },
        { root: null, rootMargin: "360px 0px" }
      );
      return observer;
    }

    function scan(rootEl) {
      if (disableImages) return;
      const scope = rootEl || document;
      const obs = ensureObserver();
      const nodes = scope.querySelectorAll("[data-cover-url], [data-cover-bg]");
      for (let i = 0; i < nodes.length; i++) {
        const el = nodes[i];
        const key = el.getAttribute("data-cover-url") || el.getAttribute("data-cover-bg") || "";
        if (!key || key === PLACEHOLDER_COVER_URL) continue;
        const cached = getResolved(key);
        if (cached) {
          if (el.hasAttribute("data-cover-url")) {
            if (el.getAttribute("src") !== cached) el.src = cached;
          } else {
            el.style.backgroundImage = `url("${cached}")`;
          }
          continue;
        }
        const dramaId = el.getAttribute("data-drama-id") || "";
        const hi = el.getAttribute("data-cover-priority") === "1";
        if (hi || !obs) {
          enqueue(key, dramaId, hi, false);
        } else {
          obs.observe(el);
        }
      }
    }

    if (typeof window !== "undefined" && window.addEventListener) {
      window.addEventListener(
        "error",
        (ev) => {
          const el = ev && ev.target;
          if (!el || el.tagName !== "IMG") return;
          const curSrc = el.getAttribute("src") || "";
          if (!curSrc || curSrc === PLACEHOLDER_COVER_URL || curSrc === "/icon.svg") return;
          el.src = PLACEHOLDER_COVER_URL;
          const key = el.getAttribute("data-cover-url") || "";
          if (key && !el.hasAttribute("data-cover-repaired")) {
            el.setAttribute("data-cover-repaired", "1");
            resolvedMap.delete(key);
            enqueue(key, el.getAttribute("data-drama-id") || "", true, true);
          }
        },
        true
      );
    }

    return {
      configure(opts) {
        if (!opts) return;
        if (typeof opts.disableImages === "boolean") disableImages = opts.disableImages;
        if (typeof opts.dramaLookup === "function") dramaLookup = opts.dramaLookup;
        if (typeof opts.dramaUpdater === "function") dramaUpdater = opts.dramaUpdater;
      },
      getResolved,
      scan,
      forceRefresh(key, dramaId) {
        if (!key) return;
        resolvedMap.delete(key);
        failedAtMap.delete(key);
        enqueue(key, dramaId || "", true, true);
      },
      isDisabled() {
        return disableImages;
      }
    };
  })();

  function resolvedCoverURL(drama) {
    const key = coverURL(drama, false);
    if (key === PLACEHOLDER_COVER_URL || CoverHydrator.isDisabled()) {
      return PLACEHOLDER_COVER_URL;
    }
    return CoverHydrator.getResolved(key) || PLACEHOLDER_COVER_URL;
  }

  window.ZGJUtils = {
    esc,
    formatTime,
    formatBytes,
    qualityLabel,
    dayBucket,
    coverURL,
    resolvedCoverURL,
    CoverHydrator,
    pinyinInitials,
    matchesQuery,
    normalizeDrama,
    mergeDrama,
    releaseLabel,
    toast
  };

  window.ZGJApi = ZGJApi;
})();
