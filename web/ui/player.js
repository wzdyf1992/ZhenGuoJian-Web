(function () {
  "use strict";

  const { esc, formatTime, normalizeDrama, toast } = window.ZGJUtils;
  const API = window.ZGJApi;

  const SPEEDS = [0.5, 0.75, 1.0, 1.25, 1.5, 2.0, 3.0];
  const DEFAULT_VOLUME = 1 / 3;
  const VOLUME_STORAGE_KEY = "zgj_player_volume";
  const DANMAKU_WINDOW_MS = 30000;

  const HLS_CDNS = [
    "https://registry.npmmirror.com/hls.js/1.5.15/files/dist/hls.min.js",
    "https://cdn.jsdelivr.net/npm/hls.js@1.5.15/dist/hls.min.js",
    "https://cdn.staticfile.net/hls.js/1.5.15/hls.min.js",
    "https://unpkg.com/hls.js@1.5.15/dist/hls.min.js"
  ];

  let hlsLoaderPromise = null;

  function loadHlsLibrary() {
    if (window.Hls) return Promise.resolve(window.Hls);
    if (hlsLoaderPromise) return Promise.resolve(hlsLoaderPromise);
    hlsLoaderPromise = new Promise((resolve) => {
      let idx = 0;
      const tryNext = () => {
        if (window.Hls) {
          resolve(window.Hls);
          return;
        }
        if (idx >= HLS_CDNS.length) {
          hlsLoaderPromise = null;
          resolve(null);
          return;
        }
        const s = document.createElement("script");
        let settled = false;
        const timer = setTimeout(() => {
          if (settled) return;
          settled = true;
          s.onload = null;
          s.onerror = null;
          s.remove();
          tryNext();
        }, 6000);
        s.src = HLS_CDNS[idx++];
        s.async = true;
        s.onload = () => {
          if (settled) return;
          settled = true;
          clearTimeout(timer);
          if (window.Hls) {
            resolve(window.Hls);
          } else {
            s.remove();
            tryNext();
          }
        };
        s.onerror = () => {
          if (settled) return;
          settled = true;
          clearTimeout(timer);
          s.remove();
          tryNext();
        };
        document.head.appendChild(s);
      };
      tryNext();
    });
    return hlsLoaderPromise;
  }

  class ZGJPlayerController {
    constructor() {
      this.active = false;
      this.drama = null;
      this.episodes = [];
      this.currentIndex = 1;
      this.plan = null;
      this.hls = null;
      this.video = null;
      this.standbyVideo = null;
      this.standbyHls = null;
      this.standbyKey = "";
      this.standbyPlan = null;
      this.standbyReady = false;
      this.standbyFailed = false;
      this.standbyEpoch = 0;
      this.ignoringStandbyError = false;
      this.shell = null;
      this.loading = false;
      this.errorText = "";
      this.locked = false;
      this.wideMode = false;
      this.mobileEpsOpen = false;
      this.pseudoFullscreen = false;
      this.wantFullscreen = false;
      this.controlsVisible = true;
      this.hideControlsTimer = null;
      this.loadingDelayTimer = null;
      this.preloadDelayTimer = null;
      this._hudTimer = null;
      this._hudFlashUntil = 0;
      this.progressSaveTimer = null;
      this.fsSyncTimers = [];
      this.lastSavedPos = -1;
      this.resumePosition = 0;
      this.preferredQuality = 0;
      this.speed = 1.0;
      this.volume = DEFAULT_VOLUME;
      this.muted = false;
      this.brightness = 1.0;
      this.danmakuEnabled = true;
      this.danmakuBuckets = new Map();
      this.danmakuFired = new Set();
      this.danmakuLoadingKey = "";
      this.danmakuTracks = new Array(8).fill(0);
      this.preloadedKey = "";
      this.preloadedPlan = null;
      this.fallbackCount = 0;
      this.onStateUpdate = null;
      this.onEpisodeChange = null;
      this.resolveEpoch = 0;
      this.attachEpoch = 0;
      this.ignoringMediaError = false;
      this.handlingFailureEpoch = -1;
      this.triedTranscode = false;
      this.probedHlsForUrl = "";

      this._boundKeyDown = this._handleKeyDown.bind(this);
      this._boundFullscreenChange = () => {
        if (!this._isNativeFullscreen() && !this.pseudoFullscreen) {
          this.wantFullscreen = false;
          this._unlockScreenOrientation();
        }
        this._syncFullscreenState();
        this._scheduleFullscreenResync();
      };
      this._boundViewportResize = () => {
        if (this.active) {
          this._syncFullscreenState();
        }
      };
      window.addEventListener("keydown", this._boundKeyDown);
      window.addEventListener("resize", this._boundViewportResize);
      window.addEventListener("orientationchange", this._boundViewportResize);
      if (window.visualViewport) {
        window.visualViewport.addEventListener("resize", this._boundViewportResize);
      }
      document.addEventListener("fullscreenchange", this._boundFullscreenChange);
      document.addEventListener("webkitfullscreenchange", this._boundFullscreenChange);
    }

    _isVolumeReadOnly() {
      if (typeof this._volReadOnlyCached === "boolean") return this._volReadOnlyCached;
      try {
        const probe = document.createElement("video");
        probe.volume = 0.5;
        this._volReadOnlyCached = Math.abs(probe.volume - 0.5) > 0.05;
      } catch (_) {
        this._volReadOnlyCached = false;
      }
      return this._volReadOnlyCached;
    }

    _applyBrightness(val) {
      const num = Number(val);
      const clamped = Math.max(0.2, Math.min(1.4, Number.isFinite(num) ? num : 1.0));
      this.brightness = clamped;
      const mask = this.shell && this.shell.querySelector(".player-brightness-mask");
      if (mask) {
        mask.style.opacity = clamped < 0.995 ? Math.min(0.82, 1 - clamped).toFixed(3) : "0";
      }
      if (this.video) {
        this.video.style.filter = clamped > 1.005 ? `brightness(${clamped.toFixed(2)})` : "";
      }
    }

    _applyVolume(vol, persist) {
      const num = Number(vol);
      const clamped = Math.max(0, Math.min(1, Number.isFinite(num) ? num : DEFAULT_VOLUME));
      this.volume = clamped;
      this.muted = clamped <= 0.001;
      if (this.video) {
        this.video.muted = this.muted;
        try {
          this.video.volume = clamped;
        } catch (_) {}
      }
      if (this.shell) {
        const volInput = this.shell.querySelector(".p-vol");
        if (volInput) volInput.value = String(Math.round(clamped * 100));
        this._syncVolumeButton();
      }
      if (persist) {
        try {
          localStorage.setItem(VOLUME_STORAGE_KEY, clamped.toFixed(4));
        } catch (_) {}
      }
    }

    _syncVolumeButton() {
      if (!this.shell) return;
      const muteBtn = this.shell.querySelector('[data-act="mute"]');
      if (!muteBtn) return;
      const muted = this.muted || (this.video && this.video.muted) || this.volume <= 0.001;
      muteBtn.textContent = muted ? "🔇" : "🔊";
      muteBtn.title = muted ? "取消静音 (M)" : `静音 (${Math.round(this.volume * 100)}%)`;
    }

    _isShellFullscreen() {
      const el = document.fullscreenElement || document.webkitFullscreenElement;
      return !!(el && this.shell && (el === this.shell || this.shell.contains(el)));
    }

    _isNativeFullscreen() {
      return !!(
        this._isShellFullscreen() ||
        (this.video && this.video.webkitDisplayingFullscreen)
      );
    }

    _isFullscreen() {
      return !!(this._isNativeFullscreen() || this.pseudoFullscreen);
    }

    _isLandscapeVideo() {
      if (this.video && this.video.videoWidth > 0 && this.video.videoHeight > 0) {
        return this.video.videoWidth > this.video.videoHeight * 1.05;
      }
      const ep = this._currentEpisodeObj();
      if (ep && ep.vertical === false) {
        return true;
      }
      return false;
    }

    _lockLandscapeIfSupported() {
      if (screen.orientation && typeof screen.orientation.lock === "function") {
        screen.orientation.lock("landscape").catch(() => {});
      }
    }

    _unlockScreenOrientation() {
      if (screen.orientation && typeof screen.orientation.unlock === "function") {
        try {
          screen.orientation.unlock();
        } catch (_) {}
      }
    }

    _clearFsSyncTimers() {
      for (const id of this.fsSyncTimers) {
        clearTimeout(id);
      }
      this.fsSyncTimers = [];
    }

    _scheduleFullscreenResync() {
      this._clearFsSyncTimers();
      for (const delay of [60, 180, 420]) {
        this.fsSyncTimers.push(
          setTimeout(() => {
            if (this.active) this._syncFullscreenState();
          }, delay)
        );
      }
    }

    async _enterFullscreen() {
      if (!this.shell) return;
      this.wantFullscreen = true;
      const landscape = this._isLandscapeVideo();
      let enteredNative = false;

      if (typeof this.shell.requestFullscreen === "function") {
        try {
          await this.shell.requestFullscreen({ navigationUI: "hide" });
          enteredNative = true;
        } catch (_) {
          try {
            await this.shell.requestFullscreen();
            enteredNative = true;
          } catch (_) {}
        }
      } else if (typeof this.shell.webkitRequestFullscreen === "function") {
        try {
          this.shell.webkitRequestFullscreen();
          enteredNative = true;
        } catch (_) {}
      }

      if (enteredNative) {
        this.pseudoFullscreen = false;
        if (landscape) {
          this._lockLandscapeIfSupported();
        }
      } else if (!landscape && this.video && typeof this.video.webkitEnterFullscreen === "function" && !window.navigator.standalone) {
        try {
          this.video.webkitEnterFullscreen();
          enteredNative = true;
        } catch (_) {}
      }

      if (!enteredNative) {
        this.pseudoFullscreen = true;
        if (landscape) {
          this._lockLandscapeIfSupported();
        }
      }

      this._syncFullscreenState();
      this._scheduleFullscreenResync();
    }

    async _exitFullscreen() {
      this.wantFullscreen = false;
      this.pseudoFullscreen = false;
      this._clearFsSyncTimers();
      this._unlockScreenOrientation();
      if (document.fullscreenElement && typeof document.exitFullscreen === "function") {
        await document.exitFullscreen().catch(() => {});
      } else if (document.webkitFullscreenElement && typeof document.webkitExitFullscreen === "function") {
        try {
          document.webkitExitFullscreen();
        } catch (_) {}
      } else if (this.video && this.video.webkitDisplayingFullscreen && typeof this.video.webkitExitFullscreen === "function") {
        try {
          this.video.webkitExitFullscreen();
        } catch (_) {}
      }
      this._syncFullscreenState();
    }

    _syncFullscreenState() {
      if (!this.shell) return;
      const shellFs = this._isShellFullscreen() || this.pseudoFullscreen;
      const full = this._isFullscreen();
      const vv = window.visualViewport;
      const vw = Math.round((vv && vv.width) || window.innerWidth || document.documentElement.clientWidth || 0);
      const vh = Math.round((vv && vv.height) || window.innerHeight || document.documentElement.clientHeight || 0);
      const shouldRotate = !!(shellFs && this._isLandscapeVideo() && vh > vw && vw > 0);

      this.shell.classList.toggle("is-fullscreen", full);
      this.shell.classList.toggle("pseudo-fullscreen", !!this.pseudoFullscreen);
      this.shell.classList.toggle("fs-rotated-landscape", shouldRotate);
      if (shouldRotate) {
        this.shell.style.setProperty("--fs-rot-w", `${vh}px`);
        this.shell.style.setProperty("--fs-rot-h", `${vw}px`);
      } else {
        this.shell.style.removeProperty("--fs-rot-w");
        this.shell.style.removeProperty("--fs-rot-h");
      }

      const fsBtn = this.shell.querySelector('[data-act="fullscreen"]');
      if (fsBtn) {
        fsBtn.textContent = full ? "退出全屏" : "⛶ 全屏";
        fsBtn.classList.toggle("active", full);
      }
    }

    prefs() {
      const appState = window.ZGJApp ? window.ZGJApp.state : null;
      const pb = (appState && appState.playback) || {};
      return {
        speed: SPEEDS.includes(Number(pb.speed)) ? Number(pb.speed) : 1.0,
        quality: Number(pb.quality) >= 0 ? Number(pb.quality) : 0,
        autoAdvance: pb.autoAdvance !== false,
        danmaku: pb.danmaku !== false,
        preload: pb.preload !== false,
        doubleTapSeek: pb.doubleTapSeek !== false,
        swipeLevels: pb.swipeLevels !== false,
        autoPictureInPicture: pb.autoPictureInPicture === true
      };
    }

    async open({ drama, episodes, episodeNumber, startPosition, forceLocal, initialPlan, onStateUpdate, onEpisodeChange }) {
      const p = this.prefs();
      this._clearPreloadedState(true);
      this.active = true;
      this.drama = normalizeDrama(drama);
      this.episodes = Array.isArray(episodes) ? episodes : [];
      this.currentIndex = Number(episodeNumber) || 1;
      this.resumePosition = Number(startPosition) > 0 ? Number(startPosition) : 0;
      this.preferredQuality = p.quality;
      this.speed = p.speed;
      this.danmakuEnabled = p.danmaku;
      this.onStateUpdate = onStateUpdate || null;
      this.onEpisodeChange = onEpisodeChange || null;
      this.locked = false;
      this.mobileEpsOpen = false;
      this.pseudoFullscreen = false;
      this.wantFullscreen = false;
      this.controlsVisible = true;
      this.muted = false;
      this.brightness = 1.0;

      this.volume = DEFAULT_VOLUME;
      try {
        const rawVol = localStorage.getItem(VOLUME_STORAGE_KEY);
        if (rawVol !== null && rawVol.trim() !== "") {
          const savedVol = Number(rawVol);
          if (Number.isFinite(savedVol) && savedVol >= 0 && savedVol <= 1) {
            this.volume = savedVol;
          }
        }
      } catch (_) {}

      const root = document.getElementById("player-root");
      root.classList.remove("hidden");
      this._renderShell();
      await this._loadEpisode(this.currentIndex, this.resumePosition, !!forceLocal, initialPlan || null);
    }

    async close() {
      if (!this.active) return;
      this.resolveEpoch++;
      this.active = false;
      clearTimeout(this.loadingDelayTimer);
      clearTimeout(this.preloadDelayTimer);
      clearTimeout(this.hideControlsTimer);
      clearTimeout(this._hudTimer);
      clearInterval(this.progressSaveTimer);
      await this._flushWatchProgress();
      this._clearPreloadedState(true);
      this._destroyMedia(true);
      await this._releaseCurrentPlan();
      await this._exitFullscreen();
      const root = document.getElementById("player-root");
      root.classList.add("hidden");
      root.innerHTML = "";
      this.video = null;
      this.standbyVideo = null;
      this.shell = null;
    }

    _currentEpisodeObj() {
      return (
        this.episodes.find((ep) => ep.number === this.currentIndex) || {
          number: this.currentIndex,
          title: `第${this.currentIndex}集`,
          vip: false,
          raw: { title: `第${this.currentIndex}集`, currentEpisode: this.currentIndex }
        }
      );
    }

    _releasePlanSession(planObj) {
      if (planObj && planObj.session) {
        return API.rpc("release", { session: planObj.session }).catch(() => {});
      }
      return Promise.resolve();
    }

    async _releaseCurrentPlan() {
      const prev = this.plan;
      this.plan = null;
      await this._releasePlanSession(prev);
    }

    _resetStandbyMedia(fullReset) {
      this.standbyEpoch++;
      this.ignoringStandbyError = true;
      this.standbyKey = "";
      this.standbyPlan = null;
      this.standbyReady = false;
      this.standbyFailed = false;
      if (this.standbyHls) {
        try {
          this.standbyHls.destroy();
        } catch (_) {}
        this.standbyHls = null;
      }
      if (this.standbyVideo) {
        try {
          this.standbyVideo.pause();
          this.standbyVideo.muted = true;
          if (fullReset) {
            this.standbyVideo.removeAttribute("src");
            this.standbyVideo.load();
          } else if (this.standbyVideo.hasAttribute("src")) {
            this.standbyVideo.removeAttribute("src");
          }
        } catch (_) {}
      }
    }

    _clearPreloadedState(fullReset) {
      clearTimeout(this.preloadDelayTimer);
      const stalePlan = this.preloadedPlan;
      this.preloadedPlan = null;
      this.preloadedKey = "";
      this._resetStandbyMedia(!!fullReset);
      if (stalePlan) {
        this._releasePlanSession(stalePlan);
      }
    }

    _destroyMedia(fullReset) {
      this.attachEpoch++;
      this.ignoringMediaError = true;
      if (this.hls) {
        try {
          this.hls.destroy();
        } catch (_) {}
        this.hls = null;
      }
      if (this.video) {
        try {
          this.video.pause();
          if (fullReset) {
            this.video.removeAttribute("src");
            this.video.load();
          } else if (this.video.hasAttribute("src")) {
            this.video.removeAttribute("src");
          }
        } catch (_) {}
      }
    }

    _resetProgressBarUI() {
      if (!this.shell) return;
      const seekInput = this.shell.querySelector(".p-seek");
      const curEl = this.shell.querySelector(".p-time-cur");
      const durEl = this.shell.querySelector(".p-time-dur");
      if (seekInput) seekInput.value = "0";
      if (curEl) curEl.textContent = "00:00";
      if (durEl) durEl.textContent = "00:00";
    }

    _armLoadingSpinner(epoch, delayMs) {
      clearTimeout(this.loadingDelayTimer);
      this.loadingDelayTimer = setTimeout(() => {
        if (!this.active || epoch !== this.resolveEpoch || this.errorText) return;
        if (!this.video || this.video.readyState < 2) {
          this.loading = true;
          this._syncUI();
        }
      }, delayMs || 220);
    }

    _createManagedHls(HlsLib, url, initialStandby) {
      const hls = new HlsLib({
        enableWorker: true,
        maxBufferLength: initialStandby ? 10 : 30,
        maxMaxBufferLength: initialStandby ? 20 : 60
      });
      let mediaErrorRecoveries = 0;
      hls.on(HlsLib.Events.MANIFEST_PARSED, () => {
        if (hls === this.standbyHls) {
          this.standbyReady = true;
          return;
        }
        if (hls === this.hls && this.active) {
          this._onMediaReady();
        }
      });
      hls.on(HlsLib.Events.ERROR, (_, data) => {
        if (!data || !data.fatal) return;
        if (hls === this.standbyHls) {
          this.standbyFailed = true;
          return;
        }
        if (hls === this.hls && this.active) {
          if (data.type === HlsLib.ErrorTypes.MEDIA_ERROR && mediaErrorRecoveries < 2) {
            mediaErrorRecoveries++;
            hls.recoverMediaError();
          } else {
            API.addLog(`[播放器] HLS 致命错误 (${data.type}/${data.details || "unknown"}) url=${url}`);
            this._handleMediaFailure("流媒体分片加载失败", this.attachEpoch);
          }
        }
      });
      return hls;
    }

    _promoteStandbyToActive(nextPlan, epoch) {
      const oldVideo = this.video;
      const nextVideo = this.standbyVideo;
      const nextHls = this.standbyHls;

      this.standbyEpoch++;
      this.standbyKey = "";
      this.standbyPlan = null;
      this.standbyReady = false;
      this.standbyFailed = false;
      this.standbyHls = null;

      if (this.hls) {
        try {
          this.hls.destroy();
        } catch (_) {}
        this.hls = null;
      }

      this.ignoringStandbyError = true;
      if (oldVideo) {
        try {
          oldVideo.pause();
          oldVideo.muted = true;
          oldVideo.style.filter = "";
          oldVideo.removeAttribute("src");
          oldVideo.load();
        } catch (_) {}
        oldVideo.classList.add("standby");
      }

      nextVideo.classList.remove("standby");
      this.video = nextVideo;
      this.standbyVideo = oldVideo;
      this.attachEpoch++;
      this.ignoringMediaError = false;
      this.plan = nextPlan;

      if (nextHls) {
        nextHls.config.maxBufferLength = 30;
        nextHls.config.maxMaxBufferLength = 60;
        this.hls = nextHls;
      }

      nextVideo.playbackRate = this.speed;
      nextVideo.muted = this.muted;
      try {
        nextVideo.volume = this.volume;
      } catch (_) {}
      this._applyBrightness(this.brightness);

      this.loading = false;
      if (nextVideo.readyState < 2) {
        this._armLoadingSpinner(epoch, 240);
      }

      this._syncUI();
      if (Number.isFinite(nextVideo.duration) && nextVideo.duration > 0 && this.shell) {
        const durEl = this.shell.querySelector(".p-time-dur");
        if (durEl) durEl.textContent = formatTime(nextVideo.duration);
      }

      const playResult = nextVideo.play();
      if (playResult && typeof playResult.catch === "function") {
        playResult.catch((err) => {
          if (!this.active || epoch !== this.resolveEpoch) return;
          if (err && err.name === "NotAllowedError" && this.standbyVideo) {
            const restoreVideo = this.standbyVideo;
            const demoteVideo = this.video;
            if (this.hls) {
              try {
                this.hls.destroy();
              } catch (_) {}
              this.hls = null;
            }
            try {
              demoteVideo.pause();
              demoteVideo.muted = true;
              demoteVideo.removeAttribute("src");
            } catch (_) {}
            demoteVideo.classList.add("standby");
            restoreVideo.classList.remove("standby");
            this.video = restoreVideo;
            this.standbyVideo = demoteVideo;
            this._armLoadingSpinner(epoch, 200);
            this._attachStream(nextPlan, epoch).catch(() => {});
          }
        });
      }

      if (this.wantFullscreen && this._isShellFullscreen() && this._isLandscapeVideo()) {
        this._lockLandscapeIfSupported();
      }
      this._ensureDanmakuWindow();
      this._queueNextPreload(450);
    }

    async _loadEpisode(epNum, seekTo, preferLocalOnly, providedPlan) {
      const epoch = ++this.resolveEpoch;
      clearTimeout(this.loadingDelayTimer);
      clearTimeout(this.preloadDelayTimer);

      const prevWatch = this._captureWatchSnapshot();
      if (prevWatch) {
        this._sendWatchSnapshot(prevWatch);
      }
      const prevPlan = this.plan;
      this.plan = null;
      if (prevPlan) {
        this._releasePlanSession(prevPlan);
      }

      this.currentIndex = epNum;
      this.resumePosition = Number(seekTo) > 0 ? Number(seekTo) : 0;
      this.errorText = "";
      this.fallbackCount = 0;
      this.triedTranscode = false;
      this.probedHlsForUrl = "";
      this.danmakuBuckets.clear();
      this.danmakuFired.clear();
      this.danmakuLoadingKey = "";
      this.lastSavedPos = -1;
      this._clearDanmakuDOM();
      this._resetProgressBarUI();

      if (this.onEpisodeChange) {
        this.onEpisodeChange(this.currentIndex);
      }

      const ep = this._currentEpisodeObj();
      const preloadKey = `${this.drama.id}:${ep.number}:${this.preferredQuality}`;
      const pinnedSingleVideo = !!(
        this.video &&
        (this.video.webkitDisplayingFullscreen ||
          document.pictureInPictureElement === this.video ||
          this.video.webkitPresentationMode === "picture-in-picture")
      );

      if (
        !providedPlan &&
        !preferLocalOnly &&
        this.resumePosition <= 0 &&
        !pinnedSingleVideo &&
        this.preloadedKey === preloadKey &&
        this.preloadedPlan &&
        this.standbyKey === preloadKey &&
        this.standbyPlan &&
        !this.standbyFailed &&
        this.standbyVideo &&
        (this.standbyReady || this.standbyVideo.readyState >= 1)
      ) {
        const nextPlan = this.preloadedPlan;
        this.preloadedPlan = null;
        this.preloadedKey = "";
        this._promoteStandbyToActive(nextPlan, epoch);
        return;
      }

      this._destroyMedia(false);

      const hasWarmPlan = !!(
        (providedPlan && providedPlan.url && !preferLocalOnly) ||
        (this.preloadedKey === preloadKey && this.preloadedPlan && !preferLocalOnly)
      );

      if (hasWarmPlan) {
        this.loading = false;
        this._syncUI();
        this._armLoadingSpinner(epoch, 220);
      } else {
        this.loading = true;
        this._syncUI();
      }

      try {
        let plan = null;
        if (providedPlan && providedPlan.url && !preferLocalOnly) {
          plan = providedPlan;
        } else if (this.preloadedKey === preloadKey && this.preloadedPlan && !preferLocalOnly) {
          plan = this.preloadedPlan;
          this.preloadedPlan = null;
          this.preloadedKey = "";
          this._resetStandbyMedia(false);
        } else {
          if (preferLocalOnly) {
            plan = await API.rpc("localPlayback", {
              drama: this.drama,
              index: ep.number
            });
          } else {
            plan = await API.rpc("resolve", {
              drama: this.drama,
              chapter: ep.raw,
              index: ep.number,
              quality: this.preferredQuality
            });
          }
        }

        if (epoch !== this.resolveEpoch || !this.active) {
          this._releasePlanSession(plan);
          return;
        }

        this.plan = plan;
        await this._attachStream(plan, epoch);
      } catch (err) {
        if (epoch !== this.resolveEpoch || !this.active) return;
        clearTimeout(this.loadingDelayTimer);
        this.loading = false;
        this.errorText = err.message || "视频地址解析失败，请重试";
        this._syncUI();
      }
    }

    async _attachStream(plan, epoch) {
      if (!this.video || !plan || !plan.url) {
        throw new Error("未获取到有效播放地址");
      }
      const video = this.video;
      const url = plan.url;
      const isHLS = plan.isHls === true || url.toLowerCase().includes(".m3u8");

      this._destroyMedia(false);
      const curAttach = this.attachEpoch;
      video.muted = this.muted;
      try {
        video.volume = this.volume;
      } catch (_) {}
      this._applyBrightness(this.brightness);

      const canNativeHls = !!video.canPlayType("application/vnd.apple.mpegurl");
      const canMse = !!(window.MediaSource || window.ManagedMediaSource);
      if (isHLS && (canMse || !canNativeHls)) {
        const HlsLib = await loadHlsLibrary();
        if (epoch !== this.resolveEpoch || curAttach !== this.attachEpoch || !this.active) return;
        if (HlsLib && HlsLib.isSupported()) {
          const hls = this._createManagedHls(HlsLib, url, false);
          this.hls = hls;
          this.ignoringMediaError = false;
          hls.loadSource(url);
          hls.attachMedia(video);
          return;
        }
        if (!canNativeHls) {
          throw new Error("当前浏览器环境无法加载 HLS 播放组件，请检查网络或更换浏览器");
        }
      }

      this.ignoringMediaError = false;
      video.src = url;
      video.load();
    }

    _onMediaReady() {
      if (!this.video) return;
      clearTimeout(this.loadingDelayTimer);
      this.loading = false;
      this.errorText = "";
      this.video.playbackRate = this.speed;
      this.video.muted = this.muted;
      try {
        this.video.volume = this.volume;
      } catch (_) {}
      this._applyBrightness(this.brightness);
      if (this.resumePosition > 1 && (!Number.isFinite(this.video.duration) || this.resumePosition < this.video.duration - 2)) {
        try {
          this.video.currentTime = this.resumePosition;
        } catch (_) {}
        this.resumePosition = 0;
      }
      this.video.play().catch(() => {});
      if (this.wantFullscreen && this._isShellFullscreen() && this._isLandscapeVideo()) {
        this._lockLandscapeIfSupported();
      }
      this._syncUI();
      this._ensureDanmakuWindow();
      this._queueNextPreload(450);
    }

    async _onNativeVideoError(failedUrl, errCode) {
      if (!this.active || !this.plan) return;
      const curAttach = this.attachEpoch;
      if (this.handlingFailureEpoch === curAttach) return;
      this.handlingFailureEpoch = curAttach;
      const epoch = this.resolveEpoch;

      let hevcUnsupported = false;
      if (!this.plan.isHls && !this.plan.local && !failedUrl.includes("transcode=h264") && this.probedHlsForUrl !== failedUrl) {
        this.probedHlsForUrl = failedUrl;
        try {
          const resp = await fetch(failedUrl, {
            method: "GET",
            headers: { Range: "bytes=0-4095" },
            credentials: "same-origin"
          });
          if (epoch !== this.resolveEpoch || !this.active) return;
          const ct = (resp.headers.get("Content-Type") || "").toLowerCase();
          const buf = new Uint8Array(await resp.arrayBuffer());
          if (epoch !== this.resolveEpoch || !this.active) return;
          const headLatin1 = Array.from(buf.slice(0, 512), (b) => String.fromCharCode(b)).join("");
          if (ct.includes("mpegurl") || headLatin1.trimStart().startsWith("#EXTM3U")) {
            this.plan.isHls = true;
            await this._attachStream(this.plan, epoch);
            return;
          }
          const fullLatin1 = Array.from(buf, (b) => String.fromCharCode(b)).join("");
          if (
            (errCode === 3 || errCode === 4) &&
            (fullLatin1.includes("hvc1") || fullLatin1.includes("hev1")) &&
            !this.video.canPlayType('video/mp4; codecs="hvc1.1.6.L93.B0"')
          ) {
            hevcUnsupported = true;
            if (window.ZGJApp && window.ZGJApp.ffmpegReady && !this.triedTranscode) {
              this.triedTranscode = true;
              const curPos = this.resumePosition > 1 ? this.resumePosition : (this.video ? this.video.currentTime : 0);
              const sep = this.plan.url.includes("?") ? "&" : "?";
              const transPlan = {
                ...this.plan,
                url: `${this.plan.url}${sep}transcode=h264${curPos > 1 ? `&start=${curPos.toFixed(1)}` : ""}`
              };
              this.resumePosition = 0;
              this.plan = transPlan;
              toast("当前浏览器不支持 HEVC 直解，已自动启用 FFmpeg 兼容转码");
              await this._attachStream(transPlan, epoch);
              return;
            }
          }
        } catch (_) {}
      }

      API.addLog(`[播放器] 原生解码或网络错误 (code=${errCode}) url=${failedUrl}`);
      const msg = hevcUnsupported
        ? "当前浏览器不支持 HEVC (H.265) 视频硬件解码，请使用最新版 Edge / Chrome / Safari，或在服务端安装 FFmpeg 开启兼容转码"
        : "媒体解码或网络读取失败";
      await this._handleMediaFailure(msg, curAttach, hevcUnsupported);
    }

    async _handleMediaFailure(reason, fromAttach, skipSameCodecRoutes) {
      if (!this.active) return;
      clearTimeout(this.loadingDelayTimer);
      const epoch = this.resolveEpoch;
      const maxFallbacks = Math.min(Math.max(0, (this.plan && this.plan.routeCount ? this.plan.routeCount : 1) - 1), 8);
      if (
        !skipSameCodecRoutes &&
        this.plan &&
        this.plan.session &&
        (this.plan.routeIndex || 0) + 1 < (this.plan.routeCount || 1) &&
        this.fallbackCount < maxFallbacks
      ) {
        this.fallbackCount++;
        this.loading = true;
        toast(`当前线路加载失败，正在切换备用线路 (${(this.plan.routeIndex || 0) + 2}/${this.plan.routeCount})...`);
        this._syncUI();
        try {
          const curPos = this.video ? this.video.currentTime : 0;
          if (curPos > 1) this.resumePosition = curPos;
          const nextPlan = await API.rpc("fallback", { session: this.plan.session });
          if (epoch !== this.resolveEpoch || !this.active) {
            this._releasePlanSession(nextPlan);
            return;
          }
          this.triedTranscode = false;
          this.probedHlsForUrl = "";
          this.plan = nextPlan;
          await this._attachStream(nextPlan, epoch);
          return;
        } catch (err) {
          if (epoch !== this.resolveEpoch) return;
          reason = err.message || reason;
        }
      }
      this.loading = false;
      this.errorText = reason || "播放出错，请尝试切换线路或重试";
      this._syncUI();
    }

    async switchRouteManually() {
      if (!this.plan || !this.plan.session) return;
      if ((this.plan.routeIndex || 0) + 1 >= (this.plan.routeCount || 1)) {
        await this._loadEpisode(this.currentIndex, this.video ? this.video.currentTime : 0, false);
        return;
      }
      const epoch = ++this.resolveEpoch;
      clearTimeout(this.loadingDelayTimer);
      const curPos = this.video ? this.video.currentTime : 0;
      if (curPos > 1) this.resumePosition = curPos;
      this.loading = true;
      this.errorText = "";
      this._syncUI();
      try {
        const nextPlan = await API.rpc("fallback", { session: this.plan.session });
        if (epoch !== this.resolveEpoch || !this.active) {
          this._releasePlanSession(nextPlan);
          return;
        }
        this.triedTranscode = false;
        this.probedHlsForUrl = "";
        this.plan = nextPlan;
        await this._attachStream(nextPlan, epoch);
        const msg = `已切换至线路 ${(nextPlan.routeIndex || 0) + 1}/${nextPlan.routeCount || 1}`;
        this._flashHud(msg, 1200);
        toast(msg);
      } catch (err) {
        if (epoch !== this.resolveEpoch) return;
        this.loading = false;
        this.errorText = err.message || "切换线路失败";
        this._syncUI();
      }
    }

    async changeQuality(q) {
      const nextQ = Number(q) || 0;
      if (nextQ !== this.preferredQuality) {
        this.preferredQuality = nextQ;
        this._clearPreloadedState(false);
      }
      API.updateUserSettings({ playback: { quality: this.preferredQuality } })
        .then((res) => {
          if (res.state && this.onStateUpdate) this.onStateUpdate(res.state);
        })
        .catch(() => {});
      const curPos = this.video ? this.video.currentTime : 0;
      await this._loadEpisode(this.currentIndex, curPos, false);
    }

    changeSpeed(spd) {
      if (!SPEEDS.includes(spd)) return;
      this.speed = spd;
      if (this.video) this.video.playbackRate = spd;
      API.updateUserSettings({ playback: { speed: spd } })
        .then((res) => {
          if (res.state && this.onStateUpdate) this.onStateUpdate(res.state);
        })
        .catch(() => {});
      this._syncUI();
    }

    toggleDanmaku() {
      this.danmakuEnabled = !this.danmakuEnabled;
      if (!this.danmakuEnabled) {
        this._clearDanmakuDOM();
      } else {
        this._ensureDanmakuWindow();
      }
      API.updateUserSettings({ playback: { danmaku: this.danmakuEnabled } })
        .then((res) => {
          if (res.state && this.onStateUpdate) this.onStateUpdate(res.state);
        })
        .catch(() => {});
      this._syncUI();
    }

    _queueNextPreload(delayMs) {
      clearTimeout(this.preloadDelayTimer);
      if (!this.active || !this.prefs().preload) return;
      this.preloadDelayTimer = setTimeout(() => {
        if (this.active) {
          this._scheduleNextPreload();
        }
      }, typeof delayMs === "number" ? delayMs : 450);
    }

    async _scheduleNextPreload() {
      if (!this.active || !this.prefs().preload || !this.drama) return;
      const nextEp = this.episodes.find((e) => e.number === this.currentIndex + 1);
      if (!nextEp) return;
      const key = `${this.drama.id}:${nextEp.number}:${this.preferredQuality}`;
      if (this.preloadedKey === key) {
        if (this.preloadedPlan && this.standbyKey !== key) {
          this._warmStandbyPlan(key, this.preloadedPlan);
        }
        return;
      }
      this._clearPreloadedState(false);
      this.preloadedKey = key;
      try {
        const p = await API.rpc("preload", {
          drama: this.drama,
          chapter: nextEp.raw,
          index: nextEp.number,
          quality: this.preferredQuality
        });
        if (this.active && this.preloadedKey === key && this.currentIndex + 1 === nextEp.number && p && p.url) {
          this.preloadedPlan = p;
          this._warmStandbyPlan(key, p);
        } else {
          if (this.preloadedKey === key) {
            this.preloadedKey = "";
          }
          this._releasePlanSession(p);
        }
      } catch (_) {
        if (this.preloadedKey === key) {
          this.preloadedKey = "";
        }
      }
    }

    async _warmStandbyPlan(key, plan) {
      if (!this.active || !this.standbyVideo || !plan || !plan.url) return;
      this._resetStandbyMedia(false);
      const sbEpoch = ++this.standbyEpoch;
      this.standbyKey = key;
      this.standbyPlan = plan;
      this.standbyReady = false;
      this.standbyFailed = false;

      const sv = this.standbyVideo;
      const url = plan.url;
      const isHLS = plan.isHls === true || url.toLowerCase().includes(".m3u8");
      sv.muted = true;
      sv.preload = "auto";

      const canNativeHls = !!sv.canPlayType("application/vnd.apple.mpegurl");
      const canMse = !!(window.MediaSource || window.ManagedMediaSource);
      if (isHLS && (canMse || !canNativeHls)) {
        const HlsLib = await loadHlsLibrary();
        if (!this.active || sbEpoch !== this.standbyEpoch || this.preloadedKey !== key) return;
        if (HlsLib && HlsLib.isSupported()) {
          const hls = this._createManagedHls(HlsLib, url, true);
          this.standbyHls = hls;
          this.ignoringStandbyError = false;
          hls.loadSource(url);
          hls.attachMedia(sv);
          return;
        }
        if (!canNativeHls) return;
      }

      this.ignoringStandbyError = false;
      sv.src = url;
      try {
        sv.load();
      } catch (_) {}
    }

    async _ensureDanmakuWindow() {
      if (!this.danmakuEnabled || !this.plan || this.plan.local || !this.plan.session || !this.plan.danmakuId || !this.video) {
        return;
      }
      const durSec = Number(this.video.duration);
      const durMs = Number.isFinite(durSec) && durSec > 1 ? Math.floor(durSec * 1000) : 600000;
      const curMs = Math.max(0, Math.floor((this.video.currentTime || 0) * 1000));
      const winStart = Math.floor(curMs / DANMAKU_WINDOW_MS) * DANMAKU_WINDOW_MS;
      if (winStart >= durMs || this.danmakuBuckets.has(winStart)) return;
      const reqKey = `${this.plan.session}:${winStart}`;
      if (this.danmakuLoadingKey === reqKey) return;
      this.danmakuLoadingKey = reqKey;
      try {
        const page = await API.rpc("danmaku", {
          playbackSession: this.plan.session,
          startMs: winStart,
          durationMs: durMs
        });
        if (this.plan && `${this.plan.session}:${winStart}` === reqKey && page && Array.isArray(page.items)) {
          this.danmakuBuckets.set(winStart, page.items);
        }
      } catch (_) {
        this.danmakuBuckets.set(winStart, []);
      } finally {
        if (this.danmakuLoadingKey === reqKey) {
          this.danmakuLoadingKey = "";
        }
      }
    }

    _tickDanmaku() {
      if (!this.danmakuEnabled || !this.video || this.video.paused) return;
      this._ensureDanmakuWindow();
      const curMs = Math.floor(this.video.currentTime * 1000);
      const winStart = Math.floor(curMs / DANMAKU_WINDOW_MS) * DANMAKU_WINDOW_MS;
      const list = this.danmakuBuckets.get(winStart);
      if (!list || list.length === 0) return;
      for (const item of list) {
        if (!item || !item.id || this.danmakuFired.has(item.id)) continue;
        if (item.timeMs >= curMs - 600 && item.timeMs <= curMs + 350) {
          this.danmakuFired.add(item.id);
          this._spawnDanmakuBullet(item.text);
        }
      }
    }

    _spawnDanmakuBullet(text) {
      const layer = this.shell && this.shell.querySelector(".danmaku-layer");
      if (!layer || !text) return;
      const now = performance.now();
      let chosenTrack = -1;
      for (let i = 0; i < this.danmakuTracks.length; i++) {
        if (now >= this.danmakuTracks[i]) {
          chosenTrack = i;
          break;
        }
      }
      if (chosenTrack < 0) return;
      const durationSec = Math.max(4.5, 8.0 / Math.max(0.75, this.speed));
      this.danmakuTracks[chosenTrack] = now + durationSec * 240;

      const el = document.createElement("div");
      el.className = "danmaku-bullet";
      el.textContent = String(text).slice(0, 120);
      el.style.top = `${chosenTrack * 28 + 4}px`;
      const stageWidth = layer.offsetWidth || layer.clientWidth || 960;
      el.style.setProperty("--dm-dist", `-${stageWidth + 640}px`);
      el.style.animationDuration = `${durationSec.toFixed(2)}s`;
      layer.appendChild(el);
      el.addEventListener("animationend", () => el.remove(), { once: true });
    }

    _clearDanmakuDOM() {
      const layer = this.shell && this.shell.querySelector(".danmaku-layer");
      if (layer) layer.innerHTML = "";
      this.danmakuTracks.fill(0);
    }

    _captureWatchSnapshot() {
      if (!this.drama || !this.drama.id || !this.video) return null;
      const pos = Number(this.video.currentTime) || 0;
      const dur = Number.isFinite(this.video.duration) && this.video.duration > 0 ? Number(this.video.duration) : 0;
      if (pos < 0.5 && dur <= 0) return null;
      if (Math.abs(pos - this.lastSavedPos) < 1.5) return null;
      this.lastSavedPos = pos;
      return {
        drama: this.drama,
        episode: this.currentIndex,
        position: pos,
        duration: dur
      };
    }

    async _sendWatchSnapshot(snap) {
      if (!snap) return;
      try {
        const res = await API.saveWatch(snap);
        if (res && res.state && this.onStateUpdate) {
          this.onStateUpdate(res.state);
        }
      } catch (_) {}
    }

    async _flushWatchProgress() {
      const snap = this._captureWatchSnapshot();
      if (!snap) return;
      await this._sendWatchSnapshot(snap);
    }

    _toggleControlsVisibility() {
      if (!this.shell) return;
      if (this.controlsVisible) {
        this.controlsVisible = false;
        clearTimeout(this.hideControlsTimer);
        this.shell.classList.add("controls-hidden");
      } else {
        this._showControlsTemporarily();
      }
    }

    _showControlsTemporarily() {
      this.controlsVisible = true;
      if (this.shell) this.shell.classList.remove("controls-hidden");
      clearTimeout(this.hideControlsTimer);
      if (this.video && !this.video.paused) {
        this.hideControlsTimer = setTimeout(() => {
          if (this.video && !this.video.paused && !this.mobileEpsOpen) {
            this.controlsVisible = false;
            if (this.shell) this.shell.classList.add("controls-hidden");
          }
        }, 4500);
      }
    }

    _showHud(text) {
      const hud = this.shell && this.shell.querySelector(".player-hud-pill");
      if (!hud) return;
      if (!text) {
        this._hudFlashUntil = 0;
        hud.classList.add("hidden");
        return;
      }
      hud.textContent = text;
      hud.classList.remove("hidden");
    }

    _flashHud(text, ms) {
      const dur = ms || 950;
      this._showHud(text);
      this._hudFlashUntil = Date.now() + dur;
      clearTimeout(this._hudTimer);
      this._hudTimer = setTimeout(() => {
        this._hudFlashUntil = 0;
        this._showHud("");
      }, dur);
    }

    _bindRangeTouchRotation(input, onInputValue, onCommitValue) {
      let activeId = null;
      const updateFromEvent = (ev) => {
        const rect = input.getBoundingClientRect();
        const ratio = Math.max(0, Math.min(1, (ev.clientY - rect.top) / Math.max(1, rect.height)));
        const min = Number(input.min) || 0;
        const max = Number(input.max) || 100;
        const val = Math.round(min + ratio * (max - min));
        input.value = String(val);
        onInputValue(val);
      };
      input.addEventListener("pointerdown", (ev) => {
        if (!this.shell || !this.shell.classList.contains("fs-rotated-landscape")) return;
        ev.preventDefault();
        ev.stopPropagation();
        activeId = ev.pointerId;
        try {
          input.setPointerCapture(ev.pointerId);
        } catch (_) {}
        updateFromEvent(ev);
      });
      input.addEventListener("pointermove", (ev) => {
        if (activeId === null || ev.pointerId !== activeId) return;
        ev.preventDefault();
        ev.stopPropagation();
        updateFromEvent(ev);
      });
      const finish = (ev) => {
        if (activeId === null || ev.pointerId !== activeId) return;
        activeId = null;
        if (onCommitValue) onCommitValue(Number(input.value));
      };
      input.addEventListener("pointerup", finish);
      input.addEventListener("pointercancel", finish);
    }

    _renderShell() {
      const root = document.getElementById("player-root");
      const pipSupported = !!(
        document.pictureInPictureEnabled ||
        (HTMLVideoElement.prototype && typeof HTMLVideoElement.prototype.webkitSetPresentationMode === "function")
      );
      root.innerHTML = `
        <div class="player-shell ${this.wideMode ? "wide-mode" : ""} ${this.mobileEpsOpen ? "mobile-eps-open" : ""}">
          <div class="player-viewport">
            <div class="player-stage">
              <video class="player-video" playsinline webkit-playsinline preload="auto"></video>
              <video class="player-video standby" playsinline webkit-playsinline preload="metadata" muted></video>
              <div class="player-brightness-mask"></div>
              <div class="danmaku-layer"></div>
              <div class="player-hud-pill hidden"></div>
              <div class="player-center-state hidden"></div>
              <button class="player-lock-btn" title="锁定屏幕控制" data-act="lock">🔓</button>
              <div class="player-top-bar">
                <button class="p-btn" data-act="close">← 返回</button>
                <div style="min-width:0;flex:1;">
                  <div class="p-title" style="font-weight:700;font-size:15px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;"></div>
                  <div class="p-subtitle" style="font-size:12px;color:rgba(255,255,255,0.68);"></div>
                </div>
                <button class="p-btn" data-act="route"></button>
              </div>
              <div class="player-bottom-bar">
                <div class="player-progress-row">
                  <span class="p-time-cur" style="font-size:12px;font-variant-numeric:tabular-nums;">00:00</span>
                  <input type="range" class="player-range p-seek" min="0" max="1000" value="0" />
                  <span class="p-time-dur" style="font-size:12px;font-variant-numeric:tabular-nums;">00:00</span>
                </div>
                <div class="player-controls-row">
                  <div class="player-ctrl-nav">
                    <button class="p-btn" data-act="prev">⏮ 上集</button>
                    <button class="p-btn p-play-btn" data-act="play">▶ 播放</button>
                    <button class="p-btn" data-act="next">下集 ⏭</button>
                  </div>
                  <div class="player-ctrl-extra">
                    <button class="p-btn" data-act="danmaku">弹幕</button>
                    <select class="p-btn p-quality-sel" style="border:none;outline:none;"></select>
                    <select class="p-btn p-speed-sel" style="border:none;outline:none;">
                      ${SPEEDS.map((s) => `<option value="${s}" style="color:#000;">${s}x</option>`).join("")}
                    </select>
                    <button class="p-btn p-mute-btn" data-act="mute" title="静音 (M)">🔊</button>
                    <input type="range" class="player-range p-vol" min="0" max="100" value="${Math.round(this.volume * 100)}" title="音量" />
                    <button class="p-btn p-pip-btn ${pipSupported ? "" : "hidden"}" data-act="pip" title="画中画">画中画</button>
                    <button class="p-btn p-wide-btn" data-act="wide" title="宽屏模式">宽屏</button>
                  </div>
                  <div class="player-ctrl-right">
                    <button class="p-btn p-eps-btn" data-act="episodes">选集</button>
                    <button class="p-btn p-fs-btn" data-act="fullscreen" title="全屏 (F)">⛶ 全屏</button>
                  </div>
                </div>
              </div>
            </div>
            <aside class="player-side-panel">
              <div class="player-side-header">
                <span>选集列表 (${this.episodes.length})</span>
                <div style="display:flex;align-items:center;gap:8px;">
                  <span style="font-size:12px;color:var(--text-muted);">${esc(this.drama.source)}</span>
                  <button class="p-btn p-side-close" data-act="close-episodes" title="关闭选集">✕</button>
                </div>
              </div>
              <div class="player-side-eps"></div>
            </aside>
          </div>
        </div>
      `;

      this.shell = root.querySelector(".player-shell");
      const videoEls = root.querySelectorAll(".player-video");
      this.video = videoEls[0];
      this.standbyVideo = videoEls[1] || null;
      this.video.muted = this.muted;
      if (this.standbyVideo) {
        this.standbyVideo.muted = true;
      }
      try {
        this.video.volume = this.volume;
        this.video.load();
        if (this.standbyVideo) {
          this.standbyVideo.volume = this.volume;
          this.standbyVideo.load();
        }
      } catch (_) {}
      this._syncVolumeButton();
      this._syncFullscreenState();

      const sideEps = root.querySelector(".player-side-eps");
      if (sideEps) {
        sideEps.innerHTML = this.episodes
          .map(
            (e) => `
          <button class="ep-btn ${e.number === this.currentIndex ? "current" : ""}" data-side-ep="${e.number}" style="background:#1c2230;color:${e.number === this.currentIndex ? "var(--accent)" : "#e2e8f0"};">
            <span>第${e.number}集</span>
            ${e.vip ? `<span class="ep-corner vip">VIP</span>` : ""}
          </button>
        `
          )
          .join("");
        sideEps.addEventListener("click", (ev) => {
          const btn = ev.target.closest("[data-side-ep]");
          if (!btn) return;
          this.mobileEpsOpen = false;
          this._loadEpisode(Number(btn.getAttribute("data-side-ep")), 0, false);
        });
      }

      const stage = root.querySelector(".player-stage");
      const seekInput = root.querySelector(".p-seek");
      const volInput = root.querySelector(".p-vol");
      const timeCurEl = root.querySelector(".p-time-cur");
      const timeDurEl = root.querySelector(".p-time-dur");
      let draggingSeek = false;

      const bindVideoEvents = (v) => {
        if (!v) return;
        v.addEventListener("webkitbeginfullscreen", () => {
          if (v !== this.video) return;
          this._syncFullscreenState();
        });
        v.addEventListener("webkitendfullscreen", () => {
          if (v !== this.video) return;
          this.wantFullscreen = false;
          this._syncFullscreenState();
        });
        v.addEventListener("loadedmetadata", () => {
          if (v === this.standbyVideo) {
            if (this.standbyPlan && !this.standbyFailed) {
              this.standbyReady = true;
            }
            return;
          }
          if (v !== this.video) return;
          this._onMediaReady();
        });
        v.addEventListener("loadeddata", () => {
          if (v === this.standbyVideo && this.standbyPlan && !this.standbyFailed) {
            this.standbyReady = true;
          }
        });
        v.addEventListener("resize", () => {
          if (v !== this.video) return;
          this._syncFullscreenState();
        });
        v.addEventListener("canplay", () => {
          if (v === this.standbyVideo) {
            if (this.standbyPlan && !this.standbyFailed) {
              this.standbyReady = true;
            }
            return;
          }
          if (v !== this.video) return;
          clearTimeout(this.loadingDelayTimer);
          if (this.loading) {
            this.loading = false;
            this._syncUI();
          }
        });
        v.addEventListener("waiting", () => {
          if (v !== this.video) return;
          if (!this.errorText) {
            this._showHud("正在缓冲...");
          }
        });
        v.addEventListener("playing", () => {
          if (v !== this.video) return;
          clearTimeout(this.loadingDelayTimer);
          if (this.loading) {
            this.loading = false;
            this._syncUI();
          }
          if (!this._hudFlashUntil || Date.now() >= this._hudFlashUntil) {
            this._showHud("");
          }
          this.shell.classList.remove("paused");
          this._syncPlayButton();
          if (this.controlsVisible) {
            this._showControlsTemporarily();
          }
          this._queueNextPreload(450);
        });
        v.addEventListener("pause", () => {
          if (v !== this.video) return;
          this.shell.classList.add("paused");
          this._syncPlayButton();
          this._flushWatchProgress();
        });
        v.addEventListener("timeupdate", () => {
          if (v !== this.video) return;
          if (!draggingSeek && Number.isFinite(v.duration) && v.duration > 0) {
            seekInput.value = String(Math.round((v.currentTime / v.duration) * 1000));
            timeCurEl.textContent = formatTime(v.currentTime);
            timeDurEl.textContent = formatTime(v.duration);
          }
          this._tickDanmaku();
        });
        v.addEventListener("ended", () => {
          if (v !== this.video) return;
          const snap = this._captureWatchSnapshot();
          if (snap) {
            this._sendWatchSnapshot(snap);
          }
          if (this.prefs().autoAdvance) {
            const nextNum = this.currentIndex + 1;
            const hasNext = this.episodes.some((e) => e.number === nextNum);
            if (hasNext) {
              this._flashHud(`第 ${nextNum} 集`, 950);
              this._loadEpisode(nextNum, 0, false);
            }
          }
        });
        v.addEventListener("error", () => {
          if (v === this.standbyVideo) {
            if (this.standbyPlan && !this.ignoringStandbyError && !this.standbyHls) {
              this.standbyFailed = true;
            }
            return;
          }
          if (v !== this.video) return;
          if (!this.active || !this.plan || this.ignoringMediaError) return;
          if (this.hls) return;
          const srcAttr = v.getAttribute("src") || "";
          if (!srcAttr) return;
          const errObj = v.error;
          const code = errObj ? errObj.code : 0;
          if (code === 1) return;
          this._onNativeVideoError(srcAttr, code);
        });
      };

      bindVideoEvents(this.video);
      bindVideoEvents(this.standbyVideo);

      const previewSeek = (val) => {
        draggingSeek = true;
        if (Number.isFinite(this.video.duration) && this.video.duration > 0) {
          const target = (Number(val) / 1000) * this.video.duration;
          root.querySelector(".p-time-cur").textContent = formatTime(target);
        }
      };
      const commitSeek = (val) => {
        if (Number.isFinite(this.video.duration) && this.video.duration > 0) {
          this.video.currentTime = (Number(val) / 1000) * this.video.duration;
          this.danmakuFired.clear();
          this._ensureDanmakuWindow();
        }
        draggingSeek = false;
      };

      seekInput.addEventListener("input", () => previewSeek(seekInput.value));
      seekInput.addEventListener("change", () => commitSeek(seekInput.value));
      this._bindRangeTouchRotation(seekInput, previewSeek, commitSeek);

      volInput.addEventListener("input", () => {
        this._applyVolume(Number(volInput.value) / 100, true);
      });
      this._bindRangeTouchRotation(volInput, (val) => {
        this._applyVolume(val / 100, true);
      });

      root.querySelector(".p-speed-sel").addEventListener("change", (e) => {
        this.changeSpeed(Number(e.target.value));
      });
      root.querySelector(".p-quality-sel").addEventListener("change", (e) => {
        this.changeQuality(Number(e.target.value));
      });

      root.querySelectorAll("[data-act]").forEach((btn) => {
        btn.addEventListener("click", (ev) => {
          ev.stopPropagation();
          const act = btn.getAttribute("data-act");
          this._handleAction(act);
        });
      });

      const keepControlsAlive = () => {
        if (this.controlsVisible && !this.locked) {
          this._showControlsTemporarily();
        }
      };
      root.querySelectorAll(".player-top-bar, .player-bottom-bar").forEach((bar) => {
        bar.addEventListener("pointerdown", keepControlsAlive);
        bar.addEventListener("input", keepControlsAlive);
        bar.addEventListener("change", keepControlsAlive);
      });

      this._bindStageGestures(stage);

      clearInterval(this.progressSaveTimer);
      this.progressSaveTimer = setInterval(() => {
        if (this.active) this._flushWatchProgress();
      }, 5000);
    }

    _stageMetrics(stage, clientX, clientY, startX, startY) {
      const rect = stage.getBoundingClientRect();
      const rotated = !!(this.shell && this.shell.classList.contains("fs-rotated-landscape"));
      if (rotated) {
        const screenDx = clientX - (startX != null ? startX : clientX);
        const screenDy = clientY - (startY != null ? startY : clientY);
        return {
          dx: screenDy,
          dy: -screenDx,
          width: stage.offsetWidth || rect.height || 1,
          height: stage.offsetHeight || rect.width || 1,
          relX: (clientY - rect.top) / Math.max(1, rect.height),
          startRelX: startY != null ? (startY - rect.top) / Math.max(1, rect.height) : 0.5
        };
      }
      return {
        dx: clientX - (startX != null ? startX : clientX),
        dy: clientY - (startY != null ? startY : clientY),
        width: rect.width || stage.offsetWidth || 1,
        height: rect.height || stage.offsetHeight || 1,
        relX: (clientX - rect.left) / Math.max(1, rect.width),
        startRelX: startX != null ? (startX - rect.left) / Math.max(1, rect.width) : 0.5
      };
    }

    _bindStageGestures(stage) {
      let pointerDown = null;
      let gestureMode = "";
      let longPressTimer = null;
      let singleTapTimer = null;
      let wasLongPress = false;
      let lastTapAt = 0;
      let lastTapX = 0;
      let lastTapY = 0;
      let lastNonMouseAt = 0;
      let lastHoverWakeAt = 0;
      let lastHoverX = -1;
      let lastHoverY = -1;

      const isInteractiveTarget = (target) =>
        !!(
          target &&
          target.closest(
            "button, input, select, option, textarea, a, .p-btn, .player-range, .player-lock-btn, .player-center-state, .player-side-panel"
          )
        );

      stage.addEventListener("dblclick", (e) => {
        if (Date.now() - lastNonMouseAt < 1200) return;
        if (isInteractiveTarget(e.target)) return;
        if (this.locked) return;
        clearTimeout(singleTapTimer);
        singleTapTimer = null;
        const m = this._stageMetrics(stage, e.clientX, e.clientY);
        if (this.prefs().doubleTapSeek && (m.relX < 0.3 || m.relX > 0.7)) {
          this._seekBy(m.relX < 0.3 ? -10 : 10);
        } else {
          this._togglePlay(true);
        }
      });

      stage.addEventListener("pointerdown", (e) => {
        if (e.pointerType !== "mouse") {
          lastNonMouseAt = Date.now();
        } else {
          if (e.button !== 0 || Date.now() - lastNonMouseAt < 1200) return;
        }
        if (isInteractiveTarget(e.target)) return;
        if (this.mobileEpsOpen) {
          this.mobileEpsOpen = false;
          this.shell.classList.remove("mobile-eps-open");
          const epsBtn = this.shell.querySelector('[data-act="episodes"]');
          if (epsBtn) epsBtn.classList.remove("active");
          return;
        }
        const recentlyHoverWoke = e.pointerType === "mouse" && Date.now() - lastHoverWakeAt < 450;
        pointerDown = {
          id: e.pointerId,
          x: e.clientX,
          y: e.clientY,
          controlsVisibleAtDown: this.controlsVisible && !recentlyHoverWoke,
          startTime: this.video ? this.video.currentTime : 0,
          startVol: this.volume,
          startBright: this.brightness
        };
        gestureMode = "";
        wasLongPress = false;
        clearTimeout(longPressTimer);
        if (!this.locked && this.video && !this.video.paused) {
          longPressTimer = setTimeout(() => {
            wasLongPress = true;
            this.video.playbackRate = 2.0;
            this._showHud("⏩ 2.0x 倍速播放中");
          }, 480);
        }
      });

      stage.addEventListener("pointermove", (e) => {
        if (e.pointerType === "mouse") {
          if (
            !pointerDown &&
            !singleTapTimer &&
            Date.now() - lastNonMouseAt > 1200 &&
            !this.locked &&
            !isInteractiveTarget(e.target)
          ) {
            if (lastHoverX >= 0 && Math.hypot(e.clientX - lastHoverX, e.clientY - lastHoverY) < 4) {
              return;
            }
            lastHoverX = e.clientX;
            lastHoverY = e.clientY;
            if (!this.controlsVisible) {
              lastHoverWakeAt = Date.now();
            }
            this._showControlsTemporarily();
          }
          return;
        }
        lastNonMouseAt = Date.now();
        if (!pointerDown || e.pointerId !== pointerDown.id) return;
        const m = this._stageMetrics(stage, e.clientX, e.clientY, pointerDown.x, pointerDown.y);
        if (Math.abs(m.dx) > 14 || Math.abs(m.dy) > 14) {
          clearTimeout(longPressTimer);
        }
        if (this.locked || wasLongPress) return;
        if (!gestureMode) {
          if (Math.abs(m.dx) > 22 && Math.abs(m.dx) > Math.abs(m.dy) * 1.25) {
            gestureMode = "seek";
          } else if (Math.abs(m.dy) > 24 && Math.abs(m.dy) > Math.abs(m.dx) * 1.25) {
            if (this.prefs().swipeLevels && (m.startRelX < 0.36 || m.startRelX > 0.64)) {
              gestureMode = m.startRelX < 0.36 ? "brightness" : "volume";
            } else {
              gestureMode = "episode";
            }
          }
        }
        if (gestureMode === "seek" && this.video && Number.isFinite(this.video.duration)) {
          const target = Math.max(0, Math.min(this.video.duration, pointerDown.startTime + (m.dx / Math.max(200, m.width)) * 90));
          pointerDown.targetSeek = target;
          this._showHud(`${formatTime(target)} / ${formatTime(this.video.duration)}`);
        } else if (gestureMode === "volume") {
          const nextVol = Math.max(0, Math.min(1, pointerDown.startVol - m.dy / Math.max(160, m.height * 0.7)));
          this._applyVolume(nextVol, true);
          if (this._isVolumeReadOnly() && nextVol > 0.01) {
            this._showHud("🔊 iOS 请按手机侧边键调系统音量");
          } else {
            this._showHud(this.muted ? "🔇 网页已静音" : `🔊 网页音量 ${Math.round(this.volume * 100)}%`);
          }
        } else if (gestureMode === "brightness") {
          const nextB = Math.max(0.2, Math.min(1.4, pointerDown.startBright - m.dy / Math.max(160, m.height * 0.7)));
          this._applyBrightness(nextB);
          this._showHud(`☀ 画面亮度 ${Math.round(this.brightness * 100)}%`);
        }
      });

      const finishPointer = (e) => {
        if (e.pointerType !== "mouse") {
          lastNonMouseAt = Date.now();
        } else if (Date.now() - lastNonMouseAt < 1200) {
          pointerDown = null;
          return;
        }
        if (!pointerDown || e.pointerId !== pointerDown.id) return;
        clearTimeout(longPressTimer);
        const m = this._stageMetrics(stage, e.clientX, e.clientY, pointerDown.x, pointerDown.y);
        const p = pointerDown;
        pointerDown = null;

        if (wasLongPress) {
          if (this.video) this.video.playbackRate = this.speed;
          this._showHud("");
          return;
        }

        if (e.type === "pointercancel") {
          if (gestureMode) this._showHud("");
          return;
        }

        if (gestureMode === "seek" && typeof p.targetSeek === "number" && this.video) {
          this.video.currentTime = p.targetSeek;
          this.danmakuFired.clear();
          this._showHud("");
          return;
        }
        if (gestureMode === "volume" || gestureMode === "brightness") {
          this._showHud("");
          return;
        }
        if (gestureMode === "episode" && Math.abs(m.dy) > 68) {
          if (m.dy < 0) {
            this._stepEpisode(1);
          } else {
            this._stepEpisode(-1);
          }
          return;
        }

        if (Math.abs(m.dx) < 16 && Math.abs(m.dy) < 16) {
          const applySingleTap = () => {
            singleTapTimer = null;
            if (!p.controlsVisibleAtDown) {
              this._showControlsTemporarily();
            } else {
              this.controlsVisible = false;
              clearTimeout(this.hideControlsTimer);
              if (this.shell) this.shell.classList.add("controls-hidden");
            }
          };

          if (e.pointerType !== "mouse") {
            const now = Date.now();
            const dist = Math.hypot(e.clientX - lastTapX, e.clientY - lastTapY);
            if (!this.locked && now - lastTapAt < 330 && dist < 72) {
              clearTimeout(singleTapTimer);
              singleTapTimer = null;
              lastTapAt = 0;
              if (this.prefs().doubleTapSeek && (m.relX < 0.3 || m.relX > 0.7)) {
                this._seekBy(m.relX < 0.3 ? -10 : 10);
              } else {
                this._togglePlay(true);
              }
              return;
            }
            lastTapAt = now;
            lastTapX = e.clientX;
            lastTapY = e.clientY;
            clearTimeout(singleTapTimer);
            singleTapTimer = setTimeout(applySingleTap, 240);
            return;
          }

          clearTimeout(singleTapTimer);
          singleTapTimer = setTimeout(applySingleTap, 240);
        }
      };

      stage.addEventListener("pointerup", finishPointer);
      stage.addEventListener("pointercancel", finishPointer);
    }

    _handleAction(act) {
      switch (act) {
        case "close":
          this.close();
          break;
        case "lock":
          this.locked = !this.locked;
          this.shell.classList.toggle("locked", this.locked);
          this.shell.querySelector(".player-lock-btn").textContent = this.locked ? "🔒" : "🔓";
          if (this.locked) {
            this.controlsVisible = false;
            this.shell.classList.add("controls-hidden");
            this._flashHud("🔒 已锁定屏幕控制", 1100);
          } else {
            this._showControlsTemporarily();
            this._flashHud("🔓 已解锁屏幕控制", 1100);
          }
          break;
        case "play":
          this._togglePlay();
          break;
        case "prev":
          this._stepEpisode(-1);
          break;
        case "next":
          this._stepEpisode(1);
          break;
        case "danmaku":
          this.toggleDanmaku();
          break;
        case "mute":
          if (this.video) {
            if (this.volume <= 0.001) {
              this._applyVolume(DEFAULT_VOLUME, true);
            } else {
              this.muted = !this.muted;
              this.video.muted = this.muted;
              this._syncVolumeButton();
            }
            this._flashHud(this.muted ? "🔇 已静音" : `🔊 音量 ${Math.round(this.volume * 100)}%`);
          }
          break;
        case "episodes":
          this.mobileEpsOpen = !this.mobileEpsOpen;
          this._syncUI();
          break;
        case "close-episodes":
          this.mobileEpsOpen = false;
          this._syncUI();
          break;
        case "route":
          this.switchRouteManually();
          break;
        case "pip":
          if (document.pictureInPictureElement) {
            document.exitPictureInPicture().catch(() => {});
          } else if (this.video && this.video.requestPictureInPicture) {
            this.video.requestPictureInPicture().catch(() => toast("当前浏览器不支持画中画"));
          } else if (this.video && typeof this.video.webkitSetPresentationMode === "function") {
            const nextMode = this.video.webkitPresentationMode === "picture-in-picture" ? "inline" : "picture-in-picture";
            try {
              this.video.webkitSetPresentationMode(nextMode);
            } catch (_) {
              toast("当前浏览器不支持画中画");
            }
          }
          break;
        case "wide":
          this.wideMode = !this.wideMode;
          this.shell.classList.toggle("wide-mode", this.wideMode);
          this._syncUI();
          break;
        case "fullscreen":
          if (this._isFullscreen()) {
            this._exitFullscreen();
          } else {
            this._enterFullscreen();
          }
          break;
      }
    }

    _togglePlay(withHud) {
      if (!this.video) return;
      if (this.video.paused) {
        this.video.play().catch(() => {});
        if (withHud) {
          this._flashHud("▶ 继续播放", 900);
          this._showControlsTemporarily();
        }
      } else {
        this.video.pause();
        if (withHud) {
          this._flashHud("⏸ 已暂停", 900);
          this._showControlsTemporarily();
        }
      }
    }

    _seekBy(deltaSec) {
      if (!this.video || !Number.isFinite(this.video.duration)) return;
      const next = Math.max(0, Math.min(this.video.duration, this.video.currentTime + deltaSec));
      this.video.currentTime = next;
      this.danmakuFired.clear();
      this._flashHud(`${deltaSec > 0 ? "⏩ +" : "⏪ "}${deltaSec}s (${formatTime(next)})`);
    }

    _stepEpisode(offset) {
      const target = this.currentIndex + offset;
      const found = this.episodes.find((e) => e.number === target);
      if (!found) {
        const msg = offset > 0 ? "已经是最后一集" : "已经是第一集";
        this._flashHud(msg, 1100);
        toast(msg);
        return;
      }
      this._loadEpisode(target, 0, false);
    }

    _handleKeyDown(e) {
      if (!this.active) return;
      const modalOpen = !document.getElementById("modal-root").classList.contains("hidden");
      if (modalOpen) return;
      const tag = (e.target && e.target.tagName) || "";
      if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;

      switch (e.key) {
        case "Escape":
          e.preventDefault();
          if (this.mobileEpsOpen) {
            this.mobileEpsOpen = false;
            this._syncUI();
          } else if (this._isFullscreen()) {
            this._exitFullscreen();
          } else {
            this.close();
          }
          break;
        case " ":
        case "k":
        case "K":
          e.preventDefault();
          this._togglePlay();
          break;
        case "ArrowLeft":
          e.preventDefault();
          this._seekBy(-5);
          break;
        case "ArrowRight":
          e.preventDefault();
          this._seekBy(5);
          break;
        case "ArrowUp":
          e.preventDefault();
          this._applyVolume(Math.min(1, this.volume + 0.08), true);
          this._flashHud(`🔊 音量 ${Math.round(this.volume * 100)}%`);
          break;
        case "ArrowDown":
          e.preventDefault();
          this._applyVolume(Math.max(0, this.volume - 0.08), true);
          this._flashHud(`🔊 音量 ${Math.round(this.volume * 100)}%`);
          break;
        case "f":
        case "F":
          e.preventDefault();
          this._handleAction("fullscreen");
          break;
        case "m":
        case "M":
          e.preventDefault();
          this._handleAction("mute");
          break;
        case "[":
        case "PageUp":
          e.preventDefault();
          this._stepEpisode(-1);
          break;
        case "]":
        case "PageDown":
          e.preventDefault();
          this._stepEpisode(1);
          break;
      }
    }

    _syncPlayButton() {
      if (!this.shell || !this.video) return;
      const btn = this.shell.querySelector('[data-act="play"]');
      if (btn) btn.textContent = this.video.paused ? "▶ 播放" : "⏸ 暂停";
    }

    _syncUI() {
      if (!this.shell) return;
      this.shell.classList.toggle("mobile-eps-open", !!this.mobileEpsOpen);
      const epsBtn = this.shell.querySelector('[data-act="episodes"]');
      if (epsBtn) {
        epsBtn.classList.toggle("active", !!this.mobileEpsOpen);
        const compactPortrait = (window.innerWidth || 0) > 0 && window.innerWidth <= 430 && !this._isFullscreen();
        epsBtn.textContent = compactPortrait ? "选集" : `选集 (${this.episodes.length})`;
      }

      const ep = this._currentEpisodeObj();
      this.shell.querySelector(".p-title").textContent = `${this.drama.title} · ${ep.title}`;
      const subParts = [];
      if (this.plan) {
        subParts.push(this.plan.local ? "本地离线文件" : "在线播放");
        if (this.plan.quality > 0) subParts.push(`${this.plan.quality}P`);
      }
      this.shell.querySelector(".p-subtitle").textContent = subParts.join(" · ");

      const routeBtn = this.shell.querySelector('[data-act="route"]');
      if (this.plan && !this.plan.local && (this.plan.routeCount || 1) > 1) {
        routeBtn.classList.remove("hidden");
        routeBtn.textContent = `线路 ${(this.plan.routeIndex || 0) + 1}/${this.plan.routeCount}`;
      } else {
        routeBtn.classList.add("hidden");
      }

      const dmBtn = this.shell.querySelector('[data-act="danmaku"]');
      if (this.drama.source === "hongguo") {
        dmBtn.classList.remove("hidden");
        dmBtn.classList.toggle("active", this.danmakuEnabled);
        dmBtn.textContent = this.danmakuEnabled ? "弹幕 开" : "弹幕 关";
      } else {
        dmBtn.classList.add("hidden");
      }

      const wideBtn = this.shell.querySelector(".p-wide-btn");
      wideBtn.classList.toggle("active", this.wideMode);

      const speedSel = this.shell.querySelector(".p-speed-sel");
      speedSel.value = String(this.speed);

      const qSel = this.shell.querySelector(".p-quality-sel");
      const qualities = (this.plan && Array.isArray(this.plan.qualities) && this.plan.qualities.length > 0)
        ? [0, ...this.plan.qualities]
        : [0, 1080, 720, 540, 480, 360];
      qSel.innerHTML = qualities
        .map((q) => `<option value="${q}" style="color:#000;" ${q === this.preferredQuality ? "selected" : ""}>${q === 0 ? (this.plan && this.plan.quality ? `自动(${this.plan.quality}P)` : "画质 自动") : `${q}P`}</option>`)
        .join("");

      const center = this.shell.querySelector(".player-center-state");
      if (this.loading) {
        center.classList.remove("hidden");
        center.innerHTML = `<div class="boot-spinner"></div><div>正在解析第 ${this.currentIndex} 集播放流...</div>`;
      } else if (this.errorText) {
        const canFallback = this.plan && this.plan.session && (this.plan.routeIndex || 0) + 1 < (this.plan.routeCount || 1);
        center.classList.remove("hidden");
        center.innerHTML = `
          <div style="font-size:15px;font-weight:700;max-width:440px;">${esc(this.errorText)}</div>
          <div style="display:flex;gap:10px;flex-wrap:wrap;justify-content:center;margin-top:6px;">
            <button class="btn btn-primary" id="p-err-retry">重新解析</button>
            ${canFallback ? `<button class="btn" id="p-err-fallback">切换备用线路</button>` : ""}
            <button class="btn" id="p-err-close">关闭播放器</button>
          </div>
        `;
        center.querySelector("#p-err-retry").onclick = () => this._loadEpisode(this.currentIndex, this.resumePosition, false);
        const fb = center.querySelector("#p-err-fallback");
        if (fb) fb.onclick = () => this.switchRouteManually();
        center.querySelector("#p-err-close").onclick = () => this.close();
      } else {
        center.classList.add("hidden");
        center.innerHTML = "";
      }

      const sideEps = this.shell.querySelector(".player-side-eps");
      if (sideEps) {
        const prevCur = sideEps.querySelector(".ep-btn.current");
        const prevNum = prevCur ? Number(prevCur.getAttribute("data-side-ep")) : -1;
        if (prevNum !== this.currentIndex) {
          if (prevCur) {
            prevCur.classList.remove("current");
            prevCur.style.color = "#e2e8f0";
          }
          const nextCur = sideEps.querySelector(`[data-side-ep="${this.currentIndex}"]`);
          if (nextCur) {
            nextCur.classList.add("current");
            nextCur.style.color = "var(--accent)";
            nextCur.scrollIntoView({ block: "nearest" });
          }
        }
      }

      this._syncPlayButton();
      this._syncVolumeButton();
      this._syncFullscreenState();
      if (this.controlsVisible) {
        this._showControlsTemporarily();
      }
    }
  }

  window.ZGJPlayer = new ZGJPlayerController();
})();
