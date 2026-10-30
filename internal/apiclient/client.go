// Package apiclient is the HTTP client used by the agent and the executor.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
)

// ErrNoLongerOwner is returned when the server says the run moved on (HTTP 409).
var ErrNoLongerOwner = errors.New("run is no longer executing this phase")

type Client struct {
	base  string
	token string
	http  *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		http:  &http.Client{Timeout: 5 * time.Minute},
	}
}

// StatusError is a non-2xx response.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("server returned %d: %s", e.Code, e.Message)
}

func (c *Client) do(ctx context.Context, method, path, contentType string, body []byte, out any) (int, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 500 * time.Millisecond):
			}
		}
		code, err := c.once(ctx, method, path, contentType, body, out)
		if err == nil {
			return code, nil
		}
		lastErr = err
		// Retry transport errors and 5xx; anything else is a definitive answer.
		var se *StatusError
		if errors.As(err, &se) && se.Code < 500 {
			if se.Code == http.StatusConflict {
				return code, ErrNoLongerOwner
			}
			return code, err
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
	}
	return 0, lastErr
}

func (c *Client) once(ctx context.Context, method, path, contentType string, body []byte, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e api.ErrorResponse
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(raw, &e) != nil || e.Error == "" {
			// Not our API (e.g. a proxy or another service on the port): keep it short.
			e.Error = strings.Join(strings.Fields(string(raw)), " ")
			if len(e.Error) > 200 {
				e.Error = e.Error[:200] + "..."
			}
		}
		return resp.StatusCode, &StatusError{Code: resp.StatusCode, Message: e.Error}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}
	if raw, ok := out.(*[]byte); ok {
		*raw, err = io.ReadAll(resp.Body)
		return resp.StatusCode, err
	}
	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) doJSON(ctx context.Context, method, path string, in, out any) (int, error) {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return 0, err
		}
	}
	return c.do(ctx, method, path, "application/json", body, out)
}

// --- agent API ---

// Claim returns the next assignment, or nil when there is nothing to run.
func (c *Client) Claim(ctx context.Context) (*api.Assignment, error) {
	var a api.Assignment
	code, err := c.doJSON(ctx, http.MethodPost, "/agent/v1/claim", nil, &a)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNoContent {
		return nil, nil
	}
	return &a, nil
}

func (c *Client) ReportFailure(ctx context.Context, runID string, phase api.Phase, reason string) error {
	_, err := c.doJSON(ctx, http.MethodPost, "/agent/v1/runs/"+runID+"/fail",
		api.ReportFailureRequest{Phase: phase, Reason: reason}, nil)
	return err
}

// --- executor API ---

func (c *Client) Spec(ctx context.Context) (*api.ExecSpec, error) {
	var s api.ExecSpec
	_, err := c.doJSON(ctx, http.MethodGet, "/exec/v1/spec", nil, &s)
	return &s, err
}

// SetCommit records the commit the plan phase checked out and, when it
// parsed, the stack's configuration at that commit.
func (c *Client) SetCommit(ctx context.Context, req api.CommitRequest) error {
	_, err := c.doJSON(ctx, http.MethodPost, "/exec/v1/commit", req, nil)
	return err
}

func (c *Client) AppendLog(ctx context.Context, data []byte) error {
	_, err := c.do(ctx, http.MethodPost, "/exec/v1/logs", "text/plain; charset=utf-8", data, nil)
	return err
}

func (c *Client) Heartbeat(ctx context.Context) error {
	_, err := c.doJSON(ctx, http.MethodPost, "/exec/v1/heartbeat", nil, nil)
	return err
}

func (c *Client) PutArtifact(ctx context.Context, name string, data []byte) error {
	_, err := c.do(ctx, http.MethodPut, "/exec/v1/artifacts/"+name, "application/octet-stream", data, nil)
	return err
}

// Step starts, finishes or skips a step of the run phase.
func (c *Client) Step(ctx context.Context, req api.StepRequest) error {
	_, err := c.doJSON(ctx, http.MethodPost, "/exec/v1/steps", req, nil)
	return err
}

// PutChanges records what the plan changes, resource by resource.
func (c *Client) PutChanges(ctx context.Context, changes []api.ResourceChange) error {
	_, err := c.doJSON(ctx, http.MethodPut, "/exec/v1/changes", changes, nil)
	return err
}

func (c *Client) GetArtifact(ctx context.Context, name string) ([]byte, error) {
	var data []byte
	_, err := c.do(ctx, http.MethodGet, "/exec/v1/artifacts/"+name, "", nil, &data)
	return data, err
}

func (c *Client) Complete(ctx context.Context, req api.CompleteRequest) (*api.Run, error) {
	var run api.Run
	_, err := c.doJSON(ctx, http.MethodPost, "/exec/v1/complete", req, &run)
	return &run, err
}
