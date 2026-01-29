# Chat UI Demo

A dark-mode chat interface for testing the Distributed SSE system with real-time latency metrics.

## Features

- Dark mode UI
- Configurable endpoint URL (with localStorage persistence)
- Real-time streaming display with typing cursor
- Latency metrics:
  - **TTFT (Time to First Token)**: Time from request to first token received
  - **Avg Token Latency**: Average time from server timestamp to client receive (subject to clock drift)
  - **Token Count**: Total tokens in response
  - **Total Time**: End-to-end request duration

## Usage

1. Open `index.html` in a browser
2. Click "Configure Endpoint" and enter your edge SSE adapter URL
3. Type a message and press Enter or click Send

## Latency Measurement

The demo calculates latency using the server-side timestamp embedded in each token:

```json
{
  "token": "Hello",
  "timestamp": 1769637100189157336,  // nanoseconds since epoch
  "sequence": 1,
  "done": false
}
```

**Note**: This latency measurement is subject to clock drift between server and client. For accurate latency measurement, ensure NTP synchronization or use relative timing.

## Serving Locally

```bash
# Python 3
python -m http.server 8000

# Node.js
npx serve .
```

Then open http://localhost:8000

## TLS Configuration

For production use with TLS:
1. Deploy behind Akamai or another CDN/edge provider
2. Configure TLS termination at the edge
3. Update the endpoint URL to use `https://`

## Screenshot

```
┌─────────────────────────────────────────────────────────┐
│ ● Distributed SSE Chat              [Configure Endpoint]│
├─────────────────────────────────────────────────────────┤
│                                                         │
│                    [U] What is 2+2?                     │
│                                                         │
│  [AI] Four.                                             │
│       TTFT: 234ms | Avg: 45ms | Tokens: 2 | Total: 312ms│
│                                                         │
├─────────────────────────────────────────────────────────┤
│  [Type a message...                              ] [➤]  │
├─────────────────────────────────────────────────────────┤
│  First Token: 234ms | Avg Latency: 45ms | Tokens: 2    │
└─────────────────────────────────────────────────────────┘
```
