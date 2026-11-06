package main

import (
	"context"
	"duanjuapp/native/core"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

var appVersion = "1.0.13+113"

type lanInterfaceInfo struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	MaskLen int    `json:"maskLen"`
}

var virtualIfKeywords = []string{
	"tun", "tap", "utun", "ppp", "vpn", "clash", "mihomo", "sing", "surge",
	"wireguard", "wg", "tailscale", "zerotier", "vmware", "vmnet", "vbox",
	"virtual", "vethernet", "docker", "br-", "veth", "wsl", "hyper-v", "loopback",
}

func isVirtualInterfaceName(name string) bool {
	lower := strings.ToLower(name)
	for _, kw := range virtualIfKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func isUsableLANIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() || ip4.IsMulticast() || ip4.IsUnspecified() {
		return false
	}
	if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return false
	}
	if ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19) {
		return false
	}
	return ip4.IsPrivate()
}

func listLANInterfaces() []lanInterfaceInfo {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	type scored struct {
		info  lanInterfaceInfo
		score int
	}
	var list []scored
	seen := map[string]bool{}
	for _, iface := range ifaces {
		if (iface.Flags&net.FlagUp) == 0 || (iface.Flags&net.FlagLoopback) != 0 {
			continue
		}
		virt := isVirtualInterfaceName(iface.Name)
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil || !isUsableLANIPv4(ip4) {
				continue
			}
			ipStr := ip4.String()
			if seen[ipStr] {
				continue
			}
			seen[ipStr] = true
			ones, _ := ipNet.Mask.Size()
			sc := 10
			if virt {
				sc -= 50
			}
			if ip4[0] == 192 && ip4[1] == 168 {
				sc += 20
			} else if ip4[0] == 10 {
				sc += 10
			}
			list = append(list, scored{
				info:  lanInterfaceInfo{Name: iface.Name, Address: ipStr, MaskLen: ones},
				score: sc,
			})
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].score > list[j].score
	})
	out := make([]lanInterfaceInfo, 0, len(list))
	for _, item := range list {
		if item.score > -20 || len(out) == 0 {
			out = append(out, item.info)
		}
	}
	return out
}

func defaultListenAddress() string {
	if v := strings.TrimSpace(os.Getenv("ZGJ_LISTEN")); v != "" {
		return v
	}
	if port := strings.TrimSpace(os.Getenv("PORT")); port != "" {
		if strings.Contains(port, ":") {
			return port
		}
		return "0.0.0.0:" + port
	}
	return "0.0.0.0:7080"
}

func defaultDataDirectory() string {
	if v := strings.TrimSpace(os.Getenv("ZGJ_DATA_DIR")); v != "" {
		return v
	}
	folder := "hongguojian-web"
	if core.AllSourcesEnabled() {
		folder = "zhenguojian-web"
	}
	if base, err := os.UserConfigDir(); err == nil && base != "" {
		return filepath.Join(base, folder)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, "."+folder)
	}
	return filepath.Join(".", folder+"-data")
}

func envBoolFlag(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "1" || v == "true" || v == "yes"
}

func openBrowserURL(targetURL string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", targetURL)
	case "darwin":
		cmd = exec.Command("open", targetURL)
	default:
		cmd = exec.Command("xdg-open", targetURL)
	}
	_ = cmd.Start()
}

func main() {
	var (
		listenAddr    string
		dataDir       string
		downloadsDir  string
		uiDir         string
		disableImages bool
		autoOpen      bool
	)

	flag.StringVar(&listenAddr, "listen", defaultListenAddress(), "HTTP 监听地址，例如 0.0.0.0:7080 或 127.0.0.1:7080")
	flag.StringVar(&dataDir, "data-dir", defaultDataDirectory(), "应用数据与配置存储目录")
	flag.StringVar(&downloadsDir, "downloads-dir", strings.TrimSpace(os.Getenv("ZGJ_DOWNLOADS_DIR")), "视频离线下载保存目录（默认位于 data-dir/downloads）")
	flag.StringVar(&uiDir, "ui-dir", strings.TrimSpace(os.Getenv("ZGJ_UI_DIR")), "前端静态资源覆盖目录（开发调试用）")
	flag.BoolVar(&disableImages, "disable-images", envBoolFlag("DISABLE_REMOTE_IMAGES"), "禁用远程站源海报加载并返回合成占位图")
	flag.BoolVar(&autoOpen, "open", false, "启动后自动在默认浏览器打开网页")
	flag.Parse()

	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析数据目录失败: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(absDataDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "创建数据目录失败: %v\n", err)
		os.Exit(1)
	}

	if downloadsDir == "" {
		downloadsDir = filepath.Join(absDataDir, "downloads")
	}
	absDownloadsDir, err := filepath.Abs(downloadsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析下载目录失败: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(absDownloadsDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "创建下载目录失败: %v\n", err)
		os.Exit(1)
	}

	absUIDir := ""
	if strings.TrimSpace(uiDir) != "" {
		absUIDir, err = filepath.Abs(uiDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "解析 UI 目录失败: %v\n", err)
			os.Exit(1)
		}
	}

	initReq, _ := json.Marshal(map[string]any{
		"action":    "initialize",
		"directory": absDataDir,
		"downloads": absDownloadsDir,
	})
	initRaw := core.NativeRequest(string(initReq))
	var initEnv struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(initRaw), &initEnv); err != nil || !initEnv.OK {
		msg := initEnv.Error
		if msg == "" {
			msg = initRaw
		}
		fmt.Fprintf(os.Stderr, "核心引擎初始化失败: %s\n", msg)
		os.Exit(1)
	}

	diary := newWebDiary()
	diary.add(fmt.Sprintf("%s 网页版 %s 启动", appDisplayName(), appVersion))

	store, err := openWebStore(absDataDir, diary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "本地数据存储初始化失败: %v\n", err)
		core.ShutdownEngine()
		os.Exit(1)
	}

	tempRoot := filepath.Join(absDataDir, "web_tmp")
	_ = os.RemoveAll(tempRoot)
	_ = os.MkdirAll(tempRoot, 0700)

	appServer := newWebAppServer(appVersion, disableImages, absUIDir, tempRoot, store, diary)

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "监听地址 %s 失败: %v\n", listenAddr, err)
		core.ShutdownEngine()
		os.Exit(1)
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go appServer.converter.run(ctx)
	go store.runBackgroundFollowChecker(ctx)

	httpServer := &http.Server{
		Handler:           appServer.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	localURL := fmt.Sprintf("http://127.0.0.1:%d", actualPort)
	fmt.Printf("%s 网页版 v%s 已启动\n", appDisplayName(), appVersion)
	fmt.Printf("  数据目录: %s\n", absDataDir)
	fmt.Printf("  下载目录: %s\n", absDownloadsDir)
	fmt.Printf("  本机访问: %s\n", localURL)
	hostPart, _, _ := net.SplitHostPort(listenAddr)
	hostPart = strings.TrimSpace(hostPart)
	if hostPart == "" || hostPart == "0.0.0.0" || hostPart == "::" || hostPart == "[::]" {
		for _, nic := range listLANInterfaces() {
			fmt.Printf("  局域网访问 (%s): http://%s:%d\n", nic.Name, nic.Address, actualPort)
		}
	}
	if disableImages {
		fmt.Println("  图片保护: 已开启（不请求远程站源海报）")
	}

	if autoOpen {
		go func() {
			time.Sleep(250 * time.Millisecond)
			openBrowserURL(localURL)
		}()
	}

	errCh := make(chan error, 1)
	go func() {
		if serveErr := httpServer.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		} else {
			errCh <- nil
		}
	}()

	select {
	case <-ctx.Done():
		fmt.Println("\n正在关闭网页服务...")
	case serveErr := <-errCh:
		if serveErr != nil {
			fmt.Fprintf(os.Stderr, "HTTP 服务异常退出: %v\n", serveErr)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	core.ShutdownEngine()
	_ = os.RemoveAll(tempRoot)
}
