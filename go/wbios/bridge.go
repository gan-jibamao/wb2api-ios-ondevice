// Package wbios 把 workbuddy2api（多账号池反代网关）封装成可被 iOS App 直接调用的 API。
//
// 与命令行版（cmd/server）的差异：
//   - 无 signal / os.Exit：生命周期由 App 掌控；
//   - 数据目录固定在 App 沙盒（config.json / auths/ / data/ 三件套）；
//   - 首次启动自动生成 config.json（关掉依赖外部 python 脚本的两个任务）；
//   - 日志同时写入数据目录 gateway.log；
//   - 启动后就绪探测 + 状态查询（供界面展示）。
//
// 本文件不含 cgo，可直接用于 gomobile bind；c-archive 入口见 ../iosmain。
package wbios

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"workbuddy2api/internal/app"
	"workbuddy2api/internal/auth"
)

// Version 内核版本标识（随 App 版本走）。
const Version = "v1.0-ios"

// DefaultListen 默认监听地址（仅本机回环，同机其它 App 可访问）。
const DefaultListen = "127.0.0.1:7863"

type instance struct {
	app     *app.App
	dataDir string
	logFile *os.File
}

var (
	mu   sync.Mutex
	inst *instance
)

// AccountView 账号（凭证）摘要，供界面展示。
type AccountView struct {
	UID       string `json:"uid"`
	Nickname  string `json:"nickname"`
	Realm     string `json:"realm"`
	Domain    string `json:"domain"`
	ExpiresAt int64  `json:"expires_at"`
	File      string `json:"file"`
}

// Status 运行状态快照（JSON 序列化后交给 Swift 侧）。
type Status struct {
	Running   bool          `json:"running"`
	Addr      string        `json:"addr"`
	Uptime    int           `json:"uptime"`
	DataDir   string        `json:"data_dir"`
	APIKey    string        `json:"api_key"`
	RedisMode string        `json:"redis_mode"`
	Accounts  []AccountView `json:"accounts"`
	Available int           `json:"available"`
	Version   string        `json:"version"`
}

// Running 返回服务是否在运行。
func Running() bool {
	mu.Lock()
	defer mu.Unlock()
	return inst != nil
}

// Addr 返回监听地址（未运行时为空串）。
func Addr() string {
	mu.Lock()
	defer mu.Unlock()
	if inst == nil {
		return ""
	}
	return inst.app.Addr
}

// Dir 返回数据目录（未运行时为空串）。
func Dir() string {
	mu.Lock()
	defer mu.Unlock()
	if inst == nil {
		return ""
	}
	return inst.dataDir
}

// Uptime 已运行秒数。
func Uptime() int {
	mu.Lock()
	defer mu.Unlock()
	if inst == nil {
		return 0
	}
	return inst.app.Uptime()
}

// Start 启动服务。dataDir 为数据目录（App 沙盒内），listen/apiKey 为空时沿用 config.json。
func Start(dataDir, listen, apiKey string) error {
	mu.Lock()
	defer mu.Unlock()
	if inst != nil {
		return fmt.Errorf("服务已在运行：%s", inst.app.Addr)
	}
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return fmt.Errorf("dataDir 不能为空")
	}
	authDir := filepath.Join(dataDir, "auths")
	stateDir := filepath.Join(dataDir, "data")
	for _, d := range []string{dataDir, authDir, stateDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("创建目录失败 %s：%w", d, err)
		}
	}

	cfgPath := filepath.Join(dataDir, "config.json")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		if err := writeDefaultConfig(cfgPath, listen, apiKey, authDir, filepath.Join(stateDir, "state.json")); err != nil {
			return fmt.Errorf("写入默认配置失败：%w", err)
		}
	}

	// 日志：stdout + 数据目录 gateway.log
	logFile, err := os.OpenFile(filepath.Join(dataDir, "gateway.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("打开日志文件失败：%w", err)
	}
	log.SetOutput(io.MultiWriter(os.Stderr, logFile))
	log.SetFlags(log.LstdFlags)

	cfg, err := app.Load(cfgPath)
	if err != nil {
		_ = logFile.Close()
		return fmt.Errorf("加载配置失败：%w", err)
	}
	if l := strings.TrimSpace(listen); l != "" {
		cfg.Listen = l
	}
	if k := strings.TrimSpace(apiKey); k != "" {
		cfg.APIKey = k
	}

	a, err := app.Start(cfg)
	if err != nil {
		_ = logFile.Close()
		return err
	}
	inst = &instance{app: a, dataDir: dataDir, logFile: logFile}
	return nil
}

// Stop 停止服务并释放资源。
func Stop() error {
	mu.Lock()
	defer mu.Unlock()
	if inst == nil {
		return nil
	}
	a := inst
	inst = nil
	err := a.app.Stop()
	if a.logFile != nil {
		_ = a.logFile.Close()
	}
	return err
}

// StatusJSON 返回状态快照（JSON 字符串，供 Swift 解析）。
func StatusJSON() string {
	mu.Lock()
	defer mu.Unlock()

	st := Status{
		Running: inst != nil,
		Version: Version,
		Accounts: []AccountView{},
	}
	if inst != nil {
		st.Addr = inst.app.Addr
		st.Uptime = inst.app.Uptime()
		st.DataDir = inst.dataDir
		st.APIKey = inst.app.Cfg.APIKey
		st.RedisMode = inst.app.RedisMod
		st.Available = len(inst.app.Pool.AvailableUIDs())

		auths, err := auth.LoadDir(inst.app.Cfg.AuthDir)
		if err == nil {
			for _, a := range auths {
				st.Accounts = append(st.Accounts, AccountView{
					UID:       a.UID,
					Nickname:  a.Nickname,
					Realm:     a.Realm(),
					Domain:    a.Domain,
					ExpiresAt: a.ExpiresAt,
					File:      filepath.Base(a.FilePath),
				})
			}
		}
	}
	b, err := json.Marshal(st)
	if err != nil {
		return `{"running":false}`
	}
	return string(b)
}

// AuthDir 返回当前凭证目录（无实例时为空）。
func AuthDir() string {
	mu.Lock()
	defer mu.Unlock()
	if inst == nil {
		return ""
	}
	return inst.app.Cfg.AuthDir
}

// APIKey 返回当前生效的 API Key。
func APIKey() string {
	mu.Lock()
	defer mu.Unlock()
	if inst == nil {
		return ""
	}
	return inst.app.Cfg.APIKey
}

// writeDefaultConfig 首次启动生成配置：关掉依赖外部脚本的两个任务，
// 其余（签到 / 猫猫旅行 / 活跃上报 / token 保活）保持默认。
func writeDefaultConfig(path, listen, apiKey, authDir, stateFile string) error {
	if strings.TrimSpace(listen) == "" {
		listen = DefaultListen
	}
	if strings.TrimSpace(apiKey) == "" {
		apiKey = "sk-" + randHex(24)
	}
	cfg := map[string]any{
		"listen":     listen,
		"api_key":    apiKey,
		"auth_dir":   authDir,
		"state_file": stateFile,
		"schedule": map[string]any{
			"checkin_enabled":   true,
			"travel_enabled":    true,
			"activity_enabled":  true,
			"keepalive_enabled": true,
			// 依赖外部 python 脚本（task_runner.py / school_open_day_2026.py），iOS 上不可用
			"school_enabled": false,
			"cat_enabled":    false,
		},
		"admin":  map[string]any{"enabled": true},
		"global": map[string]any{"enabled": true},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
