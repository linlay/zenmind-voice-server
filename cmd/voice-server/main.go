package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
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
	cfg, err := config.Load(".")
	if err != nil {
		log.Fatalf("load config: %v", err)
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
		log.Printf("voice server listening on %s", server.Addr)
		if serveErr := server.ListenAndServe(); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
		}
	}()

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case serveErr := <-errCh:
		log.Fatalf("server failed: %v", serveErr)
	case <-stopCh:
		log.Printf("voice server received stop signal, draining")
	}

	// 先给 WS 客户端 3 秒 drain 时间（发 connection.draining，让客户端主动断开重连）
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := wsHandler.Shutdown(drainCtx); err != nil && err != context.DeadlineExceeded {
		log.Printf("ws drain: %v", err)
	}
	drainCancel()

	// 再关 HTTP listener，等剩下的请求收尾
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("server shutdown: %v", err)
	}
}
