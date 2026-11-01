// Package s3 is a minimal S3 client for storing Terraform state versions in
// any S3-compatible service, Garage first (spec 0002, R4). It signs requests
// with AWS Signature Version 4 and supports what the state backend needs:
// put, get, delete and list.
package s3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Config struct {
	// Endpoint is the service's base URL, e.g. http://garage:3900.
	Endpoint string
	Bucket   string
	Region   string
	// Prefix is prepended to every key, so a bucket can be shared.
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
	// PathStyle addresses the bucket in the path (endpoint/bucket/key)
	// instead of the host name; Garage and most self-hosted services need it.
	PathStyle bool
	// CACert is a PEM bundle trusted besides the system's, for endpoints with
	// a certificate from a private CA.
	CACert string
}

type Client struct {
	cfg  Config
	base *url.URL
	http *http.Client
	now  func() time.Time
}

func New(cfg Config) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(cfg.Endpoint, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("S3 endpoint must be an http(s) URL, got %q", cfg.Endpoint)
	}
	switch {
	case cfg.Bucket == "":
		return nil, errors.New("S3 bucket is required")
	case cfg.AccessKeyID == "" || cfg.SecretAccessKey == "":
		return nil, errors.New("S3 access key ID and secret access key are required")
	}
	if cfg.Region == "" {
		cfg.Region = "garage"
	}
	cfg.Prefix = strings.Trim(cfg.Prefix, "/")
	client := &http.Client{Timeout: 2 * time.Minute}
	if strings.TrimSpace(cfg.CACert) != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(cfg.CACert)) {
			return nil, errors.New("S3 CA certificate: no PEM certificate found")
		}
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		client.Transport = t
	}
	return &Client{cfg: cfg, base: u, http: client, now: time.Now}, nil
}

// Config returns the client's configuration.
func (c *Client) Config() Config { return c.cfg }

// Error is an error response from the S3 service.
type Error struct {
	Op      string
	Status  int
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("S3 %s: HTTP %d", e.Op, e.Status)
	}
	return fmt.Sprintf("S3 %s: %s: %s", e.Op, e.Code, e.Message)
}

// ErrNotFound means the object does not exist.
var ErrNotFound = errors.New("object not found")

func (c *Client) objectKey(key string) string {
	if c.cfg.Prefix == "" {
		return key
	}
	return c.cfg.Prefix + "/" + key
}

func (c *Client) Put(ctx context.Context, key string, data []byte) error {
	resp, err := c.do(ctx, "PUT", c.objectKey(key), nil, data)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	resp, err := c.do(ctx, "GET", c.objectKey(key), nil, nil)
	if err != nil {
		var s3err *Error
		if errors.As(err, &s3err) && s3err.Status == http.StatusNotFound {
			return nil, fmt.Errorf("%s: %w", key, ErrNotFound)
		}
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// Delete removes an object; deleting one that doesn't exist succeeds.
func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.do(ctx, "DELETE", c.objectKey(key), nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

type listResult struct {
	Contents []struct {
		Key          string    `xml:"Key"`
		LastModified time.Time `xml:"LastModified"`
	} `xml:"Contents"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

// List returns the keys (without the prefix) of objects modified before t.
func (c *Client) List(ctx context.Context, before time.Time) ([]string, error) {
	prefix := ""
	if c.cfg.Prefix != "" {
		prefix = c.cfg.Prefix + "/"
	}
	var keys []string
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := c.do(ctx, "GET", "", q, nil)
		if err != nil {
			return nil, err
		}
		var res listResult
		err = xml.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("S3 list: %w", err)
		}
		for _, o := range res.Contents {
			if o.LastModified.Before(before) {
				keys = append(keys, strings.TrimPrefix(o.Key, prefix))
			}
		}
		if !res.IsTruncated || res.NextContinuationToken == "" {
			return keys, nil
		}
		token = res.NextContinuationToken
	}
}

// Check writes, reads and deletes a probe object, and explains a failure in
// terms an operator can act on: the endpoint, the credentials, the bucket or
// the key's permissions (R4.4).
func (c *Client) Check(ctx context.Context) error {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	key := ".firstlume-check/" + hex.EncodeToString(b)
	data := []byte("firstlume storage check")
	if err := c.Put(ctx, key, data); err != nil {
		return c.explain("write", err)
	}
	got, err := c.Get(ctx, key)
	if err != nil {
		return c.explain("read", err)
	}
	if !bytes.Equal(got, data) {
		return errors.New("S3 storage check: the object read back differs from what was written")
	}
	if err := c.Delete(ctx, key); err != nil {
		return c.explain("delete", err)
	}
	return nil
}

func (c *Client) explain(what string, err error) error {
	var s3err *Error
	var netErr net.Error
	switch {
	case errors.As(err, &s3err):
		msg := strings.ToLower(s3err.Message)
		switch {
		case s3err.Code == "NoSuchBucket":
			return fmt.Errorf("S3 storage check: bucket %q does not exist at %s", c.cfg.Bucket, c.cfg.Endpoint)
		case s3err.Code == "InvalidAccessKeyId", s3err.Code == "SignatureDoesNotMatch", s3err.Code == "InvalidSignature",
			// Garage answers AccessDenied for both, telling them apart in the message.
			strings.Contains(msg, "signature"), strings.Contains(msg, "no such key"):
			return fmt.Errorf("S3 storage check: the access key or secret was rejected by %s (%s)", c.cfg.Endpoint, s3err.Message)
		case s3err.Code == "AccessDenied", s3err.Code == "Forbidden":
			return fmt.Errorf("S3 storage check: access denied to %s bucket %q: the key needs read and write permission, or the credentials are wrong (%s)",
				what, c.cfg.Bucket, s3err.Message)
		}
		return fmt.Errorf("S3 storage check (%s): %w", what, err)
	case errors.As(err, &netErr), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("S3 storage check: can't reach the endpoint %s: %w", c.cfg.Endpoint, err)
	}
	return fmt.Errorf("S3 storage check (%s): %w", what, err)
}

// do sends a signed request for an object key ("" for the bucket itself).
func (c *Client) do(ctx context.Context, method, key string, query url.Values, body []byte) (*http.Response, error) {
	u := *c.base
	path := u.Path
	if c.cfg.PathStyle {
		path += "/" + c.cfg.Bucket
	} else {
		u.Host = c.cfg.Bucket + "." + u.Host
	}
	if key != "" {
		path += "/" + key
	} else if !c.cfg.PathStyle || path == "" {
		path += "/"
	}
	u.Path = path
	u.RawPath = encodePath(path)
	u.RawQuery = canonicalQuery(query)

	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(body))
	c.sign(req, u.RawPath, body)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		e := &Error{Op: method, Status: resp.StatusCode}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = xml.Unmarshal(raw, e)
		if e.Code == "" && resp.StatusCode == http.StatusNotFound {
			e.Code = "NoSuchKey"
		}
		return nil, e
	}
	return resp, nil
}

// sign adds AWS Signature Version 4 headers.
func (c *Client) sign(req *http.Request, canonicalURI string, body []byte) {
	now := c.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	payload := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(payload[:])
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)

	signed := "host;x-amz-content-sha256;x-amz-date"
	canonical := strings.Join([]string{
		req.Method,
		canonicalURI,
		req.URL.RawQuery,
		"host:" + req.URL.Host + "\nx-amz-content-sha256:" + payloadHash + "\nx-amz-date:" + amzDate + "\n",
		signed,
		payloadHash,
	}, "\n")
	scope := date + "/" + c.cfg.Region + "/s3/aws4_request"
	crHash := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(crHash[:])

	key := hmacSHA256([]byte("AWS4"+c.cfg.SecretAccessKey), date)
	key = hmacSHA256(key, c.cfg.Region)
	key = hmacSHA256(key, "s3")
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.cfg.AccessKeyID, scope, signed, sig))
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// encodePath URI-encodes each segment of an S3 path the way SigV4 expects.
func encodePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = uriEncode(s)
	}
	return strings.Join(segs, "/")
}

func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, uriEncode(k)+"="+uriEncode(v))
		}
	}
	return strings.Join(parts, "&")
}

// uriEncode encodes everything but the unreserved characters (RFC 3986).
func uriEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.' || ch == '~' {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}
