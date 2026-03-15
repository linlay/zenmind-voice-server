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

	voiceCatalog := tts.NewVoiceCatalog(cfg)
	ttsClient := tts.NewDashScopeRealtimeClient(cfg)
	ttsService := tts.NewSynthesisService(cfg, voiceCatalog, ttsClient)
	asrGateway := asr.NewDashScopeRealtimeGateway(cfg)
	runnerClient := runner.NewHTTPClient(cfg)

	wsHandler := ws.NewHandler(cfg, asrGateway, ttsService, runnerClient)
	apiHandler := httpapi.New(cfg, voiceCatalog)

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
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("server shutdown: %v", err)
	}
}
