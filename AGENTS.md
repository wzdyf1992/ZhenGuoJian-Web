# AGENTS.md

本文档供 AI 编程助手快速定位代码边界与并发约束，减少无效检索。

## 1. 架构边界

本项目为纯 Go（`CGO_ENABLED=0`）单文件自托管 Web 服务，内嵌原生 ES Module 前端，无 Node/打包工具链。

- **`core/`（站源与核心引擎，单包 `duanjuapp/native/core`）**
  - `app_runtime.go`：统一 JSON 分派入口 `NativeRequest(json) -> json`，管理单例 `nativeState.engine`。
  - `web_bridge.go`：向 `web/` 暴露的进程内桥接函数（`LoadCoverFile`、`PrepareHEICCover`、`ExtractHEICWebFrame`、`CompletedDownloadFile`、`LocalEpisodeFile`、`ShutdownEngine`）。
  - `provider_*.go`：11 个内置站源的请求、签名、目录解析与取流（**Go `regexp` 基于 RE2，不支持 `(?=...)` 等前瞻语法**，多线路截断须用显式字符串定位）。
  - `app_stream.go` / `app_downloads.go`：核心内部回环流代理与分集下载调度。
  - 新增站源需同步接入：`provider_huangguo.go`、`provider_media.go`、`app_categories.go`、`app_cover_metadata.go`、`app_runtime.go` 及 `web/store.go`（`allKnownSources`）。
- **`web/`（HTTP 服务端、实时解密重封装与多用户存储，`package main`）**
  - `main.go`：CLI 参数解析、核心初始化（`action: "initialize"`）、生命周期信号监听与优雅退出。
  - `server.go`：HTTP 路由（`/api/*` 与内嵌静态资源）、流会话管理、预加载缓存、静态资源热覆盖（`-ui-dir`）。
  - `cenc.go` / `hls_mux.go` / `converter.go`：纯 Go ISO/IEC 23001-7 `cenc-aes-ctr` MP4 HTTP Range 实时流解密（含 `moov` 预热与首段明文缓存）、HLS 同源重写与伪装 `.ts` 头剥离、离线下载转封装标准 MP4。
  - `heic_cover.go`：HEIC 封面纯 Go HEVC 帧提取、单帧 MP4 封装与可选宿主机 FFmpeg 转码缓存。
  - `store.go`：多用户权限、密码校验、追剧/历史记录按用户分文件存储（`users/<id>/*.json`），备份格式（`schema: 1`）与移动端客户端严格互通，修改字段须保持向后兼容。
- **`web/ui/`（内嵌响应式 PWA 前端，通过 `embed` 打包）**
  - `api.js`（接口封装与令牌管理）、`player.js`（双 `<video>` 预缓冲、`hls.js`、弹幕与移动端触控手势）、`app.js`（手机/平板/电脑/电视自适应视图状态机）、`styles.css`。直接修改即可生效，开发时可用 `-ui-dir web/ui` 免重编调试。

## 2. 线程与并发约束

- **禁止持锁执行阻塞 I/O**：`net/http` 每个请求运行在独立 goroutine。`webStore.mu`、`webAppServer` 会话锁及 `core` 内 `engine.mu` / `downloads.mu` 仅保护内存状态与短临界区；**严禁在持锁期间调用 `core.NativeRequest`、发起上游网络请求、解析大文件或执行转码**。
- **锁顺序与状态隔离**：`web/store.go`（`ws.mu`）与 `core` 内部锁相互独立，禁止在 `core` 回调或持锁路径中反向调用 `webStore`，避免死锁。磁盘持久化统一采用「写同目录临时文件 + `os.Rename` 原子替换」。
- **流媒体与封面并发控制**：
  - `cenc.go` 的解密流须支持浏览器并发多分片 HTTP `Range` 读取，只读共享解析后的 `moov` 采样表，底层文件读取使用 `io.ReaderAt`（`ReadAt`）而非共享游标的 `Seek+Read`。
  - `heic_cover.go` 与封面拉取须保持并发信号量限流与同 Key 合并，防止首屏大量卡片触发上游限流或内存峰值。
- **后台协程生命周期**：`converter.run(ctx)` 与 `store.runBackgroundFollowChecker(ctx)` 必须监听 `ctx.Done()` 及时退出；进程退出顺序固定为 `httpServer.Shutdown` -> 后台任务停止 -> `core.ShutdownEngine()` -> 清理 `web_tmp`。
- **前端异步竞态防护**：
  - `player.js` 切集与双 `<video>` 预缓冲必须校验递增请求序号，丢弃过期异步结果；清空备用 `<video>` 的 `src` 时需屏蔽空源触发的 `error` 事件，避免误触发线路降级。
  - 移动端播放手势需隔离 `pointerup` 与浏览器合成 `mousemove` / `dblclick` 的二次触发竞态。

## 3. 构建与校验方式

- **默认配置**：`core/app_build.go` 默认 `buildAllSources = "true"`（全站源真果鉴）。自动化测试与无头验证一律开启 `DISABLE_REMOTE_IMAGES=true`（或 `-disable-images`），不要下载或读取真实站源图片。
- **静态检查与单元测试**：
  ```bash
  gofmt -s -w ./core ./web
  go vet ./...
  DISABLE_REMOTE_IMAGES=true go test -race ./...
  ```
- **本地运行与跨平台构建**：
  ```bash
  # 开发运行（前端热调试可加 -ui-dir web/ui -disable-images）
  go run ./web

  # 构建当前主机单文件二进制（输出至 dist/）
  python scripts/build.py

  # 交叉编译全部平台二进制（Windows / Linux amd64+arm64 / macOS arm64+amd64）
  python scripts/build.py --os all
  ```
- **发布版本更新**：同步更新根目录 `VERSION` 与 `web/main.go` 中的默认 `appVersion`（保持 `x.y.z+build` 格式以兼容更新校验正则）。
