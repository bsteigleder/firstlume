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
)

// Masker replaces sensitive values with "***".
type Masker struct {
	replacer *strings.Replacer
}

func NewMasker(secrets []string) *Masker {
	var parts []string
	for _, s := range secrets {
		// Mask multi-line secrets (e.g. private keys) line by line, since output
		// is shipped in lines.
		for _, line := range strings.Split(s, "\n") {
			if line = strings.TrimSpace(line); len(line) >= minSecretLen {
				parts = append(parts, line)
			}
		}
	}
	// Longest first, so a secret that contains another is masked whole.
	sort.Slice(parts, func(i, j int) bool { return len(parts[i]) > len(parts[j]) })
	pairs := make([]string, 0, 2*len(parts))
	for _, p := range parts {
		pairs = append(pairs, p, "***")
	}
	return &Masker{replacer: strings.NewReplacer(pairs...)}
}

func (m *Masker) Mask(s string) string {
	return m.replacer.Replace(s)
}

// LogShipper buffers output and ships complete, masked lines to a sink once per
// second. Shipping whole lines keeps a secret from being split across chunks
// where the masker could not see it.
type LogShipper struct {
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

func (l *LogShipper) flush(final bool) {
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
