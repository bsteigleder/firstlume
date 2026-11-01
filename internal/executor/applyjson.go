package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/apiclient"
)

const (
	progressInterval = time.Second
	// maxResourceErrors caps the errors kept for one resource.
	maxResourceErrors = 20
)

// applyStream reads the output of `apply -json` (spec 0004). It writes the
// human-readable line of each message to the run's log and reports, resource
// by resource, what the apply did. Output that is not JSON goes to the log as
// it is, and then nothing is reported (R1.7).
type applyStream struct {
	out  io.Writer
	mask func(string) string
	// unit prefixes the keys of a Terragrunt unit's resources.
	unit string
	send func(context.Context, api.ApplyProgressRequest) error

	mu      sync.Mutex
	partial []byte
	// tracking is set by the first JSON message; reported once a report went out.
	tracking, reported bool
	res                map[string]*api.ApplyProgress
	dirty              map[string]bool
	// general is the first error not tied to a resource (R1.6).
	general string
	// failed lists the resources with errors, in the order they came.
	failed  []string
	stopped bool
	warned  bool
}

func newApplyStream(out io.Writer, mask func(string) string, unit string,
	send func(context.Context, api.ApplyProgressRequest) error) *applyStream {
	return &applyStream{out: out, mask: mask, unit: unit, send: send,
		res: map[string]*api.ApplyProgress{}, dirty: map[string]bool{}}
}

// tfMessage is the part of Terraform's machine-readable UI that is used.
type tfMessage struct {
	Message   string    `json:"@message"`
	Timestamp time.Time `json:"@timestamp"`
	Type      string    `json:"type"`
	Hook      *struct {
		Resource struct {
			Addr string `json:"addr"`
		} `json:"resource"`
		Action string `json:"action"`
	} `json:"hook"`
	Diagnostic *tfDiagnostic `json:"diagnostic"`
}

type tfDiagnostic struct {
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Detail   string `json:"detail"`
	Address  string `json:"address"`
	Range    *struct {
		Filename string `json:"filename"`
		Start    struct {
			Line int `json:"line"`
		} `json:"start"`
	} `json:"range"`
	Snippet *struct {
		Context   string `json:"context"`
		Code      string `json:"code"`
		StartLine int    `json:"start_line"`
	} `json:"snippet"`
}

func (s *applyStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partial = append(s.partial, p...)
	for {
		i := bytes.IndexByte(s.partial, '\n')
		if i < 0 {
			break
		}
		s.line(string(s.partial[:i]))
		s.partial = s.partial[i+1:]
	}
	return len(p), nil
}

// Close handles a last line without a newline.
func (s *applyStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.partial) > 0 {
		s.line(string(s.partial))
		s.partial = nil
	}
	return nil
}

func (s *applyStream) line(line string) {
	var m tfMessage
	if !strings.HasPrefix(strings.TrimSpace(line), "{") || json.Unmarshal([]byte(line), &m) != nil || m.Type == "" {
		fmt.Fprintln(s.out, line)
		return
	}
	s.tracking = true
	if m.Timestamp.IsZero() {
		m.Timestamp = time.Now()
	}
	if m.Type == "diagnostic" && m.Diagnostic != nil {
		s.diagnostic(m.Diagnostic)
		return
	}
	fmt.Fprintln(s.out, m.Message)
	if m.Hook == nil || m.Hook.Resource.Addr == "" {
		return
	}
	action := api.ChangeAction(m.Hook.Action)
	if action != api.ActionCreate && action != api.ActionUpdate && action != api.ActionDelete {
		return // data source reads and anything else are not changes
	}
	at := m.Timestamp
	switch m.Type {
	case "apply_start":
		r := s.resource(m.Hook.Resource.Addr)
		r.Steps = append(r.Steps, api.ApplyStep{Action: action, StartedAt: &at})
	case "apply_complete", "apply_errored":
		r := s.resource(m.Hook.Resource.Addr)
		for i := len(r.Steps) - 1; i >= 0; i-- {
			if st := &r.Steps[i]; st.Action == action && st.FinishedAt == nil {
				st.FinishedAt = &at
				st.Failed = m.Type == "apply_errored"
				break
			}
		}
	}
}

// diagnostic writes the error or warning as Terraform prints it, and records
// errors on their resource.
func (s *applyStream) diagnostic(d *tfDiagnostic) {
	label := "Warning"
	if d.Severity == "error" {
		label = "Error"
	}
	var b strings.Builder
	b.WriteString("╷\n│ " + label + ": " + d.Summary + "\n")
	if d.Address != "" || d.Range != nil {
		b.WriteString("│ \n")
	}
	if d.Address != "" {
		b.WriteString("│   with " + d.Address + ",\n")
	}
	if d.Range != nil {
		fmt.Fprintf(&b, "│   on %s line %d", d.Range.Filename, d.Range.Start.Line)
		if d.Snippet != nil && d.Snippet.Context != "" {
			b.WriteString(", in " + d.Snippet.Context)
		}
		b.WriteString(":\n")
		if d.Snippet != nil {
			fmt.Fprintf(&b, "│  %3d: %s\n", d.Snippet.StartLine, d.Snippet.Code)
		}
	}
	if detail := strings.TrimRight(d.Detail, "\n"); detail != "" {
		b.WriteString("│ \n")
		for _, l := range strings.Split(detail, "\n") {
			b.WriteString("│ " + l + "\n")
		}
	}
	b.WriteString("╵")
	fmt.Fprintln(s.out, b.String())

	if d.Severity != "error" {
		return
	}
	if d.Address == "" {
		if s.general == "" {
			s.general = s.mask(d.Summary)
		}
		return
	}
	r := s.resource(d.Address)
	if len(r.Errors) == 0 {
		s.failed = append(s.failed, d.Address)
	}
	if len(r.Errors) < maxResourceErrors {
		e := api.ApplyError{Summary: s.mask(d.Summary), Detail: s.mask(strings.TrimRight(d.Detail, "\n"))}
		if d.Range != nil {
			e.File, e.Line = s.mask(d.Range.Filename), d.Range.Start.Line
		}
		r.Errors = append(r.Errors, e)
	}
}

// resource returns the progress of an address and marks it to be reported.
func (s *applyStream) resource(addr string) *api.ApplyProgress {
	key := addr
	if s.unit != "" {
		key = s.unit + ":" + addr
	}
	r := s.res[key]
	if r == nil {
		r = &api.ApplyProgress{Key: key}
		s.res[key] = r
	}
	s.dirty[key] = true
	return r
}

// flush reports the resources that changed since the last report. The first
// report goes out even when empty: it tells the server progress is reported.
func (s *applyStream) flush(ctx context.Context) {
	s.mu.Lock()
	if !s.tracking || s.stopped || (s.reported && len(s.dirty) == 0) {
		s.mu.Unlock()
		return
	}
	req := api.ApplyProgressRequest{Resources: []api.ApplyProgress{}}
	sent := s.dirty
	for key := range sent {
		r := *s.res[key]
		r.Steps = append([]api.ApplyStep(nil), r.Steps...)
		r.Errors = append([]api.ApplyError(nil), r.Errors...)
		req.Resources = append(req.Resources, r)
	}
	s.dirty = map[string]bool{}
	s.mu.Unlock()

	err := s.send(ctx, req)

	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case err == nil:
		s.reported = true
	case errors.Is(err, apiclient.ErrNoLongerOwner):
		s.stopped = true
	default:
		// Reports are snapshots: send these again with the next one.
		for key := range sent {
			s.dirty[key] = true
		}
		if !s.warned {
			s.warned = true
			fmt.Fprintf(s.out, "==> Warning: could not report the apply's progress: %v\n", err)
		}
	}
}

// start reports progress every second until the returned function is called,
// which sends a last report.
func (s *applyStream) start(ctx context.Context) func() {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(progressInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				s.flush(ctx)
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		last, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		s.flush(last)
	}
}

// failure is the error to report when the apply exited with an error: the
// first error not tied to a resource, else the first failed resource, else
// the exit error itself.
func (s *applyStream) failure(exitErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.general != "" {
		return errors.New(s.general)
	}
	if len(s.failed) == 0 {
		return exitErr
	}
	first := s.failed[0]
	key := first
	if s.unit != "" {
		key = s.unit + ":" + first
	}
	msg := first + ": " + s.res[key].Errors[0].Summary
	if n := len(s.failed) - 1; n == 1 {
		msg += " (and 1 more resource)"
	} else if n > 1 {
		msg += fmt.Sprintf(" (and %d more resources)", n)
	}
	return errors.New(msg)
}
