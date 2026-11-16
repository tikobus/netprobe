package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"netprobe/internal/config"
	"netprobe/internal/prober"
	"netprobe/internal/recorder"
	"netprobe/internal/scheduler"
	"netprobe/internal/store"
	"netprobe/internal/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	var configPath string
	flag.StringVar(&configPath, "config", "configs/config.yaml", "配置文件路径")
	showVersion := flag.Bool("version", false, "打印版本号")
	flag.Parse()

	if *showVersion {
		fmt.Println("netprobe", web.Version)
		return nil
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	logger := newLogger(cfg.Log.Level)
	slog.SetDefault(logger)
	logger.Info("netprobe starting", "version", web.Version, "addr", cfg.Server.Addr, "db", cfg.Storage.DBPath)

	st, err := store.Open(cfg.Storage.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// 探活引擎
	reg := prober.NewRegistry()
	rec := recorder.New(&cfg, st, logger)
	sched := scheduler.New(&cfg, st, reg, rec, logger)

	appCtx, stopApp := context.WithCancel(context.Background())
	defer stopApp()
	go rec.Start(appCtx)
	go sched.Start(appCtx)
	logger.Info("scheduler started", "workers", cfg.Prober.Concurrency)

	srv := web.New(&cfg, st, logger, sched)
	httpSrv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("web server listening", "addr", cfg.Server.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return fmt.Errorf("web server: %w", err)
	case sig := <-stop:
		logger.Info("shutting down", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		return err
	}
	stopApp() // 停止调度与清理，等待退出
	time.Sleep(200 * time.Millisecond)
	return nil
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lv}))
}
