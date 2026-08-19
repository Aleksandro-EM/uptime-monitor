package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Discord embed colors (decimal, Discord's own brand red/green).
const (
	discordColorDown = 15548997 // #ED4245
	discordColorUp   = 5763719  // #57F287
)

// discordWebhookPayload is the JSON body POSTed to a Discord incoming
// webhook. Discord rejects any body that has neither "content" nor
// "embeds" set, so this shape is required, not just a style choice.
type discordWebhookPayload struct {
	Embeds []discordEmbed `json:"embeds"`
}

type discordEmbed struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Color       int    `json:"color"`
	Timestamp   string `json:"timestamp"` // ISO 8601, Discord renders this as a localized footer timestamp
}

// notifier sends Discord webhook notifications for target state
// transitions. A nil *notifier is valid and notify becomes a no-op, so
// callers don't need to branch on whether alerting is configured.
type notifier struct {
	url    string
	client *http.Client
}

func newNotifier(url string) *notifier {
	if url == "" {
		return nil
	}
	return &notifier{url: url, client: &http.Client{Timeout: 5 * time.Second}}
}

func (n *notifier) notify(target string, down bool, message string) error {
	if n == nil {
		return nil
	}

	title := fmt.Sprintf("%s has RECOVERED", target)
	color := discordColorUp
	if down {
		title = fmt.Sprintf("%s is DOWN", target)
		color = discordColorDown
	}

	body, err := json.Marshal(discordWebhookPayload{
		Embeds: []discordEmbed{{
			Title:       title,
			Description: message,
			Color:       color,
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		}},
	})
	if err != nil {
		return err
	}

	resp, err := n.client.Post(n.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}
