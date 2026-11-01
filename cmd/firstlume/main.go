// Command firstlume is a single binary for every component:
//
//	firstlume server   control plane API
//	firstlume agent    polls for work and launches executors
//	firstlume exec     runs one phase of a run (started by the agent)
//	firstlume migrate  applies database migrations
//	firstlume keygen   prints fresh secrets for a new installation
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/bsteigleder/firstlume/internal/agent"
	"github.com/bsteigleder/firstlume/internal/apiclient"
	"github.com/bsteigleder/firstlume/internal/executor"
	"github.com/bsteigleder/firstlume/internal/s3"
	"github.com/bsteigleder/firstlume/internal/secrets"
	"github.com/bsteigleder/firstlume/internal/server"
	"github.com/bsteigleder/firstlume/internal/store"
	"github.com/bsteigleder/firstlume/web"
)

var version = "dev"

const usage = `usage: firstlume <command>

commands:
  server    run the control plane API
  agent     poll for runs and launch executors
  exec      run one phase of a run (started by the agent)
  migrate   apply database migrations and exit
  keygen    print fresh secrets for a new installation
  version   print the version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "server":
		err = runServer(ctx, log)
	case "agent":
		err = runAgent(ctx, log)
	case "exec":
		err = runExec(ctx)
	case "migrate":
		err = runMigrate(ctx, log)
	case "keygen":
		err = runKeygen()
	case "version":
		fmt.Println(version)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		log.Error("fatal", "command", os.Args[1], "err", err)
		os.Exit(1)
	}
}

func runServer(ctx context.Context, log *slog.Logger) error {
	dbURL, err := required("FIRSTLUME_DATABASE_URL")
	if err != nil {
		return err
	}
	keys, err := masterKeys()
	if err != nil {
		return err
	}
	adminToken, err := required("FIRSTLUME_ADMIN_TOKEN")
	if err != nil {
		return err
	}
	if len(adminToken) < 16 {
		return errors.New("FIRSTLUME_ADMIN_TOKEN must be at least 16 characters")
	}
	heartbeat, err := durationEnv("FIRSTLUME_HEARTBEAT_TIMEOUT", 10*time.Minute)
	if err != nil {
		return err
	}
	stateMaxMiB, err := intEnv("FIRSTLUME_STATE_MAX_MIB", 64)
	if err != nil {
		return err
	}
	if stateMaxMiB < 1 {
		return errors.New("FIRSTLUME_STATE_MAX_MIB must be at least 1")
	}

	pool, err := store.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if boolEnv("FIRSTLUME_MIGRATE", true) {
		if err := migrate(ctx, pool, log); err != nil {
			return err
		}
	}

	st := store.New(pool, keys)
	if err := configureStateStorage(ctx, st, log); err != nil {
		return err
	}
	// The bootstrap organization (by slug) owns the bootstrap agent.
	orgID, err := st.EnsureOrganization(ctx, env("FIRSTLUME_ORGANIZATION", "default"))
	if err != nil {
		return fmt.Errorf("bootstrapping organization: %w", err)
	}
	if tok := os.Getenv("FIRSTLUME_BOOTSTRAP_AGENT_TOKEN"); tok != "" {
		name := env("FIRSTLUME_BOOTSTRAP_AGENT_NAME", "default")
		if err := st.EnsureAgent(ctx, orgID, name, tok); err != nil {
			return fmt.Errorf("bootstrapping agent: %w", err)
		}
		log.Info("bootstrap agent registered", "name", name)
	}

	auth, err := authConfig()
	if err != nil {
		return err
	}

	srv := server.New(server.Config{
		Addr:             env("FIRSTLUME_LISTEN_ADDR", ":8080"),
		AdminToken:       adminToken,
		HeartbeatTimeout: heartbeat,
		StateMaxBytes:    int64(stateMaxMiB) << 20,
		UI:               web.FS(),
		PublicURL:        os.Getenv("FIRSTLUME_PUBLIC_URL"),
		Auth:             auth,
	}, st, keys, log)
	return srv.Run(ctx)
}

// authConfig reads how people sign in (spec 0001, R7.12). Setting
// FIRSTLUME_OIDC_ISSUER turns OIDC sign-in on.
func authConfig() (server.AuthConfig, error) {
	cfg := server.AuthConfig{
		PublicURL:       strings.TrimSuffix(os.Getenv("FIRSTLUME_PUBLIC_URL"), "/"),
		AdminTokenLogin: boolEnv("FIRSTLUME_ADMIN_TOKEN_LOGIN", true),
	}
	if cfg.PublicURL != "" {
		u, err := url.Parse(cfg.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return cfg, fmt.Errorf("FIRSTLUME_PUBLIC_URL must be an http(s) URL like https://firstlume.example.com, got %q", cfg.PublicURL)
		}
	}
	issuer := os.Getenv("FIRSTLUME_OIDC_ISSUER")
	if issuer == "" {
		return cfg, nil
	}
	if cfg.PublicURL == "" {
		return cfg, errors.New("FIRSTLUME_PUBLIC_URL is required with FIRSTLUME_OIDC_ISSUER: the provider sends users back to <public URL>/auth/callback")
	}
	for _, k := range []string{"FIRSTLUME_OIDC_CLIENT_ID", "FIRSTLUME_OIDC_CLIENT_SECRET"} {
		if os.Getenv(k) == "" {
			return cfg, fmt.Errorf("%s is required with FIRSTLUME_OIDC_ISSUER", k)
		}
	}
	cfg.OIDC = &server.OIDCConfig{
		ProviderName:  env("FIRSTLUME_OIDC_PROVIDER_NAME", "Gitea"),
		Issuer:        issuer,
		ClientID:      os.Getenv("FIRSTLUME_OIDC_CLIENT_ID"),
		ClientSecret:  os.Getenv("FIRSTLUME_OIDC_CLIENT_SECRET"),
		Scopes:        listEnv("FIRSTLUME_OIDC_SCOPES", "openid profile email groups"),
		GroupsClaim:   env("FIRSTLUME_OIDC_GROUPS_CLAIM", "groups"),
		AllowedGroups: listEnv("FIRSTLUME_OIDC_ALLOWED_GROUPS", ""),
	}
	return cfg, nil
}

// listEnv splits a list separated by commas or spaces.
func listEnv(key, def string) []string {
	return strings.FieldsFunc(env(key, def), func(r rune) bool { return r == ',' || r == ' ' })
}

// configureStateStorage picks where state versions are stored (spec 0002,
// R4). With S3, a server that can't write, read and delete in the bucket
// refuses to start, saying what is wrong.
func configureStateStorage(ctx context.Context, st *store.Store, log *slog.Logger) error {
	keep, err := intEnv("FIRSTLUME_STATE_KEEP_VERSIONS", 0)
	if err != nil {
		return err
	}
	if keep < 0 {
		return errors.New("FIRSTLUME_STATE_KEEP_VERSIONS must be 0 (keep all) or more")
	}
	st.SetStateRetention(keep)

	switch kind := env("FIRSTLUME_STATE_STORAGE", "postgres"); kind {
	case "postgres":
		log.Info("state storage", "kind", "postgres", "keep_versions", keep)
		return nil
	case "s3":
		cfg := s3.Config{
			Endpoint:        os.Getenv("FIRSTLUME_STATE_S3_ENDPOINT"),
			Bucket:          os.Getenv("FIRSTLUME_STATE_S3_BUCKET"),
			Region:          env("FIRSTLUME_STATE_S3_REGION", "garage"),
			Prefix:          os.Getenv("FIRSTLUME_STATE_S3_PREFIX"),
			AccessKeyID:     os.Getenv("FIRSTLUME_STATE_S3_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("FIRSTLUME_STATE_S3_SECRET_ACCESS_KEY"),
			PathStyle:       boolEnv("FIRSTLUME_STATE_S3_PATH_STYLE", true),
		}
		client, err := s3.New(cfg)
		if err != nil {
			return fmt.Errorf("state storage: %w", err)
		}
		checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if err := client.Check(checkCtx); err != nil {
			return err
		}
		st.SetStateBlobs(client)
		log.Info("state storage", "kind", "s3", "endpoint", cfg.Endpoint, "bucket", cfg.Bucket, "prefix", cfg.Prefix, "keep_versions", keep)
		return nil
	default:
		return fmt.Errorf("FIRSTLUME_STATE_STORAGE must be postgres or s3, got %q", kind)
	}
}

func runMigrate(ctx context.Context, log *slog.Logger) error {
	dbURL, err := required("FIRSTLUME_DATABASE_URL")
	if err != nil {
		return err
	}
	pool, err := store.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	return migrate(ctx, pool, log)
}

func migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	results, err := store.Migrate(ctx, pool)
	if err != nil {
		return fmt.Errorf("migrating database: %w", err)
	}
	for _, r := range results {
		log.Info("migration applied", "version", r.Source.Version, "duration", r.Duration)
	}
	return nil
}

func runAgent(ctx context.Context, log *slog.Logger) error {
	apiURL, err := required("FIRSTLUME_API_URL")
	if err != nil {
		return err
	}
	token, err := required("FIRSTLUME_AGENT_TOKEN")
	if err != nil {
		return err
	}
	concurrency, err := intEnv("FIRSTLUME_CONCURRENCY", 4)
	if err != nil {
		return err
	}
	poll, err := durationEnv("FIRSTLUME_POLL_INTERVAL", 5*time.Second)
	if err != nil {
		return err
	}
	executorAPI := env("FIRSTLUME_EXECUTOR_API_URL", apiURL)

	var driver agent.Driver
	switch d := env("FIRSTLUME_DRIVER", "kubernetes"); d {
	case "kubernetes":
		if driver, err = kubernetesDriver(executorAPI); err != nil {
			return err
		}
	case "local":
		bin, err := os.Executable()
		if err != nil {
			return err
		}
		cacheDir, _ := os.UserCacheDir()
		driver = &agent.LocalDriver{
			Binary:        bin,
			ExecutorAPI:   executorAPI,
			WorkDir:       env("FIRSTLUME_LOCAL_WORK_DIR", filepath.Join(os.TempDir(), "firstlume", "runs")),
			ToolsDir:      env("FIRSTLUME_TOOLS_DIR", filepath.Join(cacheDir, "firstlume", "tools")),
			Output:        os.Stderr,
			KeepWorkspace: boolEnv("FIRSTLUME_KEEP_WORKSPACE", false),
		}
		log.Warn("using the local driver: executors run unisolated on this host")
	default:
		return fmt.Errorf("unknown FIRSTLUME_DRIVER %q (want kubernetes or local)", d)
	}

	a := agent.New(agent.Config{Concurrency: concurrency, PollInterval: poll}, apiclient.New(apiURL, token), driver, log)
	return a.Run(ctx)
}

func kubernetesDriver(executorAPI string) (agent.Driver, error) {
	image, err := required("FIRSTLUME_EXECUTOR_IMAGE")
	if err != nil {
		return nil, err
	}
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
	restCfg, err := loader.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubernetes config: %w", err)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	namespace := os.Getenv("FIRSTLUME_JOB_NAMESPACE")
	if namespace == "" {
		if namespace, _, err = loader.Namespace(); err != nil {
			return nil, err
		}
	}
	resources, err := agent.ParseResources(
		os.Getenv("FIRSTLUME_EXECUTOR_CPU_REQUEST"), os.Getenv("FIRSTLUME_EXECUTOR_MEMORY_REQUEST"),
		os.Getenv("FIRSTLUME_EXECUTOR_CPU_LIMIT"), os.Getenv("FIRSTLUME_EXECUTOR_MEMORY_LIMIT"))
	if err != nil {
		return nil, err
	}
	deadline, err := durationEnv("FIRSTLUME_JOB_DEADLINE", 2*time.Hour)
	if err != nil {
		return nil, err
	}
	ttl, err := durationEnv("FIRSTLUME_JOB_TTL", time.Hour)
	if err != nil {
		return nil, err
	}
	nodeSelector, tolerations, affinity, err := agent.ParseScheduling(os.Getenv("FIRSTLUME_EXECUTOR_NODE_SELECTOR"),
		os.Getenv("FIRSTLUME_EXECUTOR_TOLERATIONS"), os.Getenv("FIRSTLUME_EXECUTOR_AFFINITY"))
	if err != nil {
		return nil, err
	}
	return agent.NewKubernetesDriver(agent.KubernetesConfig{
		Namespace:        namespace,
		AgentName:        env("FIRSTLUME_AGENT_NAME", "default"),
		Image:            image,
		ImagePullPolicy:  corev1.PullPolicy(env("FIRSTLUME_EXECUTOR_IMAGE_PULL_POLICY", string(corev1.PullIfNotPresent))),
		ServiceAccount:   os.Getenv("FIRSTLUME_EXECUTOR_SERVICE_ACCOUNT"),
		AutomountToken:   boolEnv("FIRSTLUME_EXECUTOR_AUTOMOUNT_TOKEN", false),
		ExecutorAPI:      executorAPI,
		Resources:        resources,
		ActiveDeadline:   deadline,
		TTLAfterFinished: ttl,
		NodeSelector:     nodeSelector,
		Tolerations:      tolerations,
		Affinity:         affinity,
	}, client), nil
}

func runExec(ctx context.Context) error {
	apiURL, err := required("FIRSTLUME_API_URL")
	if err != nil {
		return err
	}
	token, err := required("FIRSTLUME_RUN_TOKEN")
	if err != nil {
		return err
	}
	workDir := env("FIRSTLUME_WORK_DIR", "/workspace/run")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return err
	}
	return executor.Run(ctx, executor.Config{
		APIURL:   apiURL,
		Token:    token,
		WorkDir:  workDir,
		ToolsDir: env("FIRSTLUME_TOOLS_DIR", filepath.Join(workDir, "tools")),
		Local:    os.Stdout,
	})
}

func runKeygen() error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	admin, err := secrets.RandomToken("fl_admin_")
	if err != nil {
		return err
	}
	agentTok, err := secrets.RandomToken("fla_")
	if err != nil {
		return err
	}
	fmt.Printf("FIRSTLUME_SECRET_KEY=%s\nFIRSTLUME_ADMIN_TOKEN=%s\nFIRSTLUME_BOOTSTRAP_AGENT_TOKEN=%s\n",
		base64.StdEncoding.EncodeToString(key), admin, agentTok)
	return nil
}

func masterKeys() (*secrets.Keys, error) {
	raw, err := required("FIRSTLUME_SECRET_KEY")
	if err != nil {
		return nil, err
	}
	keys, err := secrets.ParseMasterKey(raw)
	if err != nil {
		return nil, fmt.Errorf("FIRSTLUME_SECRET_KEY: %w", err)
	}
	return keys, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func required(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

func boolEnv(key string, def bool) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return def
	}
	return v
}

func intEnv(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func durationEnv(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func logLevel() slog.Level {
	switch strings.ToLower(os.Getenv("FIRSTLUME_LOG_LEVEL")) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}
