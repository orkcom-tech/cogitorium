// Package config loads server configuration with precedence:
// flags > environment (COGITORIUM_*) > config file > defaults.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/orkcom-tech/cogitorium/internal/gearnet"
	"github.com/orkcom-tech/cogitorium/internal/secrets"
)

// DefaultBrowserImage carries a real browser and the driver that speaks to it.
//
// Microsoft's Playwright image rather than one built here, because the awkward
// half of running a browser in a container is the system libraries and the
// matching browser build, and that image is maintained by the people who ship
// the driver. Pinned to a version: an image that followed a moving tag would
// change what an approved gear runs inside without the approval changing.
const DefaultBrowserImage = "mcr.microsoft.com/playwright:v1.56.0-noble"

type Config struct {
	// Listen is the HTTP listen address, e.g. "127.0.0.1:8688".
	Listen string `yaml:"listen"`
	// DataDir holds the SQLite database and everything the server owns on disk.
	DataDir string `yaml:"data_dir"`
	// LogLevel is one of debug|info|warn|error.
	LogLevel string `yaml:"log_level"`
	// ContextdPath is the contextd binary (Contextverse CLI) used for the
	// context layer. Default: "contextd" resolved from PATH.
	ContextdPath string `yaml:"contextd_path"`
	// Sandbox selects how gears and the terminal execute: "docker" isolates
	// them from the server's files, "kubernetes" runs each as a Job, and
	// "subprocess" does not isolate them at all. Default "auto" uses Docker
	// when it answers and says so plainly when it does not — it never selects
	// "kubernetes", which is a deliberate deployment rather than a guess.
	Sandbox string `yaml:"sandbox"`
	// SandboxImage is the container image gears run in.
	SandboxImage string `yaml:"sandbox_image"`
	// SandboxRuntime is the OCI runtime the Docker daemon should use for
	// gear containers — empty for its default, or a name it has been
	// configured with: "runsc" for gVisor, "kata-runtime" for Kata.
	//
	// Cogitorium does not install or configure these. It selects one and
	// refuses at startup if the daemon does not have it, which is the honest
	// boundary — the isolation belongs to the runtime, not to this product.
	SandboxRuntime string `yaml:"sandbox_runtime"`
	// BrowserImage is the container a gear granted the "browser" environment
	// runs in — an image carrying a real browser, so a gear can drive one and
	// hand back what it saw as ordinary run artifacts.
	//
	// Configurable rather than fixed, and not pulled at startup: it is large,
	// most installs never grant it, and paying for it on every start would be
	// a minute of every boot spent on a capability nobody used.
	BrowserImage string `yaml:"browser_image"`
	// SandboxPool keeps this many warm containers per image instead of
	// creating and destroying one per gear run. Zero — the default — is off.
	//
	// It is the one setting here that trades isolation for latency, so it is
	// off unless asked for. A pooled container has a history: whatever a
	// previous run left outside its payload is still there. Runs that were
	// given named values or the network are never pooled, and the payload is
	// destroyed between runs, but /tmp is shared and that is the trade.
	SandboxPool int `yaml:"sandbox_pool"`
	// MCPClients lets an operator install external MCP servers and grant their
	// tools to an agent. Off by default, and the default is the point.
	//
	// Everything else this product executes is either its own code or a gear
	// whose complete source is in this install, versioned, approved line by
	// line, and run in a container. An external MCP server is a command: the
	// source is never seen, and in this first cut the child runs on the host as
	// this server's user, so an approved one can read the database and the
	// provider keys in it. Every install, approval and grant is admin-only and
	// no agent can reach any of them, but that is policy rather than isolation.
	MCPClients bool `yaml:"mcp_clients"`
	// KubeNamespace, KubeClaim and KubeNode configure the "kubernetes"
	// sandbox, where a gear runs as a Job rather than a container this
	// process creates.
	//
	// Only the claim has to be supplied: it names the volume the data
	// directory is on, and a gear Job mounts that same claim at the run
	// directory's own subPath so it sees its payload and nothing else.
	// The namespace defaults to the pod's own, and the node comes from the
	// downward API — a ReadWriteOnce volume attaches to one node, so a Job
	// scheduled elsewhere would wait forever on a volume it cannot have.
	KubeNamespace string `yaml:"kube_namespace"`
	KubeClaim     string `yaml:"kube_claim"`
	KubeNode      string `yaml:"kube_node"`
	// KubeCPU and KubeMemory bound one gear Job. Empty means the cluster's
	// own defaults, which is usually a LimitRange or nothing at all.
	KubeCPU    string `yaml:"kube_cpu"`
	KubeMemory string `yaml:"kube_memory"`
	// Terminal opens a shell in the UI. Off by default: it is interactive
	// code execution over HTTP, so switching it on is a deliberate act. It
	// also requires a sandbox — without one the request is refused rather
	// than served with the server's own file access.
	Terminal bool `yaml:"terminal"`

	// Egress is the master switch for agents reaching the internet. Off by
	// default, and deliberately reachable ONLY from this file and the
	// environment: there is no route, no setter and no database row, so no
	// agent and no tool call can flip it. Turning it on means editing a file
	// on the operator's own disk and restarting the server.
	//
	// It grants nothing on its own. An agent must additionally hold a grant
	// an operator drew on the blueprint, and every individual search still
	// stops the turn and waits for a person to approve that exact query.
	Egress bool `yaml:"egress"`

	// EgressKey is the credential for the search destination. It travels in a
	// header, never in a query string. Empty with Egress on is a startup
	// error, not a silent degradation to unauthenticated requests.
	EgressKey string `yaml:"egress_key"`

	// EgressApprovalBearer requires a real bearer token to grant the gate or
	// approve a search, refusing the implicit-admin that loopback otherwise
	// confers. Off by default because it would make the feature unusable on a
	// default single-operator install; the audit records which kind of
	// authentication each decision actually had, so a row is never mistaken
	// for stronger evidence than it is.
	EgressApprovalBearer bool `yaml:"egress_approval_bearer"`

	// VariablesDir and SecretsDir are the directory source for the names a gear
	// is given: one file per name, the file's contents being the value. Empty
	// means this install has no such source, which is the ordinary case on a
	// laptop.
	//
	// Two directories rather than one because a ConfigMap and a Secret are two
	// mounts, and the difference is not cosmetic: a value from SecretsDir is
	// redacted everywhere it could otherwise surface, and a value from
	// VariablesDir is shown.
	//
	// These are paths, not values, so unlike SecretKey they belong in the
	// config file — on Kubernetes the mount path is exactly the sort of thing a
	// ConfigMap should say.
	VariablesDir string `yaml:"variables_dir"`
	SecretsDir   string `yaml:"secrets_dir"`

	// QueueWorkers is how many queued deliveries may run at once ACROSS
	// workspaces. It is not the ceiling that matters — one run per workspace
	// already is — so this is about how many different workspaces can be busy
	// at the same moment, and every one of them is mostly waiting on a model.
	QueueWorkers int `yaml:"queue_workers"`

	// QueueMaxPerWorkspace bounds what may be WAITING for one workspace.
	//
	// A queue with no bound is a polite way to run a server out of disk: every
	// waiting file delivery has already landed its bytes. Past this a delivery
	// is refused with 429 and told how many are ahead of it — which is
	// backpressure, and is a different thing from the data loss it replaced,
	// where a busy workspace destroyed the request outright.
	QueueMaxPerWorkspace int `yaml:"queue_max_per_workspace"`

	// CallbackHosts is who a task may tell that its run finished, by hostname.
	// EMPTY MEANS CALLBACKS ARE OFF — not that every host is allowed. A
	// callback URL arrives in a task, and a task is editable by anyone who can
	// reach the workspace, so an open default would turn editing a task into
	// making this server call an address of somebody else's choosing.
	CallbackHosts []string `yaml:"callback_hosts"`

	// BudgetRunTokens is the most ONE RUN may spend before it is stopped. Zero
	// is off, and off is the default.
	//
	// It exists for the door, not for the operator. An inlet is an entrance for
	// somebody else's system, and whoever holds the key can drive deliveries —
	// so this bounds what a THIRD PARTY can cost, which is a different thing
	// from an operator limiting themselves. There is no daily or workspace-wide
	// version for exactly that reason: nothing but the operator's own schedules
	// and their own typing drives a workspace's total, and capping that would
	// be a knob whose only use is to stop your own work.
	//
	// It REFUSES rather than reports, and a run stopped by it settles as
	// refused_budget rather than failed — a caller outside must be able to tell
	// "your job hit the ceiling" from "we broke", or it retries the one thing
	// that must not be retried.
	BudgetRunTokens int64 `yaml:"budget_run_tokens"`

	// PublicURL is how this install is reached from outside. It is used only to
	// put fetchable links to a run's files into its callback; empty leaves them
	// out, and nothing else depends on it.
	PublicURL string `yaml:"public_url"`

	// GearProxyListen is where the outward gate for gears listens: the proxy a
	// gear the operator granted the network reaches it through, and which
	// records every connection.
	//
	// Empty means the server works it out: it asks Docker which address a
	// container reaches this machine on and binds there if it can, falling back
	// to the loopback. That covers the case this used to leave to the operator
	// — on Linux host.docker.internal is the bridge gateway rather than the
	// host's loopback, so a gate on 127.0.0.1 is unreachable from every
	// container, and the grant silently did nothing on the platform servers run
	// on. See gearnet.ListenFor.
	//
	// Set it to name an address yourself, in which case it is used exactly as
	// given and a gate that cannot bind there is a startup failure rather than
	// something quietly worked around. Port 0 means the kernel picks, which is
	// what a gate nobody has to firewall wants.
	GearProxyListen string `yaml:"gear_proxy_listen"`

	// SecretKey encrypts the secrets held in this install's own database.
	//
	// Deliberately has NO yaml tag, for the same reason AdminToken has none:
	// on Kubernetes the config file is a ConfigMap, and a ConfigMap is not a
	// secret. A key sitting in the same place as the ciphertext it opens
	// protects nothing at all.
	//
	// Empty is a working install: variables work, and a secret mounted from
	// SecretsDir works, because neither is stored here. Only writing a secret
	// INTO the database is refused — with a message saying so — because the
	// alternative is writing a credential to disk in plaintext.
	SecretKey string `yaml:"-"`

	// AdminToken seeds the first admin's token instead of generating one.
	//
	// Deliberately has NO yaml tag: it is readable from the environment and
	// nowhere else. That is not a stylistic choice — on Kubernetes the config
	// file is a ConfigMap, and a ConfigMap is not a secret. Leaving this out of
	// the file means it cannot be put there by mistake; it comes from a Secret
	// as an environment variable or it does not come at all.
	//
	// Without it the server generates a token and PRINTS it once, which is
	// correct on a laptop and wrong in a cluster, where "printed once" means
	// "in the pod log, for anyone who can read logs". With it, nothing
	// sensitive is ever written to the log.
	AdminToken string `yaml:"-"`
}

func Defaults() Config {
	home, err := os.UserHomeDir()
	if err != nil {
		// No home dir (e.g. bare container) — fall back to CWD-relative data.
		home = "."
	}
	return Config{
		Listen:          "127.0.0.1:8688",
		DataDir:         filepath.Join(home, ".cogitorium"),
		LogLevel:        "info",
		ContextdPath:    "contextd",
		Sandbox:         "auto",
		BrowserImage:    DefaultBrowserImage,
		GearProxyListen: gearnet.DefaultListen,
		QueueWorkers:    4,
		// Fifty is a burst, not a backlog. It is large enough that an ordinary
		// spike waits rather than being refused, and small enough that fifty
		// file deliveries' bytes are a size an operator can reason about.
		QueueMaxPerWorkspace: 50,
	}
}

// Load builds the effective config. path is the --config flag value; empty
// means: use $COGITORIUM_CONFIG if set, else <data-dir>/config.yaml if it
// exists, else the file layer is skipped. dataDirOverride is the --data flag
// value (empty if not given) so the default config probe honors the
// effective data dir, not just the built-in default.
func Load(path, dataDirOverride string) (Config, error) {
	cfg := Defaults()

	probeDir := cfg.DataDir
	if v := os.Getenv("COGITORIUM_DATA_DIR"); v != "" {
		probeDir = v
	}
	if dataDirOverride != "" {
		probeDir = dataDirOverride
	}

	explicit := path != ""
	if !explicit {
		if env := os.Getenv("COGITORIUM_CONFIG"); env != "" {
			path, explicit = env, true
		} else {
			path = filepath.Join(probeDir, "config.yaml")
		}
	}

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", path, err)
		}
		slog.Info("config file loaded", "path", path)
	case os.IsNotExist(err) && !explicit:
		slog.Debug("no config file, using defaults", "path", path)
	default:
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}

	if v := os.Getenv("COGITORIUM_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("COGITORIUM_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("COGITORIUM_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("COGITORIUM_CONTEXTD"); v != "" {
		cfg.ContextdPath = v
	}
	if v := os.Getenv("COGITORIUM_SANDBOX"); v != "" {
		cfg.Sandbox = v
	}
	if v := os.Getenv("COGITORIUM_SANDBOX_IMAGE"); v != "" {
		cfg.SandboxImage = v
	}
	if v := os.Getenv("COGITORIUM_SANDBOX_RUNTIME"); v != "" {
		cfg.SandboxRuntime = v
	}
	if v := os.Getenv("COGITORIUM_BROWSER_IMAGE"); v != "" {
		cfg.BrowserImage = v
	}
	if v := os.Getenv("COGITORIUM_MCP_CLIENTS"); v != "" {
		cfg.MCPClients = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("COGITORIUM_SANDBOX_POOL"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return cfg, fmt.Errorf("COGITORIUM_SANDBOX_POOL must be a number of containers to keep warm, "+
				"or 0 for none (got %q)", v)
		}
		cfg.SandboxPool = n
	}
	// The claim and the node come from the chart rather than from a file: one
	// is a Helm release's own name and the other is the downward API, and
	// neither is knowable when the ConfigMap is written.
	if v := os.Getenv("COGITORIUM_KUBE_NAMESPACE"); v != "" {
		cfg.KubeNamespace = v
	}
	if v := os.Getenv("COGITORIUM_KUBE_CLAIM"); v != "" {
		cfg.KubeClaim = v
	}
	if v := os.Getenv("COGITORIUM_KUBE_NODE"); v != "" {
		cfg.KubeNode = v
	}
	if v := os.Getenv("COGITORIUM_KUBE_CPU"); v != "" {
		cfg.KubeCPU = v
	}
	if v := os.Getenv("COGITORIUM_KUBE_MEMORY"); v != "" {
		cfg.KubeMemory = v
	}
	if v := os.Getenv("COGITORIUM_TERMINAL"); v != "" {
		cfg.Terminal = v == "1" || strings.EqualFold(v, "true")
	}
	// Same strict parse as Terminal, so COGITORIUM_EGRESS=0 is a working
	// off-switch over a file that says true.
	if v := os.Getenv("COGITORIUM_EGRESS"); v != "" {
		cfg.Egress = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("COGITORIUM_EGRESS_KEY"); v != "" {
		cfg.EgressKey = v
	}
	if v := os.Getenv("COGITORIUM_EGRESS_APPROVAL_BEARER"); v != "" {
		cfg.EgressApprovalBearer = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("COGITORIUM_VARIABLES_DIR"); v != "" {
		cfg.VariablesDir = v
	}
	if v := os.Getenv("COGITORIUM_SECRETS_DIR"); v != "" {
		cfg.SecretsDir = v
	}
	if v := os.Getenv("COGITORIUM_GEAR_PROXY_LISTEN"); v != "" {
		cfg.GearProxyListen = v
	}
	// Environment only — see the field. Length is checked here rather than at
	// first use, so an operator who typed a short key learns at startup instead
	// of when a gear finally needs a credential.
	if v := os.Getenv("COGITORIUM_SECRET_KEY"); v != "" {
		if len(v) < secrets.MinSecretKeyLen {
			return Config{}, fmt.Errorf("COGITORIUM_SECRET_KEY is %d characters; it encrypts every secret in the database, so at least %d are required",
				len(v), secrets.MinSecretKeyLen)
		}
		cfg.SecretKey = v
	}
	// Environment only — see the field. A short one is refused rather than
	// accepted quietly: a seeded admin token is the whole front door, and
	// "admin" as a token would be worse than the generated one it replaced.
	if v := os.Getenv("COGITORIUM_ADMIN_TOKEN"); v != "" {
		if len(v) < MinAdminTokenLen {
			return Config{}, fmt.Errorf("COGITORIUM_ADMIN_TOKEN is %d characters; it seeds the admin's credential, so at least %d are required",
				len(v), MinAdminTokenLen)
		}
		cfg.AdminToken = v
	}
	return cfg, nil
}

// MinAdminTokenLen is the floor for a seeded admin token. Generated tokens are
// far longer; this exists so that a seeded one cannot be trivially guessable.
const MinAdminTokenLen = 24

// SlogLevel maps LogLevel to a slog.Level, defaulting to info on garbage.
func (c Config) SlogLevel() slog.Level {
	switch c.LogLevel {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "info":
		return slog.LevelInfo
	default:
		slog.Warn("unknown log_level, using info", "log_level", c.LogLevel)
		return slog.LevelInfo
	}
}
