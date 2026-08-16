package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Kube runs a gear as a Kubernetes Job.
//
// It exists because the Docker backend cannot work in a cluster — there is no
// daemon inside a pod — and the fallback was a subprocess of the server, which
// means agent-authored code holding the server's own file access: the SQLite
// database and every provider key in it. That is the whole reason this file is
// here, and it is why the chart refused to enable the terminal or the outward
// gate at all until it existed.
//
// # How the payload reaches the Job
//
// Not by copying it. The server's data directory is a volume, a gear's run
// directory is a directory on that volume, and the Job mounts the SAME claim
// with `subPath` set to exactly that run directory. So the Job sees its own
// payload at /work and NOTHING else on the volume — not the database, not
// another gear's directory, not another workspace's files. The isolation is the
// mount, which the kubelet enforces before the container starts, rather than
// anything the container is trusted to respect.
//
// It also means the results need no collection step: the gear writes out/ and
// the server is already looking at that directory.
//
// A ReadWriteOnce claim can be mounted by several pods on ONE node, which is
// why the Job is pinned to the server's own node. Without that pin a Job
// scheduled elsewhere would sit Pending forever on a volume it cannot attach,
// and the gear would look like a timeout.
//
// # Talking to the API
//
// Plain HTTP with the pod's own service account, rather than client-go or a
// kubectl binary. Three verbs are needed — create a Job, read its status,
// delete it — and a dependency the size of client-go to spell them is not a
// trade this project makes. The in-cluster contract (token, CA and namespace at
// a fixed path) is stable and is what every client library reads anyway.
type Kube struct {
	// Image is the container gears run in. The same value the Docker backend
	// uses, so a gear behaves the same in both.
	Image string
	// Namespace is where Jobs are created — the server's own.
	Namespace string
	// Claim is the PersistentVolumeClaim holding the data directory.
	Claim string
	// Node is the node the server runs on. Jobs are pinned to it because a
	// ReadWriteOnce volume attaches to one node at a time.
	Node string
	// DataDir is the server's data directory, and the root the run directory
	// is made relative to. A Spec.Dir outside it cannot be mounted and is
	// refused rather than silently run against an empty directory.
	DataDir string
	// UID is who the gear runs as. It has to be the identity that owns the
	// payload on the volume, or the container cannot read its own code.
	UID int64
	// CPU and Memory bound one gear. Empty means unbounded, which is the
	// cluster's default and is stated here rather than assumed.
	CPU, Memory string

	host   string
	token  string
	client *http.Client
}

// The control files, kept in one hidden directory so a gear's own listing is
// not littered with them and out/ collection cannot pick them up.
const (
	kubeCtl    = ".cogitorium"
	kubeMount  = "/work"
	kubeCtlDir = kubeMount + "/" + kubeCtl
)

const inClusterDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// NewKube builds the backend from the pod's own service account, or explains
// what is missing.
//
// Everything it needs is either mounted by the kubelet or supplied by the
// chart, so a wrong value here is a startup failure rather than a gear that
// fails at three in the morning with a message about a volume.
func NewKube(image, dataDir, namespace, claim, node string) (*Kube, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("sandbox is \"kubernetes\" but this process is not running in a cluster: " +
			"KUBERNETES_SERVICE_HOST is not set. That mode is for the in-cluster deployment; off-cluster use " +
			"sandbox: docker")
	}
	// Before any of the mounted files: this one is knowable from the
	// configuration alone, and reporting a missing token first would send an
	// operator to look at RBAC for a value they simply did not set.
	if claim == "" {
		return nil, errors.New("sandbox is \"kubernetes\" but no claim was named: a gear Job mounts the same " +
			"volume the data directory is on, so it has to be told which claim that is (COGITORIUM_KUBE_CLAIM)")
	}
	token, err := os.ReadFile(filepath.Join(inClusterDir, "token"))
	if err != nil {
		return nil, fmt.Errorf("read this pod's service account token: %w — the chart must set "+
			"serviceAccount.automountServiceAccountToken: true for gear Jobs to be created", err)
	}
	ca, err := os.ReadFile(filepath.Join(inClusterDir, "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("read the cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("the mounted cluster CA is not a certificate this can read")
	}
	if namespace == "" {
		ns, err := os.ReadFile(filepath.Join(inClusterDir, "namespace"))
		if err != nil {
			return nil, fmt.Errorf("read this pod's namespace: %w", err)
		}
		namespace = strings.TrimSpace(string(ns))
	}
	if image == "" {
		// The same default as the Docker backend, resolved here rather than
		// left empty: an empty image reaches the API as a Job that fails
		// validation, and the message names a field rather than a setting.
		image = DefaultImage
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	return &Kube{
		Image: image, Namespace: namespace, Claim: claim, Node: node,
		DataDir: abs, UID: int64(os.Getuid()),
		host:  "https://" + net.JoinHostPort(host, port),
		token: strings.TrimSpace(string(token)),
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		},
	}, nil
}

// jobName is unique per run. Kubernetes names have to be a DNS label, and two
// gears starting in the same millisecond must not collide — a create that fails
// with "already exists" would read as a cluster problem rather than a clash.
func jobName() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any platform this runs on; if it ever
		// did, a name that collides is better than a gear that cannot start.
		return "cogitorium-gear-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "cogitorium-gear-" + hex.EncodeToString(b[:])
}

func (k *Kube) Name() string { return "kubernetes" }

// Isolated is true, and it is the mount that makes it so: the Job sees one
// directory of the volume and cannot name another.
func (k *Kube) Isolated() bool { return true }

// Run creates the Job, follows its output, and returns what the gear produced.
func (k *Kube) Run(ctx context.Context, spec Spec) (Result, error) {
	rel, err := filepath.Rel(k.DataDir, spec.Dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Result{ExitCode: -1}, fmt.Errorf("a gear Job can only be given a directory inside the data "+
			"volume, and %s is not under %s", spec.Dir, k.DataDir)
	}

	timeout := spec.TimeoutSeconds
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	ctl := filepath.Join(spec.Dir, kubeCtl)
	if err := k.stage(ctl, spec); err != nil {
		return Result{ExitCode: -1}, err
	}
	// Removed whatever happens, including a timeout: these hold the gear's
	// arguments and its named values, and the directory they sit in is the one
	// the operator can open in the file tree.
	defer os.RemoveAll(ctl)

	name := jobName()
	if err := k.create(runCtx, name, rel, spec, timeout); err != nil {
		return Result{ExitCode: -1}, err
	}
	// context.WithoutCancel so a timed-out run still takes its Job away; a Job
	// left behind holds the volume and the next gear waits on it.
	defer k.delete(context.WithoutCancel(ctx), name)

	start := time.Now()
	res, waitErr := k.follow(runCtx, name, ctl, spec)
	slog.Info("sandboxed run", "backend", k.Name(), "job", name, "exit_code", res.ExitCode,
		"timed_out", res.TimedOut, "duration_ms", time.Since(start).Milliseconds())
	if res.TimedOut {
		return res, fmt.Errorf("timed out after %ds", timeout)
	}
	return res, waitErr
}

// stage writes what the container cannot be handed any other way.
//
// stdin is a file because a Job has no stdin to pipe, and a gear's arguments
// arrive on stdin. The environment is one file per name, in a directory,
// because the alternative is putting it in the Job object — and a gear's named
// values include secrets, which would then be readable by anything with
// permission to list Jobs in the namespace and would sit in etcd until the
// object was collected.
func (k *Kube) stage(ctl string, spec Spec) error {
	if err := os.MkdirAll(filepath.Join(ctl, "env"), 0o700); err != nil {
		return fmt.Errorf("prepare the gear's control directory: %w", err)
	}
	var stdin []byte
	if spec.Stdin != nil {
		b, err := io.ReadAll(spec.Stdin)
		if err != nil {
			return fmt.Errorf("read the gear's arguments: %w", err)
		}
		stdin = b
	}
	if err := os.WriteFile(filepath.Join(ctl, "stdin"), stdin, 0o600); err != nil {
		return err
	}
	for name, value := range spec.Env {
		if !validEnvName(name) {
			// A name that is not a name would become a file whose path the shim
			// splices into an export, so it is refused where it enters rather
			// than escaped where it is used.
			return fmt.Errorf("%q is not usable as an environment variable name", name)
		}
		if err := os.WriteFile(filepath.Join(ctl, "env", name), []byte(value), 0o600); err != nil {
			return err
		}
	}
	// Created here so following them does not have to distinguish "not written
	// yet" from "gone".
	for _, f := range []string{"stdout", "stderr"} {
		if err := os.WriteFile(filepath.Join(ctl, f), nil, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// shim is what the container actually runs.
//
// The two streams are kept apart in files rather than read from the pod log,
// because a pod log is one stream: everything a gear wrote to stderr would come
// back as stdout, and a gear whose result is its stdout would return its own
// warnings as the answer. The files are on the volume the server is already
// looking at, so this costs nothing and reads back byte for byte.
//
// $? is captured immediately and written last, and the server treats that file
// appearing as the signal that the run is over.
const shim = `set -u
for f in "$CTL"/env/*; do
  [ -f "$f" ] || continue
  export "$(basename "$f")"="$(cat "$f")"
done
rm -rf "$CTL"/env
"$@" < "$CTL"/stdin > "$CTL"/stdout 2> "$CTL"/stderr
code=$?
printf '%s' "$code" > "$CTL"/exit
exit "$code"
`

func (k *Kube) create(ctx context.Context, name, subPath string, spec Spec, timeout int) error {
	if _, err := k.do(ctx, http.MethodPost, k.jobsPath(""), k.manifest(name, subPath, spec, timeout)); err != nil {
		return fmt.Errorf("create the gear's Job: %w", err)
	}
	return nil
}

// manifest builds the Job. Separated from posting it so a test can read what
// this actually asks the cluster for — every line of it is a promise made
// elsewhere in the documentation, and a promise nothing checks is a comment.
func (k *Kube) manifest(name, subPath string, spec Spec, timeout int) map[string]any {
	args := append([]string{"sh", "-c", shim, spec.Command, spec.Command}, spec.Args...)

	image := k.Image
	if spec.Image != "" {
		image = spec.Image
	}
	container := map[string]any{
		"name":       "gear",
		"image":      image,
		"command":    args,
		"workingDir": kubeMount,
		"env": []any{
			map[string]any{"name": "CTL", "value": kubeCtlDir},
			// HOME is /tmp for the same reason as under Docker: the payload is
			// not a home directory, and an interpreter that writes a cache
			// beside the gear's code is writing into what comes back.
			map[string]any{"name": "HOME", "value": "/tmp"},
		},
		"volumeMounts": []any{
			map[string]any{"name": "work", "mountPath": kubeMount, "subPath": subPath},
			map[string]any{"name": "tmp", "mountPath": "/tmp"},
		},
		"securityContext": map[string]any{
			"allowPrivilegeEscalation": false,
			"readOnlyRootFilesystem":   true,
			"runAsUser":                k.UID,
			"capabilities":             map[string]any{"drop": []any{"ALL"}},
		},
	}
	if k.CPU != "" || k.Memory != "" {
		limits := map[string]any{}
		if k.CPU != "" {
			limits["cpu"] = k.CPU
		}
		if k.Memory != "" {
			limits["memory"] = k.Memory
		}
		container["resources"] = map[string]any{"limits": limits}
	}

	podSpec := map[string]any{
		"restartPolicy": "Never",
		// A gear must never be run twice because the first attempt failed: it
		// may have already written files, sent a request or spent money, and
		// the record would show one run with the second one's output.
		"automountServiceAccountToken": false,
		"containers":                   []any{container},
		"volumes": []any{
			map[string]any{"name": "work", "persistentVolumeClaim": map[string]any{"claimName": k.Claim}},
			map[string]any{"name": "tmp", "emptyDir": map[string]any{}},
		},
		"securityContext": map[string]any{
			"runAsNonRoot":   k.UID != 0,
			"runAsUser":      k.UID,
			"seccompProfile": map[string]any{"type": "RuntimeDefault"},
		},
	}
	if k.Node != "" {
		podSpec["nodeName"] = k.Node
	}

	labels := map[string]any{
		"app.kubernetes.io/name":      "cogitorium",
		"app.kubernetes.io/component": "gear",
		// The label a NetworkPolicy selects on. A gear without a grant carries
		// "denied" and the chart's policy cuts its egress; a granted one carries
		// "granted" and is left to the gate, which checks and records every
		// destination. Stated as a label rather than as an absent flag because
		// Kubernetes has no equivalent of --network none, and pretending it does
		// is how a promise becomes decorative.
		"cogitorium.orkcom.tech/network": map[bool]string{true: "granted", false: "denied"}[spec.Network],
	}

	return map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata":   map[string]any{"name": name, "labels": labels},
		"spec": map[string]any{
			"backoffLimit":          0,
			"activeDeadlineSeconds": timeout,
			// Deleted by this process when the run ends; the TTL is the backstop
			// for the case where it does not get to — a server killed mid-run
			// would otherwise leave Jobs holding the volume.
			"ttlSecondsAfterFinished": 300,
			"template": map[string]any{
				"metadata": map[string]any{"labels": labels},
				"spec":     podSpec,
			},
		},
	}
}

func (k *Kube) jobsPath(name string) string {
	p := "/apis/batch/v1/namespaces/" + k.Namespace + "/jobs"
	if name != "" {
		p += "/" + name
	}
	return p
}

// follow watches the two output files and the exit file, reporting each new
// chunk as it appears.
//
// Polling rather than watching the API: what is being followed is a file on a
// local volume, and the API knows nothing about it. The interval is short
// enough that a person sees output arrive and long enough that a gear printing
// a progress bar does not turn into a thousand stat calls a second.
func (k *Kube) follow(ctx context.Context, job, ctl string, spec Spec) (Result, error) {
	var stdout, stderr tailer
	stdout.path = filepath.Join(ctl, "stdout")
	stderr.path = filepath.Join(ctl, "stderr")
	exitPath := filepath.Join(ctl, "exit")

	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	// Checked far less often than the files: it is a network call, and it only
	// matters for a Job that will never write an exit file at all.
	nextStatus := time.Now().Add(3 * time.Second)

	for {
		stdout.pump(spec.OnOutput, "stdout")
		stderr.pump(spec.OnOutput, "stderr")

		if code, ok := readExit(exitPath); ok {
			stdout.pump(spec.OnOutput, "stdout")
			stderr.pump(spec.OnOutput, "stderr")
			return Result{Stdout: stdout.seen.String(), Stderr: stderr.seen.String(), ExitCode: code}, nil
		}

		if time.Now().After(nextStatus) {
			nextStatus = time.Now().Add(3 * time.Second)
			if failed, why := k.failed(ctx, job); failed {
				return Result{Stdout: stdout.seen.String(), Stderr: stderr.seen.String(), ExitCode: -1},
					fmt.Errorf("the gear's Job did not run: %s", why)
			}
		}

		select {
		case <-ctx.Done():
			// The exit file may have appeared in the same instant the deadline
			// did; a gear that finished is not a gear that timed out.
			if code, ok := readExit(exitPath); ok {
				return Result{Stdout: stdout.seen.String(), Stderr: stderr.seen.String(), ExitCode: code}, nil
			}
			return Result{Stdout: stdout.seen.String(), Stderr: stderr.seen.String(), ExitCode: -1, TimedOut: true}, nil
		case <-tick.C:
		}
	}
}

// tailer reads a file that is being appended to, from where it last stopped.
type tailer struct {
	path string
	at   int64
	seen bytes.Buffer
}

func (t *tailer) pump(on func(string, string), stream string) {
	f, err := os.Open(t.path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(t.at, io.SeekStart); err != nil {
		return
	}
	buf := make([]byte, 32*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			t.at += int64(n)
			t.seen.Write(buf[:n])
			if on != nil {
				on(stream, string(buf[:n]))
			}
		}
		if err != nil {
			return
		}
	}
}

func readExit(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, false
	}
	return code, true
}

// failed reports a Job that will never produce a result — an image that does
// not exist, a node that cannot take the pod, a deadline the API enforced.
//
// The pod is asked first, and that ordering is the whole point. Kubernetes does
// NOT fail a Job whose image cannot be pulled: the pod sits in ImagePullBackOff
// and is retried until the Job's deadline expires, so waiting for the Job to
// say something means waiting out the gear's entire timeout and then reporting
// a timeout. This was measured, not assumed — a gear pointed at a registry that
// does not exist took the full sixty seconds and came back "timed out", which
// is a true sentence about the wrong thing.
func (k *Kube) failed(ctx context.Context, name string) (bool, string) {
	if fatal, why := k.podVerdict(ctx, name); fatal {
		return true, why
	}
	raw, err := k.do(ctx, http.MethodGet, k.jobsPath(name), nil)
	if err != nil {
		return false, ""
	}
	var job struct {
		Status struct {
			Failed     int `json:"failed"`
			Conditions []struct {
				Type, Status, Reason, Message string
			} `json:"conditions"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &job) != nil {
		return false, ""
	}
	for _, c := range job.Status.Conditions {
		if c.Type == "Failed" && c.Status == "True" {
			return true, strings.TrimSpace(c.Reason + ": " + c.Message)
		}
	}
	if job.Status.Failed > 0 {
		return true, "the Job reported a failed pod"
	}
	return false, ""
}

// stuck names the container states a gear will not recover from.
//
// ContainerCreating and PodInitializing are deliberately absent: those are the
// ordinary first second of every run, and treating them as failure would fail
// every gear. What is here is the set that means the kubelet has decided it
// cannot produce this container — a bad image reference, an unreadable pull
// secret, a configuration the runtime rejected.
var stuck = map[string]bool{
	"ErrImagePull":               true,
	"ImagePullBackOff":           true,
	"InvalidImageName":           true,
	"ImageInspectError":          true,
	"RegistryUnavailable":        true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
}

// podVerdict asks the pod what is wrong, because the Job's status says at most
// that one failed. "Back-off pulling image ... not found" is an answer; "1
// failed" is not.
func (k *Kube) podVerdict(ctx context.Context, job string) (bool, string) {
	raw, err := k.do(ctx, http.MethodGet,
		"/api/v1/namespaces/"+k.Namespace+"/pods?labelSelector=job-name%3D"+job, nil)
	if err != nil {
		return false, ""
	}
	var list struct {
		Items []struct {
			Status struct {
				Phase      string `json:"phase"`
				Reason     string `json:"reason"`
				Message    string `json:"message"`
				Conditions []struct {
					Type, Status, Reason, Message string
				} `json:"conditions"`
				ContainerStatuses []struct {
					State map[string]struct {
						Reason  string `json:"reason"`
						Message string `json:"message"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &list) != nil || len(list.Items) == 0 {
		return false, ""
	}
	p := list.Items[0].Status
	for _, cs := range p.ContainerStatuses {
		for phase, st := range cs.State {
			if phase == "waiting" && stuck[st.Reason] {
				return true, strings.TrimSpace(st.Reason + ": " + st.Message)
			}
		}
	}
	// A pod nothing will schedule: the node pinned for the volume is gone or
	// full, or the claim cannot be attached where it was sent. Left to the
	// deadline this reads as a gear that hung.
	for _, c := range p.Conditions {
		if c.Type == "PodScheduled" && c.Status == "False" && c.Reason == "Unschedulable" {
			return true, strings.TrimSpace("Unschedulable: " + c.Message)
		}
	}
	if p.Phase == "Failed" {
		return true, strings.TrimSpace(p.Reason + ": " + p.Message)
	}
	return false, ""
}

func (k *Kube) delete(ctx context.Context, name string) {
	// Foreground so the pod goes with the Job rather than being left for the
	// garbage collector while it still holds the volume.
	path := k.jobsPath(name) + "?propagationPolicy=Foreground"
	if _, err := k.do(ctx, http.MethodDelete, path, nil); err != nil {
		slog.Warn("could not delete a finished gear Job", "job", name, "err", err)
	}
}

func (k *Kube) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		enc, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(enc)
	}
	req, err := http.NewRequestWithContext(ctx, method, k.host+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := k.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("%s %s: %s: %s", method, path, res.Status, apiMessage(raw))
	}
	return raw, nil
}

// apiMessage pulls the sentence out of a Kubernetes Status object. Its message
// names the exact permission a RoleBinding is missing, which is the one thing
// worth reading when this fails.
func apiMessage(raw []byte) string {
	var st struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &st) == nil && st.Message != "" {
		return st.Message
	}
	return strings.TrimSpace(string(raw))
}

// Check confirms this install can actually create a Job before a gear needs to.
//
// A missing RoleBinding otherwise surfaces as the first gear an agent runs
// failing with a 403 from an API the operator was not thinking about.
func (k *Kube) Check(ctx context.Context) error {
	_, err := k.do(ctx, http.MethodGet, k.jobsPath(""), nil)
	if err != nil {
		return fmt.Errorf("this pod cannot manage Jobs in namespace %s: %w", k.Namespace, err)
	}
	return nil
}
