package service

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sendGridTestClient returns an *http.Client whose requests are
// answered by the handler and a close function for the server.
func sendGridTestClient(t *testing.T, h http.HandlerFunc) (*http.Client, string, func()) {
	t.Helper()
	srv := httptest.NewServer(h)
	return srv.Client(), srv.URL, srv.Close
}

// TestSendGridDeliver_Success verifies the request shape against the
// real SendGrid v3 contract: POST to /v3/mail/send, bearer auth, and
// a JSON body with personalizations/from/subject/content.
func TestSendGridDeliver_Success(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any

	client, url, closeSrv := sendGridTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("body is not valid JSON: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	})
	defer closeSrv()

	err := sendGridDeliverTo(client, url+"/v3/mail/send", "SG.testkey", "Keygate <noreply@keygate.test>",
		"customer@example.com", "Hello", "<p>hi</p>")
	if err != nil {
		t.Fatalf("sendGridDeliver: %v", err)
	}

	if gotPath != "/v3/mail/send" {
		t.Errorf("path = %q, want /v3/mail/send", gotPath)
	}
	if gotAuth != "Bearer SG.testkey" {
		t.Errorf("Authorization = %q, want Bearer SG.testkey", gotAuth)
	}

	to := gotBody["personalizations"].([]any)[0].(map[string]any)["to"].([]any)[0].(map[string]any)
	if to["email"] != "customer@example.com" {
		t.Errorf("to.email = %v, want customer@example.com", to["email"])
	}
	if from := gotBody["from"].(map[string]any); from["email"] != "Keygate <noreply@keygate.test>" {
		t.Errorf("from.email = %v", from["email"])
	}
	if gotBody["subject"] != "Hello" {
		t.Errorf("subject = %v, want Hello", gotBody["subject"])
	}
	content := gotBody["content"].([]any)[0].(map[string]any)
	if content["type"] != "text/html" || content["value"] != "<p>hi</p>" {
		t.Errorf("content = %v", content)
	}
}

// TestSendGridDeliver_RejectsErrorStatus verifies non-2xx responses
// surface the provider's error body in the returned error.
func TestSendGridDeliver_RejectsErrorStatus(t *testing.T) {
	client, url, closeSrv := sendGridTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errors":[{"message":"The from address does not match a verified Sender Identity"}]}`)
	})
	defer closeSrv()

	err := sendGridDeliverTo(client, url, "SG.badkey", "noreply@keygate.test", "a@b.test", "s", "<p>x</p>")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "verified Sender Identity") {
		t.Errorf("error should surface status and body, got: %v", err)
	}
}

// TestSendGridDeliver_TransportFailure surfaces network-level errors.
func TestSendGridDeliver_TransportFailure(t *testing.T) {
	client, url, closeSrv := sendGridTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Simulate a hang by hijacking nothing — just close the conn.
		w.Header().Set("Connection", "close")
	})
	closeSrv() // server already down → dial error

	err := sendGridDeliverTo(client, url, "SG.key", "noreply@keygate.test", "a@b.test", "s", "<p>x</p>")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "sendgrid") {
		t.Errorf("error should be prefixed with sendgrid, got: %v", err)
	}
}

// TestSendGridDeliver_MissingAPIKey fails fast without a network call.
func TestSendGridDeliver_MissingAPIKey(t *testing.T) {
	err := sendGridDeliver(http.DefaultClient, "", "noreply@keygate.test", "a@b.test", "s", "<p>x</p>")
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("expected API key error, got: %v", err)
	}
}

// TestEmailService_DeliverDispatchesToSendGrid drives the runtime
// provider switch through EmailService.deliver: a service configured
// for SMTP (host set) must route to the HTTP transport when the
// effective provider is "sendgrid".
func TestEmailService_DeliverDispatchesToSendGrid(t *testing.T) {
	var called bool
	client, url, closeSrv := sendGridTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	})
	defer closeSrv()

	svc := &EmailService{
		host:       "smtp.example.com",
		port:       "587",
		from:       "noreply@keygate.test",
		provider:   "sendgrid",
		sgKey:      "SG.dispatch",
		httpClient: client,
		logger:     slog.Default(),
	}

	// Point the package endpoint at the test server for this test.
	oldEndpoint := sendGridEndpoint
	sendGridEndpoint = url
	defer func() { sendGridEndpoint = oldEndpoint }()

	err := svc.deliver(svc.from, svc.provider, svc.sgKey, "a@b.test", "s", "<p>x</p>")
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if !called {
		t.Error("sendgrid endpoint was not hit")
	}
}

// TestEmailService_DeliverDefaultsToSMTP confirms the default provider
// keeps the SMTP path (regression guard for existing deployments).
func TestEmailService_DeliverDefaultsToSMTP(t *testing.T) {
	svc := &EmailService{
		host:     "smtp.example.com",
		port:     "25",
		from:     "noreply@keygate.test",
		provider: "smtp",
		logger:   slog.Default(),
	}
	err := svc.deliver(svc.from, svc.provider, "", "a@b.test", "s", "<p>x</p>")
	if err == nil || !strings.Contains(err.Error(), "dial") {
		t.Errorf("expected dial error against unreachable SMTP host, got: %v", err)
	}
}
