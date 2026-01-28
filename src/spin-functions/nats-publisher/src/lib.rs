// Fermyon Spin WASM function - NATS Publisher Example
// This is a bare-bones example showing how to connect to and publish to NATS
// from a Spin function.

use anyhow::Result;
use spin_sdk::http::{IntoResponse, Request, Response};
use spin_sdk::http_component;

// Note: As of writing, Spin doesn't have native NATS support.
// This example shows the pattern for when NATS support is added,
// or how you might use an HTTP-to-NATS bridge.

/// A simple HTTP handler that would publish to NATS
#[http_component]
fn handle_request(req: Request) -> Result<impl IntoResponse> {
    // In a real implementation, you would:
    // 1. Parse the incoming request
    // 2. Connect to NATS (when SDK support is available)
    // 3. Publish the message
    
    // For now, this demonstrates the structure
    let body = req.body();
    let message = String::from_utf8_lossy(body);
    
    println!("Would publish to NATS: {}", message);
    
    // Example of what NATS publishing would look like:
    // 
    // let nc = nats::connect("nats://localhost:4222")?;
    // nc.publish("chat.{conversation_id}.tokens", message.as_bytes())?;
    
    Ok(Response::builder()
        .status(200)
        .header("content-type", "application/json")
        .body(r#"{"status": "published"}"#)
        .build())
}

// Alternative: HTTP-to-NATS bridge pattern
// If running alongside a NATS HTTP gateway, you could make HTTP calls
// to publish messages. This works today with Spin's outbound HTTP support.

/*
use spin_sdk::outbound_http;

async fn publish_via_http_bridge(subject: &str, data: &[u8]) -> Result<()> {
    let url = format!("http://nats-http-bridge/publish/{}", subject);
    
    let response = outbound_http::send_request(
        http::Request::builder()
            .method("POST")
            .uri(&url)
            .body(Some(data.into()))?
    ).await?;
    
    if response.status() != 200 {
        anyhow::bail!("Failed to publish: {}", response.status());
    }
    
    Ok(())
}
*/
