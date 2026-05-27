package bridge

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sarahmaeve/toolbox/pkg/messagestore"
)

// DefaultClientTimeout bounds a single HTTP round-trip. Matches the
// server's WriteTimeout so both sides agree when to give up.
const DefaultClientTimeout = 60 * time.Second

// ClientConfig configures a bridge Client at construction.
type ClientConfig struct {
	// BaseURL is the bridge URL (e.g. "https://127.0.0.1:21517").
	// Required. http:// and https:// are both accepted; only https://
	// configures TLS trust.
	BaseURL string

	// CAPath is the optional path to a PEM-encoded CA bundle for
	// HTTPS trust. When empty, the system root pool is used. When
	// non-empty, the file is loaded and added on top of the system
	// pool (so a user who's run `mkcert -install` continues to work
	// even without a project-managed anchor).
	//
	// InsecureSkipVerify is never set, even on localhost.
	CAPath string

	// Timeout bounds a single HTTP round-trip. Zero applies
	// DefaultClientTimeout.
	Timeout time.Duration
}

// Client is a low-ceremony HTTP client for the bridge service.
type Client struct {
	baseURL string
	httpC   *http.Client
}

// NewClient returns a Client configured against cfg.
//
// Accepted schemes:
//   - https:// (production): TLS trust uses cfg.CAPath if present,
//     falling back to the system root pool. Never InsecureSkipVerify.
//   - http:// (tests): TLS config is skipped; plain HTTP is selected
//     by scheme, not by an opt-out flag.
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("bridge: ClientConfig.BaseURL is required")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL %q: %w", cfg.BaseURL, err)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return nil, fmt.Errorf("base URL %q: unsupported scheme %q (want http or https)",
			cfg.BaseURL, u.Scheme)
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultClientTimeout
	}
	httpC := &http.Client{Timeout: timeout}
	if u.Scheme == "https" {
		tlsCfg, err := buildTLSConfig(cfg.CAPath)
		if err != nil {
			return nil, fmt.Errorf("build TLS config: %w", err)
		}
		httpC.Transport = &http.Transport{TLSClientConfig: tlsCfg}
	}

	return &Client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		httpC:   httpC,
	}, nil
}

// buildTLSConfig loads the CA anchor at caPath if present, falling
// back to the system root pool. Never disables verification.
func buildTLSConfig(caPath string) (*tls.Config, error) {
	if caPath == "" {
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil //nolint:gosec // G402: TLS 1.2+ baseline; system pool used
	}

	data, err := os.ReadFile(caPath) //nolint:gosec // G304: path is operator-supplied via ClientConfig
	if errors.Is(err, os.ErrNotExist) {
		// Anchor absent. Fall back to system roots — bridge against a
		// machine where `mkcert -install` populated the OS keychain
		// but the project's CAPath isn't yet wired.
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil //nolint:gosec // G402: TLS 1.2+ baseline; system pool used
	}
	if err != nil {
		return nil, fmt.Errorf("read CA anchor %s: %w", caPath, err)
	}

	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("CA anchor %s contained no usable PEM certificates", caPath)
	}

	return &tls.Config{
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
	}, nil
}

// CreateSession starts a bridge session and returns the server's
// populated Session record.
func (c *Client) CreateSession(ctx context.Context, target, metadata string) (*messagestore.Session, error) {
	var sess messagestore.Session
	err := c.postJSON(ctx, "/api/sessions", "create session",
		createSessionRequest{Target: target, Metadata: metadata}, &sess)
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// DepositRequest carries the fields a client supplies when depositing
// a message. Role, Type, and Content are required; SenderID,
// SubjectID, and Metadata are optional.
type DepositRequest struct {
	Role      string
	SenderID  string
	Type      string
	SubjectID string
	Content   json.RawMessage
	Metadata  string
}

// DepositMessage posts a message to the given session. Returns the
// server's populated Message on success, or an error carrying the
// server's {"error": "..."} body on validation failure.
func (c *Client) DepositMessage(ctx context.Context, sessionID string, req DepositRequest) (*messagestore.Message, error) {
	if sessionID == "" {
		return nil, errors.New("deposit message: session id is required")
	}
	var msg messagestore.Message
	err := c.postJSON(ctx, "/api/sessions/"+sessionID+"/messages", "deposit message",
		depositMessageRequest{
			Role:      req.Role,
			SenderID:  req.SenderID,
			Type:      req.Type,
			SubjectID: req.SubjectID,
			Content:   req.Content,
			Metadata:  req.Metadata,
		},
		&msg,
	)
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

// MessageQuery is the filter shape for GetLatestMessage. SessionID is
// required; other fields are optional.
type MessageQuery struct {
	Role      string
	SenderID  string
	Type      string
	SubjectID string
}

// GetLatestMessage returns the most recent message matching the given
// filter. Returns an error wrapping the server's response body on
// non-2xx.
func (c *Client) GetLatestMessage(ctx context.Context, sessionID string, q MessageQuery) (*messagestore.Message, error) {
	if sessionID == "" {
		return nil, errors.New("get latest message: session id is required")
	}
	path := "/api/sessions/" + sessionID + "/messages/latest"
	v := url.Values{}
	if q.Role != "" {
		v.Set("role", q.Role)
	}
	if q.SenderID != "" {
		v.Set("sender_id", q.SenderID)
	}
	if q.Type != "" {
		v.Set("type", q.Type)
	}
	if q.SubjectID != "" {
		v.Set("subject_id", q.SubjectID)
	}
	if encoded := v.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var msg messagestore.Message
	if err := c.getJSON(ctx, path, "get latest message", &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// postJSON is the shared POST+JSON-round-trip primitive.
func (c *Client) postJSON(ctx context.Context, path, verb string, body, out any) error {
	reqBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal %s body: %w", verb, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+path, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("build %s request: %w", verb, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpC.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return decodeServerError(resp, verb)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s response: %w", verb, err)
	}
	return nil
}

// getJSON is the shared GET+JSON-round-trip primitive.
func (c *Client) getJSON(ctx context.Context, path, verb string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build %s request: %w", verb, err)
	}

	resp, err := c.httpC.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return decodeServerError(resp, verb)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s response: %w", verb, err)
	}
	return nil
}

// drainAndClose is the canonical "I'm done with this response body"
// sequence. Without the drain, Go's HTTP transport can't return the
// underlying TCP connection to the keep-alive pool.
func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close() //nolint:errcheck
}

// decodeServerError reads a non-success response body, extracts the
// server's {"error": "..."} message when present, and returns a
// formatted error with status code.
func decodeServerError(resp *http.Response, verb string) error {
	const maxRead = 4 * 1024
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRead))

	var envelope struct {
		Error string `json:"error"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &envelope) == nil && envelope.Error != "" {
		return fmt.Errorf("%s: server returned %d: %s",
			verb, resp.StatusCode, envelope.Error)
	}

	excerpt := string(bytes.TrimSpace(raw))
	if excerpt == "" {
		if readErr != nil {
			return fmt.Errorf("%s: server returned %d, but response body read failed: %w",
				verb, resp.StatusCode, readErr)
		}
		return fmt.Errorf("%s: server returned %d with empty body",
			verb, resp.StatusCode)
	}
	return fmt.Errorf("%s: server returned %d: %s", verb, resp.StatusCode, excerpt)
}
