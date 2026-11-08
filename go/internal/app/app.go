// Package app workbuddy2api 服务端装配：加载配置、构建账号池、起调度器与 HTTP 服务。
//
// 同一份装配同时供两个场景使用：
//   - 命令行：cmd/server（薄壳，调用 Run）
//   - iOS App：wbios 包（调用 Start/Stop，进程内运行，无信号处理）
package app

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/redisstore"
	"workbuddy2api/internal/scheduler"
	"workbuddy2api/internal/server"
	"workbuddy2api/internal/session"
	"workbuddy2api/internal/upstream"
)

// modelJSONPath 由 state.json 路径推导 model.json 路径（同目录同名换缀）：
// 两者同为数据目录持久化物（Docker ./data volume），配套而非各自配置。
// state 路径为空（纯内存测试形态）→ 空 = 禁用 model.json 落盘（内存 + 种子仍可用）。
func modelJSONPath(stateFile string) string {
	if stateFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(stateFile), "model.json")
}

// App 是运行中的服务实例（Start 返回，Stop 释放）。
type App struct {
	Cfg      *Config
	Pool     *pool.Pool
	Addr     string
	RedisMod string

	srv       *http.Server
	cancel    context.CancelFunc
	store     redisstore.Store
	stopWatch func()
	stopGC    func()
	startedAt time.Time
}

// Run 命令行入口：装配 → 阻塞等待中断信号 → 优雅退出。
func Run() {
	cfgPath := flag.String("config", "config.json", "path to config json")
	flag.Parse()

	cfg, err := Load(*cfgPath)
	if err != nil {
		// 配置文件不存在时给一次机会用纯默认 + env
		if os.IsNotExist(err) {
			log.Printf("config %s not found, using defaults+env", *cfgPath)
			cfg, err = Load("")
		}
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
	}

	a, err := Start(cfg)
	if err != nil {
		log.Fatalf("%v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	if err := a.Stop(); err != nil {
		log.Printf("WARN: [server] stop: %v", err)
	}
	log.Printf("bye")
}

// Start 装配并启动服务（非阻塞，就绪后返回）。
func Start(cfg *Config) (*App, error) {
	auths, err := auth.LoadDir(cfg.AuthDir)
	if err != nil {
		return nil, fmt.Errorf("load auths: %w", err)
	}
	log.Printf("loaded %d account(s) from %s", len(auths), cfg.AuthDir)

	// global realm 路由开关（config global.enabled，缺省 true）：注入 auth 包全局闸。
	auth.SetGlobalEnabled(cfg.Global.Enabled)

	// model.json 本地缓存接线（context_length 四级查找链第 3 级）。
	upstream.SetModelCatalogPath(modelJSONPath(cfg.StateFile))

	// redisstore：未配置/连接失败 → Noop（纯内存模式，一切功能照常）。
	store := redisstore.New(cfg.Upstash.URL, cfg.Upstash.Token)

	p := pool.New(cfg.StateFile)
	p.SetStore(store)
	p.RestoreFromSnapshot()
	p.SyncToDir(auths)

	stopWatch := p.StartAuthDirWatch(cfg.AuthDir)

	p.SetBreaker(cfg.Pool.BreakerThreshold, cfg.BreakerCooldownDur, cfg.BreakerCooldownMaxD)
	p.SetDegrade(cfg.Pool.DegradeThreshold, cfg.DegradeCooldownDur, cfg.DegradeCooldownMaxD)
	p.SetMaxInFlight(cfg.Pool.MaxInFlight)
	p.SetMaxInFlightGlobal(cfg.Pool.MaxInFlightGlobal)
	p.SetSoftRateMax(cfg.SoftRateMaxDur)
	p.SetWeights(cfg.Pool.IdleWeightPerHour, cfg.Pool.IdleWeightMax)
	p.SetCostExploreInterval(cfg.CostExploreIntervalDur)

	// 会话粘性路由（可配关闭）。
	var sessRouter *session.Router
	redisMode := "noop"
	if _, ok := store.(redisstore.Noop); !ok {
		redisMode = "upstash"
	}
	var stopGC func()
	if cfg.SessionSticky.Enabled {
		sessRouter = session.New(session.Config{
			TTL:               cfg.SessionTTL,
			GCInterval:        cfg.SessionGCInterval,
			Store:             store,
			Available:         p.AvailableUIDs,
			AvailableForModel: realmAwareAvailableForModel(p),
		})
		sessRouter.LoadFromStore()
		sessRouter.StartGC()
		stopGC = sessRouter.StopGC
	}
	sessCount := func() int {
		if sessRouter != nil {
			return sessRouter.Count()
		}
		return 0
	}

	up := upstream.New()
	up.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	up.HeaderTimeout = time.Duration(cfg.Upstream.HeaderTimeoutSeconds) * time.Second
	if tr, ok := up.ChatHTTP.Transport.(*http.Transport); ok {
		tr.ResponseHeaderTimeout = up.HeaderTimeout
	}
	up.IdleTimeout = time.Duration(cfg.Upstream.IdleTimeoutSeconds) * time.Second
	up.SanitizeFingerprints = cfg.Features.SanitizeBlacklistFingerprints
	up.UserAgent = cfg.Upstream.UserAgent
	up.ClientVersion = cfg.Upstream.ClientVersion
	up.CliVersion = cfg.Upstream.CliVersion
	up.DeviceToken = cfg.Upstream.DeviceToken
	up.DeviceTokenFile = cfg.Upstream.DeviceTokenFile
	up.ClientName = cfg.Upstream.ClientName
	up.PassthroughIP = cfg.Upstream.PassthroughIP
	up.ChatBaseGlobal = cfg.Global.ChatBase
	up.BillingBaseGlobal = cfg.Global.BillingBase
	up.GlobalEnabled = cfg.Global.Enabled

	sch := scheduler.New(scheduler.Config{
		Pool:                p,
		Upstream:            up,
		CheckinHours:        cfg.Schedule.CheckinHours,
		TravelHours:         cfg.Schedule.TravelHours,
		ActivityHours:       cfg.Schedule.ActivityHours,
		KeepaliveHours:      cfg.Schedule.KeepaliveHours,
		SchoolHours:         cfg.Schedule.SchoolHours,
		CatHours:            cfg.Schedule.CatHours,
		ActivityReportCount: cfg.Schedule.ActivityReportCount,
		ExpiringSoonWindow:  cfg.ExpiringSoonDur,
		CheckinDisabled:     !cfg.Schedule.CheckinEnabled,
		TravelDisabled:      !cfg.Schedule.TravelEnabled,
		ActivityDisabled:    !cfg.Schedule.ActivityEnabled,
		KeepaliveDisabled:   !cfg.Schedule.KeepaliveEnabled,
		SchoolDisabled:      !cfg.Schedule.SchoolEnabled,
		CatDisabled:         !cfg.Schedule.CatEnabled,
	})

	h := server.NewHandler(server.Config{
		Pool:          p,
		Upstream:      up,
		APIKey:        cfg.APIKey,
		Session:       sessRouter,
		StickyCount:   sessCount,
		RedisMode:     redisMode,
		SoftCooldown:  cfg.SoftRateDur,
		PromptMode:    cfg.Prompt.Mode,
		PromptText:    cfg.PromptText,
		GlobalEnabled: cfg.Global.Enabled,
		AdminEnabled:  cfg.Admin.Enabled,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go sch.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           h,
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	a := &App{
		Cfg:       cfg,
		Pool:      p,
		Addr:      cfg.Listen,
		RedisMod:  redisMode,
		srv:       srv,
		cancel:    cancel,
		store:     store,
		stopWatch: stopWatch,
		stopGC:    stopGC,
		startedAt: time.Now(),
	}

	go func() {
		<-ctx.Done()
		log.Printf("[app] 调度器已停止")
	}()
	go func() {
		log.Printf("workbuddy2api listening on %s (api_key=%v)", cfg.Listen, cfg.APIKey != "")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("ERROR: [server] http: %v", err)
		}
	}()

	// 就绪探测：最多 10s
	for i := 0; i < 100; i++ {
		time.Sleep(100 * time.Millisecond)
		if portOpen(cfg.Listen) {
			log.Printf("[app] 服务就绪：%s", cfg.Listen)
			return a, nil
		}
	}
	_ = a.Stop()
	return nil, fmt.Errorf("服务启动超时（未监听 %s）", cfg.Listen)
}

// Stop 优雅停机：停止调度器 → 落盘 → 关服务 → 释放资源。
func (a *App) Stop() error {
	if a.cancel != nil {
		a.cancel()
	}
	if a.Pool != nil {
		a.Pool.Flush()
	}
	if a.stopWatch != nil {
		a.stopWatch()
	}
	if a.stopGC != nil {
		a.stopGC()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.srv.Shutdown(ctx)
	if a.store != nil {
		if cErr := a.store.Close(); cErr != nil {
			log.Printf("WARN: [server] redisstore close: %v", cErr)
		}
	}
	if a.Pool != nil {
		a.Pool.Close()
	}
	log.Printf("[app] 已停止")
	return err
}

// Uptime 已运行秒数。
func (a *App) Uptime() int { return int(time.Since(a.startedAt).Seconds()) }

// portOpen 探测本地监听是否就绪。
func portOpen(addr string) bool {
	c, err := net.DialTimeout("tcp", normalizeAddr(addr), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// normalizeAddr 把 ":7863" 形式的监听地址补成可 dial 的地址。
func normalizeAddr(addr string) string {
	if len(addr) > 0 && addr[0] == ':' {
		return "127.0.0.1" + addr
	}
	return addr
}
