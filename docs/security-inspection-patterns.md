# Security Inspection Patterns

This document describes the security inspection options available in the distributed SSE architecture. The customer can choose the pattern that best balances their security requirements with performance constraints.

## Overview

LLM outputs may contain content that needs inspection before delivery to users:
- PII (accidentally included in training data or user prompts)
- Prompt injection attempts in responses
- Toxic or harmful content
- Confidential information leakage

The challenge with streaming responses is that inspection traditionally requires buffering the entire response, which defeats the purpose of streaming. This architecture provides three patterns with different trade-offs.

## Pattern 1: Inline (Blocking)

```
NATS Leaf ──► Security Inspector ──► SSE Adapter ──► User
                    │
                    └── If flagged: drop/redact token
```

### How It Works

1. SSE Adapter receives token from NATS
2. Before sending to user, passes token to inline inspector
3. Inspector evaluates token (possibly with context window)
4. If approved: send to user
5. If flagged: redact or drop

### Trade-offs

| Aspect | Impact |
|--------|--------|
| Latency | +10-50ms per inspection call |
| Security | Highest - nothing reaches user uninspected |
| Complexity | Moderate - inspector in request path |
| Failure Mode | Inspector down = streaming stops |

### When to Use

- Highly regulated environments (healthcare, finance)
- When any data leakage is unacceptable
- When latency is less critical than security

### Implementation Notes

```go
// In SSE Adapter
func (s *SSEHandler) handleToken(token Token) error {
    // Inline inspection
    result, err := s.inspector.Inspect(token)
    if err != nil {
        return err // Don't send on inspection failure
    }
    
    if result.Action == ActionDrop {
        log.Warn("Token dropped by inspector", "reason", result.Reason)
        return nil // Don't send, but continue stream
    }
    
    if result.Action == ActionRedact {
        token.Content = result.RedactedContent
    }
    
    return s.sendSSE(token)
}
```

## Pattern 2: Async Tap (Non-Blocking)

```
                    ┌──► Security Inspector (parallel)
                    │         │
NATS Leaf ──────────┼─────────▼───────────────────────────
                    │    (alerts if flagged)
                    │
                    └──► SSE Adapter ──► User (immediate)
```

### How It Works

1. NATS message arrives at leaf node
2. SSE Adapter forwards to user immediately
3. Inspector subscribes to same subject, receives copy
4. Inspector evaluates asynchronously
5. If flagged: trigger alert, optionally kill connection

### Trade-offs

| Aspect | Impact |
|--------|--------|
| Latency | ~0ms added to delivery path |
| Security | Lower - content may reach user before inspection completes |
| Complexity | Lower - inspector is independent service |
| Failure Mode | Inspector down = no inspection, but delivery continues |

### When to Use

- Performance-critical applications
- When inspection is primarily for audit/logging
- When post-hoc remediation is acceptable
- When you need to detect patterns across full response

### Implementation Notes

```go
// Security Inspector (separate service)
func (i *AsyncInspector) Start() {
    sub, _ := i.nc.Subscribe("chat.*.tokens", func(msg *nats.Msg) {
        result := i.inspect(msg.Data)
        
        if result.Flagged {
            // Alert - could trigger connection kill via control channel
            i.alertService.Send(Alert{
                Subject:    msg.Subject,
                Reason:     result.Reason,
                Timestamp:  time.Now(),
            })
            
            // Optionally: publish kill signal
            i.nc.Publish("chat.control.kill", []byte(conversationID))
        }
    })
}
```

## Pattern 3: Hybrid (Buffered)

```
NATS Leaf ──► Buffer (100-200ms) ──► Security Inspector ──► SSE Adapter ──► User
                                            │
                                            └── Inspector runs during buffer window
```

### How It Works

1. Tokens arrive from NATS into a short buffer
2. Buffer holds tokens for configurable window (100-200ms)
3. Inspector processes buffered tokens
4. After buffer window, release inspected tokens to user
5. Subsequent tokens continue flowing with same buffer delay

### Trade-offs

| Aspect | Impact |
|--------|--------|
| Latency | +100-200ms initial delay, then streaming |
| Security | Moderate - inspection catches up within buffer window |
| Complexity | Higher - buffer management, timing |
| Failure Mode | Inspector slow = buffer grows, potential backpressure |

### When to Use

- Balance between security and performance
- When small initial delay is acceptable
- When inspection can keep pace with token rate

### Implementation Notes

```go
type BufferedInspector struct {
    bufferDuration time.Duration
    tokenBuffer    chan Token
    inspected      chan Token
}

func (b *BufferedInspector) Start() {
    go func() {
        for token := range b.tokenBuffer {
            // Add artificial delay
            time.Sleep(b.bufferDuration)
            
            // Inspect (should complete within buffer window)
            result := b.inspect(token)
            
            if result.Action == ActionAllow {
                b.inspected <- token
            } else if result.Action == ActionRedact {
                token.Content = result.RedactedContent
                b.inspected <- token
            }
            // ActionDrop: don't forward
        }
    }()
}
```

## Comparison Summary

| Pattern | Latency Impact | Security Level | Implementation Complexity | Inspector Failure Behavior |
|---------|----------------|----------------|---------------------------|---------------------------|
| Inline | +10-50ms/token | Highest | Moderate | Blocks delivery |
| Async Tap | ~0ms | Lowest | Lowest | Delivery continues |
| Hybrid | +100-200ms initial | Moderate | Highest | Backpressure/delay |

## Inspection Logic Examples

### PII Detection

```rust
// Spin function example
fn inspect_pii(token: &str, context: &[String]) -> InspectionResult {
    // Simple regex patterns (production would use ML models)
    let patterns = vec![
        (r"\b\d{3}-\d{2}-\d{4}\b", "SSN"),
        (r"\b\d{16}\b", "Credit Card"),
        (r"\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b", "Email"),
    ];
    
    for (pattern, pii_type) in patterns {
        if Regex::new(pattern).unwrap().is_match(token) {
            return InspectionResult::Flag {
                reason: format!("Potential {} detected", pii_type),
                action: Action::Redact,
            };
        }
    }
    
    InspectionResult::Allow
}
```

### Prompt Injection Detection

```rust
fn inspect_injection(token: &str, context: &[String]) -> InspectionResult {
    // Look for patterns that suggest the model is following injected instructions
    let suspicious_patterns = vec![
        "ignore previous instructions",
        "disregard the above",
        "new instructions:",
        "system prompt:",
    ];
    
    let full_context = context.join(" ") + " " + token;
    
    for pattern in suspicious_patterns {
        if full_context.to_lowercase().contains(pattern) {
            return InspectionResult::Flag {
                reason: "Potential prompt injection in output",
                action: Action::Alert, // Don't block, but alert
            };
        }
    }
    
    InspectionResult::Allow
}
```

## NATS Subject Design for Inspection

```
# Token delivery (main flow)
chat.{conversation_id}.tokens

# Control channel (for killing connections)
chat.{conversation_id}.control

# Inspection results (for audit)
inspection.{conversation_id}.results
```

Inspector subscribes to `chat.*.tokens` with queue group to distribute load:

```go
// Multiple inspector instances share the load
nc.QueueSubscribe("chat.*.tokens", "inspectors", handleInspection)
```

## Configuring Inspection Mode

The SSE Adapter accepts configuration to select inspection mode:

```yaml
# ConfigMap
apiVersion: v1
kind: ConfigMap
metadata:
  name: sse-adapter-config
data:
  INSPECTION_MODE: "inline"  # Options: inline, async, hybrid, disabled
  INSPECTION_BUFFER_MS: "150"  # Only for hybrid mode
  INSPECTION_ENDPOINT: "http://inspector:8080/inspect"  # Only for inline mode
```

## Demo Commands

To demonstrate the difference between patterns:

```bash
# Start with inline inspection
kubectl set env deployment/sse-adapter INSPECTION_MODE=inline
# Observe: higher latency, all content inspected

# Switch to async
kubectl set env deployment/sse-adapter INSPECTION_MODE=async
# Observe: lower latency, inspection alerts appear in logs after delivery

# Switch to hybrid
kubectl set env deployment/sse-adapter INSPECTION_MODE=hybrid INSPECTION_BUFFER_MS=200
# Observe: initial 200ms delay, then streaming with inspection
```
