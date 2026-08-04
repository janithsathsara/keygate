package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// sendGridEndpoint is the Web API v3 Mail Send endpoint. Overridable
// via sendGridDeliverTo so tests can point at an httptest.Server.
var sendGridEndpoint = "https://api.sendgrid.com/v3/mail/send"

// sendGridDeliver sends one email through the SendGrid Web API v3 over
// HTTPS (port 443). This is the transport for hosting providers that
// block SMTP egress on ports 25/465/587 (Render free tier, Heroku
// free dynos, etc.). The endpoint accepts an API key as a bearer token
// and a JSON payload; any 2xx response means the message was accepted.
func sendGridDeliver(client *http.Client, apiKey, from, to, subject, htmlBody string) error {
	return sendGridDeliverTo(client, sendGridEndpoint, apiKey, from, to, subject, htmlBody)
}

func sendGridDeliverTo(client *http.Client, endpoint, apiKey, from, to, subject, htmlBody string) error {
	if apiKey == "" {
		return fmt.Errorf("sendgrid: API key is empty (set SENDGRID_API_KEY or the admin panel setting)")
	}

	payload := sendGridEmail{
		Personalizations: []sendGridPersonalization{{To: []sendGridAddress{{Email: to}}}},
		From:             sendGridAddress{Email: from},
		Subject:          subject,
		Content:          []sendGridContent{{Type: "text/html", Value: htmlBody}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("sendgrid: encode: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("sendgrid: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sendgrid: post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// Drain so the connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	// SendGrid returns a JSON errors array on failure (e.g.
	// "The from address does not match a verified Sender Identity").
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("sendgrid: api status %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
}

type sendGridEmail struct {
	Personalizations []sendGridPersonalization `json:"personalizations"`
	From             sendGridAddress           `json:"from"`
	Subject          string                    `json:"subject"`
	Content          []sendGridContent         `json:"content"`
}

type sendGridPersonalization struct {
	To []sendGridAddress `json:"to"`
}

type sendGridAddress struct {
	Email string `json:"email"`
}

type sendGridContent struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}
