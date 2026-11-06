# 真果鉴 · 网页版 (ZhenGuoJian-Web)

[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.24%2B-00ADD8.svg)](https://go.dev/)
[![PWA](https://img.shields.io/badge/Web-Responsive%20SPA%20%2F%20PWA-FF6B35.svg)](#快速开始)

**真果鉴网页版**是一款**单文件、零外部运行时依赖（`CGO_ENABLED=0`）**的跨平台短剧与影视自托管 Web 应用。内嵌多站源请求签名引擎、实时音视频流解密重封装服务与响应式 PWA 前端，只需运行单个可执行文件，即可在手机、平板、电脑或智能电视浏览器中直接使用。

> 移动端原生客户端（Android / Android TV / iOS）见姐妹仓库：[**ZhenGuoJian-Mobile**](https://github.com/wzdyf1992/ZhenGuoJian-Mobile)。两个项目的备份文件（`schema: 1`）完全互通。

---

## 核心特性

- **单文件免依赖部署**：纯 Go 静态编译，前端静态资源与图标通过 `embed` 内嵌进二进制文件，支持 Windows、Linux（x86_64 / ARM64）、macOS（Apple Silicon / Intel）即拷即用。
- **全站源聚合与独立开关**：内置 11 个短剧 / 影视 / 番剧站源，支持管理员在「设置 › 站源」逐个启用或停用，支持输入联想搜索与多季剧集聚合检索。
- **纯 Go 实时解密与无缝连播**：
  - 内置 ISO/IEC 23001-7 `cenc-aes-ctr` MP4 HTTP Range 实时流解密，以及 HLS 播放列表同源重写与伪装 `.ts` 分片头剥离。
  - 双 `<video>` 后台静音预缓冲管线 + 服务端 `moov` 索引与首段明文内存预热，自动连播与切下一集秒开起播。
  - 针对源站下发的 `HEIC` 封面，支持纯 Go 提取 HEVC 主图帧配合浏览器 `WebCodecs` / 单帧 MP4 解码回写 JPEG 缓存（若宿主机装有 FFmpeg 则自动后台硬件/软件转码）。
- **多端自适应交互与 PWA 支持**：
  - 自动适配手机底栏竖屏沉浸式播控、平板/电脑可折叠侧栏与左右分栏选集、电视大屏遥控器焦点导航与拼音首字母软键盘。
  - 移动端播放器支持左侧上下滑调节画面亮度、右侧上下滑调节音量、中间双击暂停/播放、两侧双击快退/快进及横屏全屏旋转锁定。
  - 支持红果分段滚动弹幕、倍速与清晰度记忆、6 套主题色与深浅色模式，HTTPS 环境下可一键安装为独立桌面/主屏 PWA 应用。
- **离线下载与无损转 MP4**：后台并发下载分集，完成后纯 Go 自动解密并转封装为标准 `.mp4` 文件，支持浏览器直接取回或按集保存。
- **多用户权限与跨端备份**：支持多用户独立保存追剧、观看记录与搜索历史，管理员可配置密码（3–32 位）、访问站源白名单与下载权限；支持一键导出/导入 `.json` 备份并与移动端客户端无缝迁移。

> 当前版本（`1.0.13+113`）网页端双 `<video>` 预缓冲、移动端触控手势与 PWA 安装能力已在本地浏览器验证，多机型与公网环境标为开发快照，待集中复验。

---

## 内置站源

| 站源 | 内容范围 | 搜索方式 |
| --- | --- | --- |
| 红果 | 短剧、漫剧、AI 剧；推荐混排流、榜单、题材筛选 | 在线搜索（含联想词与多季语义匹配） |
| 韩小圈 | 韩剧、韩国电影、综艺、动漫，多线路切换 | 在线搜索 |
| 鬼片 | 恐怖片、电视剧、动漫，多线路切换 | 匹配站点 RSS 最新条目 |
| 青空 | 番剧、剧场动画、特摄 | 在线搜索 |
| 黄豆 | 精选短剧、榜单 | 本机目录缓存检索 |
| 剧果 | 热门、最新、分类短剧 | 在线搜索 |
| 野果 | 分类短剧 | 在线搜索 |
| 帝果 | 分类视频 | 在线搜索 |
| 黄果视频 / 黄果 AI / 黄果旧版 | 视频剧集、AI 短剧与旧版剧库 | 黄果 AI 在线搜索，其余检索本机缓存 |

> 站源可用性、清晰度与区域限制取决于源站及部署主机网络；应用不解除源站 VIP 或其他授权限制。

---

## 快速开始

### 1. 下载运行

前往 [**Releases**](https://github.com/wzdyf1992/ZhenGuoJian-Web/releases) 下载对应系统的可执行文件并直接运行：

```bash
# Linux / macOS
chmod +x ./zhenguojian-web-linux-amd64
./zhenguojian-web-linux-amd64

# Windows PowerShell
.\zhenguojian-web-windows-amd64.exe -open
```

启动后终端会打印本机访问地址（默认 `http://127.0.0.1:7080`）及局域网 IPv4 地址（自动过滤虚拟网卡与代理隧道），在同一局域网的电脑、手机或电视浏览器打开即可。

### 2. 命令行参数与环境变量

| 参数 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `-listen` | `ZGJ_LISTEN` / `PORT` | `0.0.0.0:7080` | HTTP 监听地址与端口 |
| `-data-dir` | `ZGJ_DATA_DIR` | 系统用户配置目录 `/zhenguojian-web` | 配置、用户数据与剧库缓存目录 |
| `-downloads-dir` | `ZGJ_DOWNLOADS_DIR` | `<data-dir>/downloads` | 离线视频下载保存目录 |
| `-open` | — | `false` | 启动后自动在默认浏览器打开页面 |
| `-disable-images` | `DISABLE_REMOTE_IMAGES` | `false` | 禁用远程站源海报请求并返回本地占位图 |
| `-ui-dir` | `ZGJ_UI_DIR` | 空（使用内嵌静态资源） | 前端热调试时指定外部静态目录覆盖 |

---

## 本地编译构建

### 环境要求

- **Go**：`1.24.1+`
- **Python**：`3.10+`（仅在使用跨平台批量构建脚本时需要）

### 编译命令

```bash
# 直接启动开发服务
go run ./web

# 编译当前平台单文件可执行程序
python scripts/build.py

# 一次性交叉编译 Windows / Linux (amd64, arm64) / macOS (arm64, amd64) 全部平台产物（输出至 dist/）
python scripts/build.py --os all
```

### 代码检查与测试

```bash
go vet ./...
go test -race ./...
```

---

## 项目结构

```text
ZhenGuoJian-Web/
├── core/                  # Go 多站源核心引擎（站源解析、签名、目录缓存、流代理、弹幕、下载调度）
├── web/                   # 独立 Web 服务端（HTTP API、纯 Go CENC 实时解密、HLS 封装、HEIC 处理、多用户存储）
│   └── ui/                # 内嵌响应式前端（HTML / CSS / 原生 JS 模块 / PWA Manifest & Service Worker）
├── scripts/
│   └── build.py           # 跨平台静态单文件构建脚本
├── .github/workflows/     # GitHub Actions 自动化编译与发布工作流
├── go.mod
├── VERSION
└── LICENSE
```

---

## 免责声明

- 本项目仅供个人学习与音视频流媒体技术研究。
- 本项目与红果短剧及其他各站源运营方无任何关联，未获得其授权或认可；文中涉及的名称与商标归各自权利人所有。
- 程序本身不存储、不上传、不分发任何影视内容，所有数据均由使用者所在设备实时请求第三方站点获取，版权归原权利人所有；程序不解除源站 VIP 或其他授权限制。
- 使用者应遵守所在地法律法规及各站点服务条款，因部署或使用本项目产生的一切后果由使用者自行承担。

---

## 开源协议与致谢

本项目核心站源框架改编自 [fish2018](https://github.com/fish2018) 的开源代码，整体以 [**GNU General Public License v3.0 (GPL-3.0)**](LICENSE) 协议发布。第三方依赖遵循各自的开源许可证。
