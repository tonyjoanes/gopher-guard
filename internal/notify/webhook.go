// Package notify sends healing update messages to Slack or Discord via
// incoming webhooks. The webhook URL is never hardcoded — it must be
// stored in a Kubernetes Secret and referenced via the operator config.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tonyjoanes/gopher-guard/internal/llm"
)

// HealingUpdate is the data passed to SendHealingUpdate.
type HealingUpdate struct {
	DeploymentName string
	Namespace      string
	Diagnosis      *llm.Diagnosis
	PRURL          string
	HealingScore   int32
	// SafeMode true means no PR was created — mention that in the message.
	SafeMode bool
}

// NotificationClient sends webhook messages to Slack or Discord.
// Auto-detected from the URL: discord.com → Discord format, otherwise Slack.
type NotificationClient struct {
	WebhookURL string
	http       *http.Client
}

// NewNotificationClient creates a client. An empty URL silently no-ops all sends.
func NewNotificationClient(webhookURL string) *NotificationClient {
	return &NotificationClient{
		WebhookURL: webhookURL,
		http:       &http.Client{Timeout: 10 * time.Second},
	}
}

// SendHealingUpdate posts a formatted message to the configured webhook.
// Returns nil (no-op) when WebhookURL is empty.
func (n *NotificationClient) SendHealingUpdate(ctx context.Context, u HealingUpdate) error {
	if n.WebhookURL == "" {
		return nil
	}

	var payload any
	if strings.Contains(n.WebhookURL, "discord.com") {
		payload = n.buildDiscordPayload(u)
	} else {
		payload = n.buildSlackPayload(u)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.http.Do(req)
	if err != nil {
		return fmt.Errorf("sending webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// --- Slack Block Kit payload ---

type slackPayload struct {
	Blocks []slackBlock `json:"blocks"`
}

type slackBlock struct {
	Type string     `json:"type"`
	Text *slackText `json:"text,omitempty"`
}

type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (n *NotificationClient) buildSlackPayload(u HealingUpdate) slackPayload {
	header := fmt.Sprintf("🐹 *GopherGuard healed `%s/%s`* (score: %d)",
		u.Namespace, u.DeploymentName, u.HealingScore)

	details := fmt.Sprintf("*Root cause:* %s\n*AI says:* _%s_",
		u.Diagnosis.RootCause, u.Diagnosis.WittyLine)

	action := ""
	if u.SafeMode {
		action = "🔒 Safe mode enabled — no PR created. Review the diagnosis above."
	} else if u.PRURL != "" {
		action = fmt.Sprintf("🔗 <%s|View healing PR>", u.PRURL)
	}

	blocks := []slackBlock{
		{Type: "header", Text: &slackText{Type: "plain_text", Text: "🐹 GopherGuard Healing Report"}},
		{Type: "section", Text: &slackText{Type: "mrkdwn", Text: header}},
		{Type: "section", Text: &slackText{Type: "mrkdwn", Text: details}},
	}
	if action != "" {
		blocks = append(blocks, slackBlock{
			Type: "section",
			Text: &slackText{Type: "mrkdwn", Text: action},
		})
	}
	blocks = append(blocks, slackBlock{Type: "divider"})
	return slackPayload{Blocks: blocks}
}

// --- Discord webhook payload ---

type discordPayload struct {
	Username string         `json:"username"`
	Embeds   []discordEmbed `json:"embeds"`
}

type discordEmbed struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Color       int            `json:"color"` // decimal RGB
	Fields      []discordField `json:"fields,omitempty"`
	URL         string         `json:"url,omitempty"`
	Footer      *discordFooter `json:"footer,omitempty"`
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type discordFooter struct {
	Text string `json:"text"`
}

func (n *NotificationClient) buildDiscordPayload(u HealingUpdate) discordPayload {
	color := 0x57F287 // green
	if u.SafeMode {
		color = 0xFEE75C // yellow for safe mode
	}

	title := fmt.Sprintf("🐹 GopherGuard healed %s/%s", u.Namespace, u.DeploymentName)
	prLine := "Safe mode — no PR created"
	if !u.SafeMode && u.PRURL != "" {
		prLine = fmt.Sprintf("[View healing PR](%s)", u.PRURL)
	}

	embed := discordEmbed{
		Title:       title,
		Description: fmt.Sprintf("**Root cause:** %s\n\n> 💬 *%s*", u.Diagnosis.RootCause, u.Diagnosis.WittyLine),
		Color:       color,
		Fields: []discordField{
			{Name: "Healing Score", Value: fmt.Sprintf("%d", u.HealingScore), Inline: true},
			{Name: "Pull Request", Value: prLine, Inline: true},
		},
		Footer: &discordFooter{Text: "GopherGuard • " + time.Now().UTC().Format("2006-01-02 15:04 UTC")},
	}
	if !u.SafeMode && u.PRURL != "" {
		embed.URL = u.PRURL
	}

	return discordPayload{
		Username: "GopherGuard",
		Embeds:   []discordEmbed{embed},
	}
}
