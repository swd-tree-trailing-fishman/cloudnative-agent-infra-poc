package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cloudnative-poc/agent-infra/internal/feature"
	"github.com/cloudnative-poc/agent-infra/internal/handler"
	"github.com/cloudnative-poc/agent-infra/internal/observability"
	"github.com/cloudnative-poc/agent-infra/internal/sandbox"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// --- Observability: init OTLP tracer ---
	shutdownTracer, err := observability.InitTracer(ctx)
	if err != nil {
		log.Printf("WARN: OTLP tracer init failed (continuing without tracing): %v", err)
		// Non-fatal: run without tracing in local dev
	} else {
		defer func() {
			if err := shutdownTracer(context.Background()); err != nil {
				log.Printf("tracer shutdown error: %v", err)
			}
		}()
	}

	// --- Feature Toggle ---
	toggle := feature.New()

	// --- Sandbox Runner (optional: skip if K8s unavailable) ---
	sandboxRunner, err := sandbox.New()
	if err != nil {
		log.Printf("WARN: sandbox runner init failed (mock mode): %v", err)
		sandboxRunner = nil
	}

	// --- HTTP Server ---
	mux := http.NewServeMux()
	api := handler.New(toggle, sandboxRunner)
	api.RegisterRoutes(mux)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("Server listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}
}
