// Package vault is a minimal client for HashiCorp Vault (spec 0003): login
// with a JWT, read a field of a secret (KV v1, KV v2 or any other engine),
// check what the token may read and revoke the token.
package vault

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
	"strings"
	"time"
)

type Config struct {
	// Address is Vault's base URL, e.g. https://vault.example.com:8200.
	Address string
	// CACert is a PEM bundle trusted besides the system's.
	CACert    string
	Namespace string
}

type Client struct {
	base      *url.URL
	http      *http.Client
	namespace string
	token     string
	// mounts caches the KV version of each mount, by mount path.
	mounts map[string]string
}

func New(cfg Config) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(cfg.Address, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("the Vault address must be an http(s) URL, got %q", cfg.Address)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.CACert != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(cfg.CACert)) {
			return nil, errors.New("the Vault CA certificate is not a valid PEM certificate")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &Client{base: u, http: &http.Client{Timeout: 30 * time.Second, Transport: transport},
		namespace: cfg.Namespace, mounts: map[string]string{}}, nil
}

// Error is an error response from Vault.
type Error struct {
	Status int
	Errors []string
}

func (e *Error) Error() string {
	msg := strings.Join(e.Errors, "; ")
	switch {
	case e.Status == http.StatusForbidden && msg == "":
		msg = "permission denied"
	case msg == "":
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("Vault answered %d: %s", e.Status, msg)
}

// ErrUnreachable wraps network errors talking to Vault.
var ErrUnreachable = errors.New("can't reach Vault")

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+"/v1/"+strings.TrimPrefix(path, "/"), body)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("X-Vault-Token", c.token)
	}
	if c.namespace != "" {
		req.Header.Set("X-Vault-Namespace", c.namespace)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w at %s: %v", ErrUnreachable, c.base, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		e := &Error{Status: resp.StatusCode}
		var payload struct {
			Errors []string `json:"errors"`
		}
		if json.Unmarshal(raw, &payload) == nil {
			e.Errors = payload.Errors
		}
		return e
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Login exchanges a JWT for a token with the jwt auth method mounted at authPath.
func (c *Client) Login(ctx context.Context, authPath, role, jwt string) error {
	var out struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	err := c.do(ctx, "POST", "auth/"+strings.Trim(authPath, "/")+"/login", map[string]string{"role": role, "jwt": jwt}, &out)
	if err != nil {
		return err
	}
	if out.Auth.ClientToken == "" {
		return errors.New("Vault's login answer has no token")
	}
	c.token = out.Auth.ClientToken
	return nil
}

// Token is the token from Login, so callers can mask it.
func (c *Client) Token() string { return c.token }

// Revoke revokes the token from Login.
func (c *Client) Revoke(ctx context.Context) error {
	if c.token == "" {
		return nil
	}
	err := c.do(ctx, "POST", "auth/token/revoke-self", nil, nil)
	c.token = ""
	return err
}

// apiPath turns a logical path, as `vault kv get` takes it, into the API
// path to read: KV v2 keeps its data under <mount>/data/.
func (c *Client) apiPath(ctx context.Context, logical string) string {
	for mount, version := range c.mounts {
		if strings.HasPrefix(logical, mount) {
			return kvPath(mount, version, logical)
		}
	}
	var out struct {
		Data struct {
			Path    string `json:"path"`
			Type    string `json:"type"`
			Options struct {
				Version string `json:"version"`
			} `json:"options"`
		} `json:"data"`
	}
	// Any token that can use a path may ask about its mount. If it can't,
	// the path is read as is.
	if err := c.do(ctx, "GET", "sys/internal/ui/mounts/"+logical, nil, &out); err != nil || out.Data.Path == "" {
		return logical
	}
	version := ""
	if out.Data.Type == "kv" || out.Data.Type == "generic" {
		version = out.Data.Options.Version
	}
	c.mounts[out.Data.Path] = version
	return kvPath(out.Data.Path, version, logical)
}

func kvPath(mount, version, logical string) string {
	if version != "2" {
		return logical
	}
	return mount + "data/" + strings.TrimPrefix(logical, mount)
}

// ReadSecret returns the data of a secret, unwrapping KV v2.
func (c *Client) ReadSecret(ctx context.Context, logical string) (map[string]any, error) {
	path := c.apiPath(ctx, logical)
	var out struct {
		Data map[string]any `json:"data"`
	}
	if err := c.do(ctx, "GET", path, nil, &out); err != nil {
		var ve *Error
		if errors.As(err, &ve) && ve.Status == http.StatusNotFound {
			return nil, fmt.Errorf("no secret at %s", logical)
		}
		return nil, err
	}
	if path != logical { // KV v2: the fields are under data.data
		inner, ok := out.Data["data"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("no secret at %s (deleted or never written)", logical)
		}
		return inner, nil
	}
	return out.Data, nil
}

// FieldString turns a secret's field into text: strings as they are,
// anything else as JSON.
func FieldString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

// CanRead reports whether the token may read the logical path, without reading it.
func (c *Client) CanRead(ctx context.Context, logical string) (bool, error) {
	path := c.apiPath(ctx, logical)
	var out struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := c.do(ctx, "POST", "sys/capabilities-self", map[string]any{"paths": []string{path}}, &out); err != nil {
		return false, err
	}
	for _, cap := range out.Capabilities {
		if cap == "read" || cap == "root" {
			return true, nil
		}
	}
	return false, nil
}
