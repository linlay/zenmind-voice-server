package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"zenmind-voice-server/internal/asr"
	"zenmind-voice-server/internal/config"
	"zenmind-voice-server/internal/health"
	"zenmind-voice-server/internal/httpapi"
	"zenmind-voice-server/internal/runner"
	"zenmind-voice-server/internal/tts"
	"zenmind-voice-server/internal/ws"
)

func main() {
	setupLogging()

	cfg, err := config.Load(".")
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}

	asrProbe := health.New()
	ttsProbe := health.New()

	voiceCatalog := tts.NewVoiceCatalog(cfg)
	ttsClient := tts.NewDashScopeRealtimeClientWithProbe(cfg, ttsProbe)
	ttsService := tts.NewSynthesisService(cfg, voiceCatalog, ttsClient)
	asrGateway := asr.NewDashScopeRealtimeGatewayWithProbe(cfg, asrProbe)
	runnerClient := runner.NewHTTPClient(cfg)

	wsHandler := ws.NewHandler(cfg, asrGateway, ttsService, runnerClient)
	apiHandler := httpapi.NewWithProbes(cfg, voiceCatalog, asrProbe, ttsProbe, httpapi.DrainGateFunc(wsHandler.IsDraining))

	mux := http.NewServeMux()
	apiHandler.Register(mux)
	mux.Handle("/api/voice/ws", wsHandler)

	server := &http.Server{
		Addr:              cfg.ListenAddr(),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("voice server listening", "addr", server.Addr)
		if serveErr := server.ListenAndServe(); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
		}
	}()

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case serveErr := <-errCh:
		slog.Error("server failed", "err", serveErr)
		os.Exit(1)
	case <-stopCh:
		slog.Info("voice server received stop signal, draining")
	}

	// 先给 WS 客户端 3 秒 drain 时间（发 connection.draining，让客户端主动断开重连）
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := wsHandler.Shutdown(drainCtx); err != nil && err != context.DeadlineExceeded {
		slog.Warn("ws drain failed", "err", err)
	}
	drainCancel()

	// 再关 HTTP listener，等剩下的请求收尾
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		slog.Warn("server shutdown failed", "err", err)
	}
}

func setupLogging() {
	level := slog.LevelInfo
	if strings.EqualFold(strings.TrimSpace(os.Getenv("APP_VOICE_LOG_LEVEL")), "debug") {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(os.Getenv("APP_VOICE_LOG_FORMAT"))) {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	default:
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}
