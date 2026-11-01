package executor

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// tool describes where to download a pinned binary and its checksums.
type tool struct {
	name    string // binary name on disk
	url     string
	sumsURL string
	asset   string // file name as listed in the checksums file
	zipped  bool
}

func toolFor(name, version, goos, goarch string) (tool, error) {
	switch name {
	case "terraform":
		asset := fmt.Sprintf("terraform_%s_%s_%s.zip", version, goos, goarch)
		base := fmt.Sprintf("https://releases.hashicorp.com/terraform/%s/", version)
		return tool{name: "terraform", url: base + asset, sumsURL: base + fmt.Sprintf("terraform_%s_SHA256SUMS", version), asset: asset, zipped: true}, nil
	case "opentofu":
		asset := fmt.Sprintf("tofu_%s_%s_%s.zip", version, goos, goarch)
		base := fmt.Sprintf("https://github.com/opentofu/opentofu/releases/download/v%s/", version)
		return tool{name: "tofu", url: base + asset, sumsURL: base + fmt.Sprintf("tofu_%s_SHA256SUMS", version), asset: asset, zipped: true}, nil
	case "terragrunt":
		asset := fmt.Sprintf("terragrunt_%s_%s", goos, goarch)
		base := fmt.Sprintf("https://github.com/gruntwork-io/terragrunt/releases/download/v%s/", version)
		return tool{name: "terragrunt", url: base + asset, sumsURL: base + "SHA256SUMS", asset: asset}, nil
	}
	return tool{}, fmt.Errorf("unknown tool %q", name)
}

// Installer downloads pinned tool versions into a cache directory, verifying
// each download against the release's published SHA256 checksums.
type Installer struct {
	Dir  string
	HTTP *http.Client
	Log  io.Writer
	// Backoff is the wait before each retry of a download; nil uses
	// downloadBackoff.
	Backoff []time.Duration
}

// downloadBackoff spaces the retries of a download. Some clusters lose
// connections to GitHub's release servers now and then (a reset mid-TLS), and
// one lost connection must not fail the run.
var downloadBackoff = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second}

// Install returns the path to the binary, downloading it if it is not cached.
func (in *Installer) Install(ctx context.Context, name, version string) (string, error) {
	t, err := toolFor(name, version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(in.Dir, name, version)
	bin := filepath.Join(dir, t.name)
	if info, err := os.Stat(bin); err == nil && info.Mode().IsRegular() {
		fmt.Fprintf(in.Log, "==> Using cached %s %s\n", name, version)
		return bin, nil
	}

	fmt.Fprintf(in.Log, "==> Installing %s %s\n", name, version)
	sums, err := in.get(ctx, t.sumsURL)
	if err != nil {
		return "", fmt.Errorf("downloading checksums: %w", err)
	}
	want, err := findChecksum(sums, t.asset)
	if err != nil {
		return "", err
	}
	data, err := in.get(ctx, t.url)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", t.asset, err)
	}
	if got := sha256.Sum256(data); hex.EncodeToString(got[:]) != want {
		return "", fmt.Errorf("checksum mismatch for %s", t.asset)
	}
	if t.zipped {
		if data, err = unzipFile(data, t.name); err != nil {
			return "", err
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	// Rename is atomic, so concurrent executors sharing a cache never see a partial binary.
	if err := os.Rename(tmp.Name(), bin); err != nil {
		return "", err
	}
	return bin, nil
}

// get downloads url, retrying network errors and answers that may be
// temporary (429 and 5xx).
func (in *Installer) get(ctx context.Context, url string) ([]byte, error) {
	backoff := in.Backoff
	if backoff == nil {
		backoff = downloadBackoff
	}
	for attempt := 0; ; attempt++ {
		data, retry, err := in.getOnce(ctx, url)
		if err == nil || !retry || attempt == len(backoff) || ctx.Err() != nil {
			return data, err
		}
		fmt.Fprintf(in.Log, "==> %v; retrying in %s\n", err, backoff[attempt])
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff[attempt]):
		}
	}
}

// getOnce makes one attempt and says whether a failure is worth retrying.
func (in *Installer) getOnce(ctx context.Context, url string) ([]byte, bool, error) {
	client := in.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, retry, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if err != nil {
		return nil, true, err
	}
	return data, false, nil
}

func findChecksum(sums []byte, asset string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("no checksum published for %s", asset)
}

func unzipFile(data []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, 1<<30))
	}
	return nil, fmt.Errorf("%s not found in archive", name)
}
