package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
)

// Config holds application configuration
type Config struct {
	RedisAddr       string
	RedisPassword   string
	RedisDB         int
	RedisChannel    string
	NATSUrl         string
	InstanceID      string
	DedupeWindowSec int
}

func loadConfig() *Config {
	return &Config{
		RedisAddr:       getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:   getEnv("REDIS_PASSWORD", ""),
		RedisDB:         getEnvInt("REDIS_DB", 0),
		RedisChannel:    getEnv("REDIS_CHANNEL", "llm:tokens:*"),
		NATSUrl:         getEnv("NATS_URL", "nats://localhost:4222"),
		InstanceID:      getEnv("INSTANCE_ID", fmt.Sprintf("bridge-%d", time.Now().UnixNano())),
		DedupeWindowSec: getEnvInt("DEDUPE_WINDOW_SEC", 30),
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		var i int
		fmt.Sscanf(val, "%d", &i)
		return i
	}
	return defaultVal
}

// TokenMessage represents a token from the LLM
type TokenMessage struct {
	ConversationID string `json:"conversation_id"`
	Token          string `json:"token"`
	Sequence       int64  `json:"sequence"`
	Done           bool   `json:"done"`
	Timestamp      int64  `json:"timestamp"`
}

func main() {
	cfg := loadConfig()

	// Setup logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	slog.Info("Starting Redis-NATS Bridge",
		"redis_addr", cfg.RedisAddr,
		"redis_channel", cfg.RedisChannel,
		"nats_url", cfg.NATSUrl,
		"instance_id", cfg.InstanceID,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect to Redis
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Error("Failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	slog.Info("Connected to Redis")

	// Connect to NATS with JetStream
	nc, err := nats.Connect(cfg.NATSUrl,
		nats.Name(cfg.InstanceID),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		slog.Error("Failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()
	slog.Info("Connected to NATS", "server", nc.ConnectedUrl())

	// Note: We publish to core NATS (not JetStream) for fan-out to leaf nodes
	// JetStream publishes don't automatically propagate to leaf subscriptions
	// If deduplication is needed, implement at the application level or use
	// JetStream consumers on the SSE adapters

	// Subscribe to Redis pub/sub
	pubsub := rdb.PSubscribe(ctx, cfg.RedisChannel)
	defer pubsub.Close()

	// Wait for subscription confirmation
	_, err = pubsub.Receive(ctx)
	if err != nil {
		slog.Error("Failed to subscribe to Redis channel", "error", err)
		os.Exit(1)
	}
	slog.Info("Subscribed to Redis channel", "pattern", cfg.RedisChannel)

	// Channel for Redis messages
	msgCh := pubsub.Channel()

	// Handle shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Process messages
	slog.Info("Starting message processing loop")
	var msgCount int64
	for {
		select {
		case <-quit:
			slog.Info("Shutting down", "messages_processed", msgCount)
			return

		case msg := <-msgCh:
			if msg == nil {
				slog.Warn("Received nil message")
				continue
			}

			slog.Info("Received Redis message",
				"channel", msg.Channel,
				"payload_len", len(msg.Payload),
			)

			var token TokenMessage
			if err := json.Unmarshal([]byte(msg.Payload), &token); err != nil {
				slog.Error("Failed to unmarshal message", "error", err, "payload", msg.Payload)
				continue
			}

			// Publish to NATS core for fan-out to leaf nodes
			subject := fmt.Sprintf("chat.%s.tokens", token.ConversationID)
			data, _ := json.Marshal(token)

			// Publish to core NATS for fan-out to leaf nodes
			err := nc.Publish(subject, data)
			if err != nil {
				slog.Error("Failed to publish to NATS",
					"error", err,
					"subject", subject,
					"sequence", token.Sequence,
				)
				continue
			}

			msgCount++
			slog.Info("Message bridged to NATS",
				"conversation_id", token.ConversationID,
				"sequence", token.Sequence,
				"subject", subject,
			)
		}
	}
}
