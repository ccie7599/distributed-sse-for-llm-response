package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics
var (
	activeConnections = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "sse_active_connections",
		Help: "Number of active SSE connections",
	})

	totalConnections = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sse_total_connections",
		Help: "Total number of SSE connections",
	})

	messagesDelivered = promauto.NewCounter(prometheus.CounterOpts{
		Name: "sse_messages_delivered_total",
		Help: "Total number of messages delivered via SSE",
	})

	connectionDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "sse_connection_duration_seconds",
		Help:    "Duration of SSE connections",
		Buckets: []float64{1, 5, 10, 30, 60, 120, 300, 600},
	})
)

// TokenMessage represents a token from the LLM
type TokenMessage struct {
	ConversationID string `json:"conversation_id"`
	Token          string `json:"token"`
	Sequence       int64  `json:"sequence"`
	Done           bool   `json:"done"`
	Timestamp      int64  `json:"timestamp"`
}

// SSEHandler handles SSE connections
type SSEHandler struct {
	nc     *nats.Conn
	config *Config
	
	// Track active subscriptions for cleanup
	subscriptions sync.Map
	connCount     atomic.Int64
}

// NewSSEHandler creates a new SSE handler
func NewSSEHandler(nc *nats.Conn, cfg *Config) *SSEHandler {
	return &SSEHandler{
		nc:     nc,
		config: cfg,
	}
}

// HandleStream handles SSE stream requests
// URL format: /stream/{conversation_id}
func (h *SSEHandler) HandleStream(w http.ResponseWriter, r *http.Request) {
	// Extract conversation ID from URL
	path := strings.TrimPrefix(r.URL.Path, "/stream/")
	conversationID := strings.TrimSuffix(path, "/")
	
	if conversationID == "" {
		http.Error(w, "conversation_id required", http.StatusBadRequest)
		return
	}

	// Check for Last-Event-ID for reconnection handling
	lastEventID := r.Header.Get("Last-Event-ID")
	var startSequence int64
	if lastEventID != "" {
		fmt.Sscanf(lastEventID, "%d", &startSequence)
		startSequence++ // Start from next message
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	// Track connection
	connID := h.connCount.Add(1)
	startTime := time.Now()
	activeConnections.Inc()
	totalConnections.Inc()
	
	defer func() {
		activeConnections.Dec()
		connectionDuration.Observe(time.Since(startTime).Seconds())
	}()

	slog.Info("SSE connection started",
		"conversation_id", conversationID,
		"conn_id", connID,
		"last_event_id", lastEventID,
	)

	// Create channel for messages
	msgChan := make(chan *TokenMessage, 100)
	doneChan := make(chan struct{})

	// Subscribe to NATS subject for this conversation
	subject := fmt.Sprintf("chat.%s.tokens", conversationID)
	
	sub, err := h.nc.Subscribe(subject, func(msg *nats.Msg) {
		var token TokenMessage
		if err := json.Unmarshal(msg.Data, &token); err != nil {
			slog.Error("Failed to unmarshal token", "error", err)
			return
		}

		// Skip messages before our start sequence (for reconnection)
		if startSequence > 0 && token.Sequence <= startSequence {
			return
		}

		select {
		case msgChan <- &token:
		case <-doneChan:
			return
		default:
			slog.Warn("Message channel full, dropping message",
				"conversation_id", conversationID,
				"sequence", token.Sequence,
			)
		}
	})

	if err != nil {
		slog.Error("Failed to subscribe to NATS", "error", err, "subject", subject)
		http.Error(w, "Failed to subscribe", http.StatusInternalServerError)
		return
	}
	
	h.subscriptions.Store(connID, sub)
	defer func() {
		close(doneChan)
		sub.Unsubscribe()
		h.subscriptions.Delete(connID)
		slog.Info("SSE connection closed",
			"conversation_id", conversationID,
			"conn_id", connID,
			"duration", time.Since(startTime),
		)
	}()

	// Send initial comment to establish connection
	fmt.Fprintf(w, ": connected to %s\n\n", conversationID)
	flusher.Flush()

	// Keep-alive ticker
	keepAliveTicker := time.NewTicker(15 * time.Second)
	defer keepAliveTicker.Stop()

	// Main event loop
	for {
		select {
		case <-r.Context().Done():
			// Client disconnected
			return

		case token := <-msgChan:
			// Security inspection is disabled for now.
			// See README.md and docs/security-inspection-patterns.md for available modes:
			//   - inline: Block delivery until inspector approves (adds latency)
			//   - async: Deliver immediately, inspect in parallel, alert if flagged
			//   - hybrid: Buffer for 100-200ms, inspect during buffer window
			//
			// To implement, uncomment and add inspection logic:
			// if h.config.InspectionMode == "inline" {
			//     result, err := callInspector(h.config.InspectionEndpoint, token)
			//     if err != nil || result.Action == ActionDrop {
			//         continue // Skip this token
			//     }
			//     if result.Action == ActionRedact {
			//         token.Token = result.RedactedContent
			//     }
			// }

			// Format and send SSE event
			data, _ := json.Marshal(token)
			fmt.Fprintf(w, "event: token\n")
			fmt.Fprintf(w, "id: %d\n", token.Sequence)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			
			messagesDelivered.Inc()

			// Close connection if this is the last token
			if token.Done {
				slog.Info("Stream completed",
					"conversation_id", conversationID,
					"final_sequence", token.Sequence,
				)
				return
			}

		case <-keepAliveTicker.C:
			// Send keep-alive comment
			fmt.Fprintf(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}
