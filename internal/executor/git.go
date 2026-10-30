package executor

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// gitEnv returns environment entries that make git non-interactive, restrict
// transports, and (for HTTPS) authenticate with the template's token. The token
// is passed through GIT_CONFIG_* so it never appears in argv, and child git
// processes (e.g. Terraform fetching private modules) inherit it.
func gitEnv(token string) []string {
	env := []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=https:ssh:file",
	}
	if token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+basic,
		)
	}
	return env
}

type git struct {
	dir string
	env []string
	out io.Writer
}

func (g *git) run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.dir
	cmd.Env = g.env
	cmd.Stdout = g.out
	cmd.Stderr = g.out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	return nil
}

func (g *git) output(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.dir
	cmd.Env = g.env
	cmd.Stderr = g.out
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// checkout fetches a single ref (or commit) shallowly into dir and returns the
// checked-out commit SHA.
func checkout(ctx context.Context, dir, repo, ref, commit string, env []string, out io.Writer) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	g := &git{dir: dir, env: env, out: out}
	if err := g.run(ctx, "init", "--quiet"); err != nil {
		return "", err
	}
	if err := g.run(ctx, "remote", "add", "--", "origin", repo); err != nil {
		return "", err
	}

	target := ref
	if commit != "" {
		target = commit
	}
	if err := g.run(ctx, "fetch", "--quiet", "--depth=1", "--no-tags", "--", "origin", target); err != nil {
		if commit == "" {
			return "", err
		}
		// Some servers refuse fetching a bare SHA; fetch the ref's history instead.
		fmt.Fprintf(out, "==> Fetching %s by SHA failed, fetching %s history\n", short(commit), ref)
		if err := g.run(ctx, "fetch", "--quiet", "--no-tags", "--", "origin", ref); err != nil {
			return "", err
		}
		if err := g.run(ctx, "checkout", "--quiet", "--detach", commit); err != nil {
			return "", err
		}
	} else if err := g.run(ctx, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return "", err
	}

	sha, err := g.output(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	if commit != "" && sha != commit {
		return "", fmt.Errorf("checked out %s, expected %s", sha, commit)
	}
	return sha, nil
}

// commitInfo describes the checked-out commit: author name, author email and subject line.
func commitInfo(ctx context.Context, dir string, env []string) (name, email, subject string, err error) {
	g := &git{dir: dir, env: env, out: io.Discard}
	out, err := g.output(ctx, "log", "-1", "--format=%an%x00%ae%x00%s", "HEAD")
	if err != nil {
		return "", "", "", fmt.Errorf("git log: %w", err)
	}
	parts := strings.SplitN(out, "\x00", 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], parts[2], nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
