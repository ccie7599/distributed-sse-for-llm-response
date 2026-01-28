package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Config holds application configuration
type Config struct {
	NATSUrl            string
	SSEPort            string
	MetricsPort        string
	InspectionMode     string
	InspectionBuffer   time.Duration
	InspectionEndpoint string
	LogLevel           string
	LLMProxyURL        string // Origin LLM proxy URL for forwarding chat requests
}

func loadConfig() *Config {
	return &Config{
		NATSUrl:            getEnv("NATS_URL", "nats://localhost:4222"),
		SSEPort:            getEnv("SSE_PORT", "8080"),
		MetricsPort:        getEnv("METRICS_PORT", "9090"),
		InspectionMode:     getEnv("INSPECTION_MODE", "disabled"),
		InspectionBuffer:   getDurationEnv("INSPECTION_BUFFER_MS", 150*time.Millisecond),
		InspectionEndpoint: getEnv("INSPECTION_ENDPOINT", ""),
		LogLevel:           getEnv("LOG_LEVEL", "info"),
		LLMProxyURL:        getEnv("LLM_PROXY_URL", ""), // e.g., http://172.238.181.87
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getDurationEnv(key string, defaultVal time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if d, err := time.ParseDuration(val + "ms"); err == nil {
			return d
		}
	}
	return defaultVal
}

func main() {
	cfg := loadConfig()

	// Setup structured logging
	var logLevel slog.Level
	switch cfg.LogLevel {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	slog.Info("Starting SSE Adapter",
		"nats_url", cfg.NATSUrl,
		"sse_port", cfg.SSEPort,
		"inspection_mode", cfg.InspectionMode,
		"llm_proxy_url", cfg.LLMProxyURL,
	)

	// Connect to NATS
	nc, err := connectNATS(cfg.NATSUrl)
	if err != nil {
		slog.Error("Failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	// Create SSE handler
	sseHandler := NewSSEHandler(nc, cfg)

	// Setup HTTP routes
	mux := http.NewServeMux()

	// SSE streaming endpoint (legacy - for direct NATS subscriptions)
	mux.HandleFunc("/stream/", sseHandler.HandleStream)

	// Chat endpoint - accepts POST, returns SSE stream (Pattern 1)
	// This is the preferred endpoint for clients
	mux.HandleFunc("/chat", sseHandler.HandleChat)

	// Health checks
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if nc.Status() != nats.CONNECTED {
			http.Error(w, "NATS disconnected", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if nc.Status() != nats.CONNECTED {
			http.Error(w, "NATS disconnected", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ready"))
	})

	// SSE server
	sseServer := &http.Server{
		Addr:         ":" + cfg.SSEPort,
		Handler:      mux,
		ReadTimeout:  0, // No read timeout for SSE
		WriteTimeout: 0, // No write timeout for SSE
		IdleTimeout:  120 * time.Second,
	}

	// Metrics server (separate port)
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsServer := &http.Server{
		Addr:    ":" + cfg.MetricsPort,
		Handler: metricsMux,
	}

	// Start servers
	go func() {
		slog.Info("Starting SSE server", "port", cfg.SSEPort)
		if err := sseServer.ListenAndServe(); err != http.ErrServerClosed {
			slog.Error("SSE server error", "error", err)
		}
	}()

	go func() {
		slog.Info("Starting metrics server", "port", cfg.MetricsPort)
		if err := metricsServer.ListenAndServe(); err != http.ErrServerClosed {
			slog.Error("Metrics server error", "error", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("Shutting down servers...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := sseServer.Shutdown(ctx); err != nil {
		slog.Error("SSE server shutdown error", "error", err)
	}

	if err := metricsServer.Shutdown(ctx); err != nil {
		slog.Error("Metrics server shutdown error", "error", err)
	}

	slog.Info("Servers stopped")
}

func connectNATS(url string) (*nats.Conn, error) {
	opts := []nats.Option{
		nats.Name("sse-adapter"),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1), // Unlimited reconnects
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			slog.Warn("NATS disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			slog.Info("NATS reconnected", "server", nc.ConnectedUrl())
		}),
		nats.ErrorHandler(func(nc *nats.Conn, sub *nats.Subscription, err error) {
			slog.Error("NATS error", "error", err)
		}),
	}

	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("connecting to NATS: %w", err)
	}

	slog.Info("Connected to NATS", "server", nc.ConnectedUrl())
	return nc, nil
}
