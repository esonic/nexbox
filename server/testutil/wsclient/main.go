// wsclient is a standalone WebSocket client for e2e chat testing.
// It connects to a NexBox server's global WebSocket, subscribes to a
// session, sends a message, collects all streaming events, and
// outputs a JSON summary to stdout.
//
// Usage:
//
//	NEXBOX_AUTH_TOKEN=... wsclient -url ws://127.0.0.1:PORT/ws -session SESSION_ID -message "hello"
//
// Output (JSON):
//
//	{"events":[...], "full_text":"...", "event_counts":{...}, "got_done":true, "got_error":false, "error_msg":""}
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type Result struct {
	Events      []map[string]interface{} `json:"events"`
	FullText    string                   `json:"full_text"`
	EventCounts map[string]int           `json:"event_counts"`
	GotDone     bool                     `json:"got_done"`
	GotError    bool                     `json:"got_error"`
	ErrorMsg    string                   `json:"error_msg"`
	GotText     bool                     `json:"got_text"`
}

func main() {
	wsURL := flag.String("url", "", "WebSocket URL (ws://host:port/ws)")
	sessionID := flag.String("session", "", "Session ID to subscribe and send to")
	message := flag.String("message", "Hello", "Message to send")
	timeoutSec := flag.Int("timeout", 180, "Read timeout in seconds")
	flag.Parse()

	if *wsURL == "" || *sessionID == "" {
		fmt.Fprintln(os.Stderr, "usage: NEXBOX_AUTH_TOKEN=... wsclient -url ws://host:port/ws -session SESSION_ID -message ...")
		os.Exit(1)
	}
	authToken := strings.TrimSpace(os.Getenv("NEXBOX_AUTH_TOKEN"))
	if authToken == "" {
		fmt.Fprintln(os.Stderr, "NEXBOX_AUTH_TOKEN is required")
		os.Exit(1)
	}

	log.SetOutput(os.Stderr) // keep stdout clean for JSON result

	log.Printf("Connecting to %s", *wsURL)
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+authToken)
	conn, _, err := websocket.DefaultDialer.Dial(*wsURL, headers)
	if err != nil {
		log.Fatalf("WebSocket connect failed: %v", err)
	}
	defer conn.Close()
	log.Println("Connected!")

	// Subscribe to the session first
	sub, _ := json.Marshal(map[string]interface{}{
		"type":       "subscribe",
		"session_id": *sessionID,
	})
	if err := conn.WriteMessage(websocket.TextMessage, sub); err != nil {
		log.Fatalf("Subscribe failed: %v", err)
	}

	// Send user message
	msg, _ := json.Marshal(map[string]string{
		"type":       "user_message",
		"session_id": *sessionID,
		"content":    *message,
	})
	if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		log.Fatalf("Send failed: %v", err)
	}
	log.Printf("Sent: %s", *message)

	// Collect events
	result := Result{
		EventCounts: make(map[string]int),
	}
	var fullText strings.Builder

	_ = conn.SetReadDeadline(time.Now().Add(time.Duration(*timeoutSec) * time.Second))

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if !result.GotDone {
				log.Printf("Read error (no done received): %v", err)
				result.ErrorMsg = fmt.Sprintf("ws read: %v", err)
				result.GotError = true
			}
			break
		}

		var event map[string]interface{}
		if err := json.Unmarshal(raw, &event); err != nil {
			log.Printf("Non-JSON: %s", raw)
			continue
		}

		// Filter events for our session (global WS broadcasts all)
		evtSessionID, _ := event["session_id"].(string)
		if evtSessionID != *sessionID {
			continue
		}

		result.Events = append(result.Events, event)
		eventType, _ := event["type"].(string)
		result.EventCounts[eventType]++

		switch eventType {
		case "text_delta":
			content, _ := event["content"].(string)
			fullText.WriteString(content)
			result.GotText = true
			fmt.Fprint(os.Stderr, content)

		case "tool_start":
			name, _ := event["tool"].(string)
			id, _ := event["tool_id"].(string)
			log.Printf("[tool_start] %s (%s)", name, id)

		case "tool_result":
			id, _ := event["tool_id"].(string)
			isErr, _ := event["is_error"].(bool)
			log.Printf("[tool_result] %s is_error=%v", id, isErr)

		case "thinking_delta":
			// just count it

		case "done":
			result.GotDone = true
			sid, _ := event["session_id"].(string)
			log.Printf("\n[done] session_id=%s", sid)

		case "error":
			result.GotError = true
			result.ErrorMsg, _ = event["message"].(string)
			log.Printf("[error] %s", result.ErrorMsg)
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

		default:
			log.Printf("[%s]", eventType)
		}

		if result.GotDone {
			break
		}
	}

	result.FullText = fullText.String()

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(result)
}
