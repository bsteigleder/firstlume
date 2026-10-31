package executor

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	flushInterval   = time.Second
	maxBufferedLine = 256 << 10
	minSecretLen    = 3
	// minSecretLineLen is the shortest line of a multi-line secret that is masked.
	minSecretLineLen = 6
)

// Masker replaces sensitive values with "***". Secrets found during the
// phase, like the content of generated files, can be added later.
type Masker struct {
	mu       sync.RWMutex
	parts    []string
	replacer *strings.Replacer
}

func NewMasker(secrets []string) *Masker {
	m := &Masker{}
	m.Add(secrets...)
	return m
}

// Add masks more secrets from now on. A multi-line secret (e.g. a private
// key) is masked line by line, since output is shipped in lines; its lines
// shorter than minSecretLineLen are left alone, so the log stays readable.
func (m *Masker) Add(secrets ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range secrets {
		lines := strings.Split(s, "\n")
		min := minSecretLen
		if len(lines) > 1 {
			min = minSecretLineLen
		}
		for _, line := range lines {
			if line = strings.TrimSpace(line); len(line) >= min {
				m.parts = append(m.parts, line)
			}
		}
	}
	// Longest first, so a secret that contains another is masked whole.
	sort.Slice(m.parts, func(i, j int) bool { return len(m.parts[i]) > len(m.parts[j]) })
	pairs := make([]string, 0, 2*len(m.parts))
	for _, p := range m.parts {
		pairs = append(pairs, p, "***")
	}
	m.replacer = strings.NewReplacer(pairs...)
}

func (m *Masker) Mask(s string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.replacer.Replace(s)
}

// LogShipper buffers output and ships complete, masked lines to a sink once per
// second. Shipping whole lines keeps a secret from being split across chunks
// where the masker could not see it.
type LogShipper struct {
	// sendMu keeps chunks in order between the background loop and Flush.
	sendMu sync.Mutex
	mu     sync.Mutex
	buf    bytes.Buffer
	masker *Masker
	send   func(ctx context.Context, data []byte) error
	local  io.Writer
	err    error

	done chan struct{}
	wg   sync.WaitGroup
}

func NewLogShipper(masker *Masker, local io.Writer, send func(ctx context.Context, data []byte) error) *LogShipper {
	l := &LogShipper{masker: masker, local: local, send: send, done: make(chan struct{})}
	l.wg.Add(1)
	go l.loop()
	return l
}

func (l *LogShipper) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	return len(p), nil
}

func (l *LogShipper) loop() {
	defer l.wg.Done()
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-t.C:
			l.flush(false)
		}
	}
}

// take removes what is ready to ship: complete lines, or everything when final
// or when a single line grows unreasonably long.
func (l *LogShipper) take(final bool) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	data := l.buf.Bytes()
	n := len(data)
	if !final && n < maxBufferedLine {
		n = bytes.LastIndexByte(data, '\n') + 1
	}
	if n == 0 {
		return ""
	}
	out := string(data[:n])
	l.buf.Next(n)
	return out
}

// Flush ships everything written so far, including an unfinished line.
func (l *LogShipper) Flush() {
	l.flush(true)
}

func (l *LogShipper) flush(final bool) {
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	chunk := l.take(final)
	if chunk == "" {
		return
	}
	masked := l.masker.Mask(chunk)
	if l.local != nil {
		_, _ = io.WriteString(l.local, masked)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := l.send(ctx, []byte(masked)); err != nil {
		l.mu.Lock()
		if l.err == nil {
			l.err = err
		}
		l.mu.Unlock()
	}
}

// Err returns the first error from the sink, if any.
func (l *LogShipper) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// Close stops the background loop and ships whatever is left.
func (l *LogShipper) Close() error {
	close(l.done)
	l.wg.Wait()
	l.flush(true)
	return l.Err()
}
