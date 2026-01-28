package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// ChatRequest is the request format for the /chat endpoint
type ChatRequest struct {
	Message string `json:"message"`
}

// ChatResponse is the response format
type ChatResponse struct {
	ConversationID string `json:"conversation_id"`
	Status         string `json:"status"`
}

// OpenAI-compatible types
type ChatCompletionRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type StreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// TokenMessage matches the format expected by the Redis-NATS bridge
type TokenMessage struct {
	ConversationID string `json:"conversation_id"`
	Token          string `json:"token"`
	Sequence       int64  `json:"sequence"`
	Done           bool   `json:"done"`
	Timestamp      int64  `json:"timestamp"`
}

var (
	rdb         *redis.Client
	vllmURL     string
	modelName   string
	activeChats atomic.Int64
)

func main() {
	// Configuration
	redisAddr := getEnv("REDIS_ADDR", "redis.redis-system.svc.cluster.local:6379")
	vllmURL = getEnv("VLLM_URL", "http://llm-inference.llm-system.svc.cluster.local:8000")
	modelName = getEnv("MODEL_NAME", "mistralai/Mistral-7B-Instruct-v0.3")
	port := getEnv("PORT", "8080")

	// Connect to Redis
	rdb = redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	log.Printf("Connected to Redis at %s", redisAddr)

	// HTTP endpoints
	http.HandleFunc("/chat", handleChat)
	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/metrics", handleMetrics)

	log.Printf("Starting LLM Stream Proxy on port %s", port)
	log.Printf("vLLM endpoint: %s", vllmURL)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "active_chats %d\n", activeChats.Load())
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Message == "" {
		http.Error(w, "Message is required", http.StatusBadRequest)
		return
	}

	// Generate conversation ID
	conversationID := uuid.New().String()

	// Start streaming in background
	go streamFromLLM(conversationID, req.Message)

	// Return conversation ID immediately
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ChatResponse{
		ConversationID: conversationID,
		Status:         "streaming",
	})
}

func streamFromLLM(conversationID, message string) {
	activeChats.Add(1)
	defer activeChats.Add(-1)

	ctx := context.Background()

	// Prepare vLLM request
	chatReq := ChatCompletionRequest{
		Model: modelName,
		Messages: []Message{
			{Role: "user", Content: message},
		},
		Stream: true,
	}

	reqBody, _ := json.Marshal(chatReq)

	req, err := http.NewRequest("POST", vllmURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		log.Printf("[%s] Failed to create request: %v", conversationID, err)
		publishToken(ctx, conversationID, "[ERROR]", 1, true)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[%s] Failed to call vLLM: %v", conversationID, err)
		publishToken(ctx, conversationID, "[ERROR]", 1, true)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[%s] vLLM error: %s - %s", conversationID, resp.Status, string(body))
		publishToken(ctx, conversationID, "[ERROR]", 1, true)
		return
	}

	// Parse SSE stream
	reader := bufio.NewReader(resp.Body)
	var sequence int64 = 0

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				log.Printf("[%s] Read error: %v", conversationID, err)
			}
			break
		}

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			publishToken(ctx, conversationID, "[DONE]", sequence+1, true)
			break
		}

		var chunk StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				sequence++
				publishToken(ctx, conversationID, choice.Delta.Content, sequence, false)
			}
			if choice.FinishReason != nil {
				publishToken(ctx, conversationID, "[DONE]", sequence+1, true)
				return
			}
		}
	}
}

func publishToken(ctx context.Context, conversationID, token string, sequence int64, done bool) {
	msg := TokenMessage{
		ConversationID: conversationID,
		Token:          token,
		Sequence:       sequence,
		Done:           done,
		Timestamp:      time.Now().UnixNano(),
	}

	data, _ := json.Marshal(msg)
	channel := "chat." + conversationID + ".tokens"

	if err := rdb.Publish(ctx, channel, data).Err(); err != nil {
		log.Printf("[%s] Failed to publish token: %v", conversationID, err)
	}
}
