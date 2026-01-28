package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
)

// TokenMessage matches the format used by the bridge and SSE adapter
type TokenMessage struct {
	ConversationID string `json:"conversation_id"`
	Token          string `json:"token"`
	Sequence       int64  `json:"sequence"`
	Done           bool   `json:"done"`
	Timestamp      int64  `json:"timestamp"`
}

// Stats tracks metrics for the load test
type Stats struct {
	TokensPublished   atomic.Int64
	TokensReceived    atomic.Int64
	ConnectionsOpened atomic.Int64
	ConnectionsClosed atomic.Int64
	Errors            atomic.Int64
	TotalLatencyNs    atomic.Int64
	MinLatencyNs      atomic.Int64
	MaxLatencyNs      atomic.Int64
}

func (s *Stats) RecordLatency(latencyNs int64) {
	s.TotalLatencyNs.Add(latencyNs)

	// Update min (compare-and-swap loop)
	for {
		current := s.MinLatencyNs.Load()
		if current != 0 && current <= latencyNs {
			break
		}
		if s.MinLatencyNs.CompareAndSwap(current, latencyNs) {
			break
		}
	}

	// Update max
	for {
		current := s.MaxLatencyNs.Load()
		if current >= latencyNs {
			break
		}
		if s.MaxLatencyNs.CompareAndSwap(current, latencyNs) {
			break
		}
	}
}

func (s *Stats) Print() {
	received := s.TokensReceived.Load()
	avgLatency := float64(0)
	if received > 0 {
		avgLatency = float64(s.TotalLatencyNs.Load()) / float64(received) / 1e6
	}

	fmt.Printf("\n=== Load Test Statistics ===\n")
	fmt.Printf("Tokens Published:    %d\n", s.TokensPublished.Load())
	fmt.Printf("Tokens Received:     %d\n", received)
	fmt.Printf("Connections Opened:  %d\n", s.ConnectionsOpened.Load())
	fmt.Printf("Connections Closed:  %d\n", s.ConnectionsClosed.Load())
	fmt.Printf("Errors:              %d\n", s.Errors.Load())
	if received > 0 {
		fmt.Printf("Avg Latency:         %.2f ms\n", avgLatency)
		fmt.Printf("Min Latency:         %.2f ms\n", float64(s.MinLatencyNs.Load())/1e6)
		fmt.Printf("Max Latency:         %.2f ms\n", float64(s.MaxLatencyNs.Load())/1e6)
	}
	fmt.Printf("============================\n")
}

// Sample text to use for token generation
var sampleText = strings.Split(`The quick brown fox jumps over the lazy dog.
This is a sample response from a large language model that simulates streaming tokens.
Each word is sent as a separate token with timestamps for latency measurement.
NATS provides excellent fan-out capabilities for distributing these tokens to edge locations.
The SSE adapter converts NATS messages into Server-Sent Events for browser clients.`, " ")

func main() {
	// Command line flags
	mode := flag.String("mode", "both", "Mode: producer, consumer, or both")
	redisAddr := flag.String("redis", "localhost:6379", "Redis address")
	sseURL := flag.String("sse", "http://localhost:8080", "SSE adapter base URL")
	numConversations := flag.Int("conversations", 5, "Number of concurrent conversations")
	tokensPerConversation := flag.Int("tokens", 50, "Tokens per conversation")
	tokenDelayMs := flag.Int("token-delay", 50, "Delay between tokens in ms")
	duration := flag.Duration("duration", 30*time.Second, "Test duration (0 for single run)")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Shutting down...")
		cancel()
	}()

	stats := &Stats{}
	var wg sync.WaitGroup

	log.Printf("Starting load generator in %s mode", *mode)
	log.Printf("Conversations: %d, Tokens/conversation: %d, Token delay: %dms",
		*numConversations, *tokensPerConversation, *tokenDelayMs)

	// Start stats printer
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fmt.Printf("[%s] Published: %d, Received: %d, Errors: %d\n",
					time.Now().Format("15:04:05"),
					stats.TokensPublished.Load(),
					stats.TokensReceived.Load(),
					stats.Errors.Load())
			}
		}
	}()

	// Generate conversation IDs
	conversationIDs := make([]string, *numConversations)
	for i := 0; i < *numConversations; i++ {
		conversationIDs[i] = fmt.Sprintf("loadtest-%d-%d", time.Now().UnixNano(), i)
	}

	// Start consumers first (if enabled)
	if *mode == "consumer" || *mode == "both" {
		for _, convID := range conversationIDs {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				runConsumer(ctx, *sseURL, id, stats)
			}(convID)
		}
		// Give consumers time to connect
		time.Sleep(500 * time.Millisecond)
	}

	// Start producers (if enabled)
	if *mode == "producer" || *mode == "both" {
		rdb := redis.NewClient(&redis.Options{
			Addr: *redisAddr,
		})
		defer rdb.Close()

		if err := rdb.Ping(ctx).Err(); err != nil {
			log.Fatalf("Failed to connect to Redis: %v", err)
		}
		log.Printf("Connected to Redis at %s", *redisAddr)

		for _, convID := range conversationIDs {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				runProducer(ctx, rdb, id, *tokensPerConversation, *tokenDelayMs, stats)
			}(convID)
		}
	}

	// Run for duration or until complete
	if *duration > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(*duration):
			log.Println("Duration elapsed, stopping...")
			cancel()
		}
	}

	// Wait for goroutines to finish
	wg.Wait()

	// Print final stats
	stats.Print()
}

// runProducer publishes tokens to Redis, simulating LLM output
func runProducer(ctx context.Context, rdb *redis.Client, conversationID string, numTokens, delayMs int, stats *Stats) {
	channel := fmt.Sprintf("llm:tokens:%s", conversationID)
	log.Printf("[Producer] Starting conversation %s", conversationID)

	for i := 0; i < numTokens; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		token := TokenMessage{
			ConversationID: conversationID,
			Token:          sampleText[i%len(sampleText)],
			Sequence:       int64(i + 1),
			Done:           i == numTokens-1,
			Timestamp:      time.Now().UnixNano(),
		}

		data, _ := json.Marshal(token)
		if err := rdb.Publish(ctx, channel, string(data)).Err(); err != nil {
			log.Printf("[Producer] Error publishing: %v", err)
			stats.Errors.Add(1)
			continue
		}

		stats.TokensPublished.Add(1)

		if delayMs > 0 {
			// Add some jitter to simulate realistic LLM token timing
			jitter := rand.Intn(delayMs / 2)
			time.Sleep(time.Duration(delayMs+jitter) * time.Millisecond)
		}
	}

	log.Printf("[Producer] Completed conversation %s (%d tokens)", conversationID, numTokens)
}

// runConsumer connects to SSE endpoint and receives tokens
func runConsumer(ctx context.Context, baseURL, conversationID string, stats *Stats) {
	url := fmt.Sprintf("%s/stream/%s", baseURL, conversationID)
	log.Printf("[Consumer] Connecting to %s", url)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		log.Printf("[Consumer] Error creating request: %v", err)
		stats.Errors.Add(1)
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	client := &http.Client{
		Timeout: 0, // No timeout for SSE
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[Consumer] Error connecting: %v", err)
		stats.Errors.Add(1)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[Consumer] Unexpected status: %d", resp.StatusCode)
		stats.Errors.Add(1)
		return
	}

	stats.ConnectionsOpened.Add(1)
	defer stats.ConnectionsClosed.Add(1)

	log.Printf("[Consumer] Connected to %s", conversationID)

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Text()

		// Parse SSE data lines
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")

			var token TokenMessage
			if err := json.Unmarshal([]byte(data), &token); err != nil {
				continue
			}

			// Calculate latency
			if token.Timestamp > 0 {
				latency := time.Now().UnixNano() - token.Timestamp
				stats.RecordLatency(latency)
			}

			stats.TokensReceived.Add(1)

			if token.Done {
				log.Printf("[Consumer] Conversation %s completed", conversationID)
				return
			}
		}
	}

	if err := scanner.Err(); err != nil {
		if ctx.Err() == nil {
			log.Printf("[Consumer] Scanner error: %v", err)
			stats.Errors.Add(1)
		}
	}
}
