package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Job manifest is a set of promises made in prose elsewhere, so it is
// checked here rather than believed.
//
// Every one of these was verified once by hand against a real cluster. That
// proves the design; it does not stop somebody editing a line of this manifest
// six months from now and quietly removing the thing that makes it safe. A
// subPath dropped by accident mounts the WHOLE data volume — the database and
// every provider key in it — into agent-authored code, and nothing else in the
// system would notice.
func TestTheGearJobAsksForExactlyWhatIsPromised(t *testing.T) {
	k := &Kube{
		Image: "python:3.12-alpine", Namespace: "cog", Claim: "cog-data",
		Node: "node-a", DataDir: "/data", UID: 65532, CPU: "1", Memory: "512Mi",
	}
	job := k.manifest("cogitorium-gear-abc", "gears/wordcount/v1.run-xyz",
		Spec{Command: "python3", Args: []string{"main.py"}}, 45)

	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("the manifest does not encode: %v", err)
	}
	var got struct {
		Spec struct {
			BackoffLimit          *int `json:"backoffLimit"`
			ActiveDeadlineSeconds int  `json:"activeDeadlineSeconds"`
			Template              struct {
				Spec struct {
					NodeName     string `json:"nodeName"`
					Automount    *bool  `json:"automountServiceAccountToken"`
					RestartPtion string `json:"restartPolicy"`
					Volumes      []struct {
						Name string `json:"name"`
						PVC  *struct {
							ClaimName string `json:"claimName"`
						} `json:"persistentVolumeClaim"`
					} `json:"volumes"`
					Containers []struct {
						Image        string   `json:"image"`
						Command      []string `json:"command"`
						WorkingDir   string   `json:"workingDir"`
						VolumeMounts []struct {
							Name, MountPath, SubPath string
						} `json:"volumeMounts"`
						SecurityContext struct {
							AllowPrivilegeEscalation *bool `json:"allowPrivilegeEscalation"`
							ReadOnlyRootFilesystem   *bool `json:"readOnlyRootFilesystem"`
							RunAsUser                *int  `json:"runAsUser"`
							Capabilities             struct {
								Drop []string `json:"drop"`
							} `json:"capabilities"`
						} `json:"securityContext"`
						Resources struct {
							Limits map[string]string `json:"limits"`
						} `json:"resources"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode the manifest: %v", err)
	}
	s := got.Spec.Template.Spec

	// The one that matters most: the container sees this run's directory and
	// nothing else on the volume.
	if len(s.Containers) != 1 {
		t.Fatalf("want one container, got %d", len(s.Containers))
	}
	c := s.Containers[0]
	var work bool
	for _, m := range c.VolumeMounts {
		if m.Name != "work" {
			continue
		}
		work = true
		if m.SubPath != "gears/wordcount/v1.run-xyz" {
			t.Fatalf("the work mount has subPath %q — without the run's own subPath this mounts the "+
				"WHOLE data volume, database included, into agent-authored code", m.SubPath)
		}
		if m.MountPath != "/work" {
			t.Fatalf("the payload is mounted at %q, and the gear's working directory is /work", m.MountPath)
		}
	}
	if !work {
		t.Fatal("no work mount at all: the gear would run against an empty directory")
	}

	// A gear's pod must hold no cluster credential. The server needs one to
	// create this Job; the code the Job runs must not inherit it.
	if s.Automount == nil || *s.Automount {
		t.Fatal("the gear's pod mounts a service account token: agent-authored code would hold a " +
			"credential for the cluster API")
	}
	if s.RestartPtion != "Never" {
		t.Fatalf("restartPolicy is %q: a gear re-run after failing may already have sent a request or "+
			"spent money, and the record would show one run with the second one's output", s.RestartPtion)
	}
	if got.Spec.BackoffLimit == nil || *got.Spec.BackoffLimit != 0 {
		t.Fatal("backoffLimit is not 0, so Kubernetes would retry a gear that failed")
	}
	if got.Spec.ActiveDeadlineSeconds != 45 {
		t.Fatalf("the gear's timeout did not reach the Job: %d", got.Spec.ActiveDeadlineSeconds)
	}
	if s.NodeName != "node-a" {
		t.Fatalf("the Job is not pinned to the server's node (%q): on a ReadWriteOnce volume it would "+
			"sit Pending forever, which reads as a gear that hung", s.NodeName)
	}
	if len(s.Volumes) == 0 || s.Volumes[0].PVC == nil || s.Volumes[0].PVC.ClaimName != "cog-data" {
		t.Fatal("the work volume does not come from the data claim")
	}

	sc := c.SecurityContext
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Fatal("privilege escalation is not refused")
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Fatal("the root filesystem is writable")
	}
	if sc.RunAsUser == nil || *sc.RunAsUser != 65532 {
		t.Fatal("the gear does not run as the identity that owns its payload, so it cannot read its own code")
	}
	if len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("capabilities are not dropped: %v", sc.Capabilities.Drop)
	}
	if c.Resources.Limits["cpu"] != "1" || c.Resources.Limits["memory"] != "512Mi" {
		t.Fatalf("the configured limits did not reach the container: %v", c.Resources.Limits)
	}
	if c.WorkingDir != "/work" {
		t.Fatalf("the gear does not start in its payload: %q", c.WorkingDir)
	}
	// sh -c <shim> $0 $1... — the command appears twice on purpose, once as $0
	// so the shim's "$@" carries the program as well as its arguments.
	if len(c.Command) < 5 || c.Command[0] != "sh" || c.Command[3] != "python3" || c.Command[4] != "python3" {
		t.Fatalf("the command is not the shim wrapping the gear: %v", c.Command)
	}
	if c.Command[len(c.Command)-1] != "main.py" {
		t.Fatalf("the gear's own arguments did not survive: %v", c.Command)
	}
}

// The network label is what a NetworkPolicy selects on, so it has to say what
// the operator decided rather than what is convenient.
func TestAGrantedGearAndAnUngrantedOneAreLabelledDifferently(t *testing.T) {
	k := &Kube{Image: "i", Namespace: "n", Claim: "c", DataDir: "/data"}
	for _, tc := range []struct {
		granted bool
		want    string
	}{{false, "denied"}, {true, "granted"}} {
		job := k.manifest("j", "sub", Spec{Command: "sh", Network: tc.granted}, 10)
		raw, _ := json.Marshal(job)
		var got struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				Template struct {
					Metadata struct {
						Labels map[string]string `json:"labels"`
					} `json:"metadata"`
				} `json:"template"`
			} `json:"spec"`
		}
		_ = json.Unmarshal(raw, &got)
		// The POD's labels are the ones a NetworkPolicy matches. A policy that
		// selected on the Job's labels alone would select nothing at all.
		if l := got.Spec.Template.Metadata.Labels["cogitorium.orkcom.tech/network"]; l != tc.want {
			t.Fatalf("granted=%v labelled the pod %q, want %q — the chart's policy selects on this",
				tc.granted, l, tc.want)
		}
		if got.Spec.Template.Metadata.Labels["app.kubernetes.io/component"] != "gear" {
			t.Fatal("the pod is not labelled as a gear, so the policy's selector misses it")
		}
	}
}

// A directory outside the data volume cannot be mounted, and saying so is
// better than mounting nothing and running the gear against an empty /work —
// which is a gear that "ran" and produced nothing, with no error anywhere.
func TestADirectoryOutsideTheVolumeIsRefusedRatherThanRunEmpty(t *testing.T) {
	k := &Kube{Image: "i", Namespace: "n", Claim: "c", DataDir: "/data"}
	_, err := k.Run(context.Background(), Spec{Dir: "/etc", Command: "sh"})
	if err == nil {
		t.Fatal("a directory outside the data volume was accepted")
	}
	if !strings.Contains(err.Error(), "/etc") || !strings.Contains(err.Error(), "/data") {
		t.Fatalf("the refusal does not name both paths, so nobody can act on it: %v", err)
	}
}

// The environment reaches the container as one file per name, and a name that
// is not a name would become a path the shim splices into an export.
func TestAnEnvironmentNameThatIsNotOneIsRefused(t *testing.T) {
	dir := t.TempDir()
	k := &Kube{Image: "i", Namespace: "n", Claim: "c", DataDir: dir}
	for _, bad := range []string{"../../escape", "with space", "with/slash", "", "9leading"} {
		err := k.stage(filepath.Join(dir, kubeCtl), Spec{Env: map[string]string{bad: "x"}})
		if err == nil {
			t.Fatalf("%q was accepted as an environment variable name", bad)
		}
	}
	// And an ordinary one is written where the shim looks for it, with the
	// value byte for byte — not shell-quoted, because the shim reads the file
	// rather than parsing it.
	if err := k.stage(filepath.Join(dir, kubeCtl), Spec{Env: map[string]string{"API_BASE": "http://x/ '\"$(id)"}}); err != nil {
		t.Fatalf("an ordinary name was refused: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, kubeCtl, "env", "API_BASE"))
	if err != nil {
		t.Fatalf("the value was not written where the shim reads it: %v", err)
	}
	if string(got) != "http://x/ '\"$(id)" {
		t.Fatalf("the value was altered on the way in: %q", got)
	}
}

// A gear's arguments arrive on stdin, and a Job has no stdin, so they are
// staged as a file. An empty one still has to exist: the shim redirects from it
// unconditionally, and a missing file is the gear failing to start.
func TestArgumentsAreStagedEvenWhenThereAreNone(t *testing.T) {
	dir := t.TempDir()
	k := &Kube{Image: "i", Namespace: "n", Claim: "c", DataDir: dir}
	if err := k.stage(filepath.Join(dir, kubeCtl), Spec{}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	for _, f := range []string{"stdin", "stdout", "stderr"} {
		if _, err := os.Stat(filepath.Join(dir, kubeCtl, f)); err != nil {
			t.Fatalf("%s was not created, and the shim redirects to it unconditionally: %v", f, err)
		}
	}
}

// Output is followed by reading the files the shim writes, so a gear that
// prints for a minute is watched rather than waited on.
func TestOutputIsReportedAsItArrivesRatherThanAtTheEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stdout")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tl := tailer{path: path}
	var chunks []string
	on := func(_, chunk string) { chunks = append(chunks, chunk) }

	tl.pump(on, "stdout")
	if len(chunks) != 0 {
		t.Fatalf("an empty file reported something: %v", chunks)
	}
	appendTo(t, path, "step one\n")
	tl.pump(on, "stdout")
	appendTo(t, path, "step two\n")
	tl.pump(on, "stdout")

	if len(chunks) != 2 || chunks[0] != "step one\n" || chunks[1] != "step two\n" {
		t.Fatalf("each append should be reported once, as it appears: %q", chunks)
	}
	if tl.seen.String() != "step one\nstep two\n" {
		t.Fatalf("the buffered copy is not the whole stream: %q", tl.seen.String())
	}
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

// The exit file is the signal that a run is over, so a half-written one must
// not be read as a result. It is written with a single printf, but a reader
// that accepted an empty file would report success for a gear still running.
func TestAHalfWrittenExitFileIsNotAResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exit")
	if _, ok := readExit(path); ok {
		t.Fatal("a missing exit file was read as a result")
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readExit(path); ok {
		t.Fatal("an empty exit file was read as exit code 0, which is a gear that had not finished " +
			"being reported as having succeeded")
	}
	if err := os.WriteFile(path, []byte("3"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, ok := readExit(path); !ok || code != 3 {
		t.Fatalf("readExit gave (%d, %v), want (3, true)", code, ok)
	}
}

// Off-cluster this backend cannot work at all, and it says so rather than
// starting and failing on the first gear.
func TestKubernetesModeRefusesOffCluster(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	_, err := NewKube("", t.TempDir(), "", "claim", "")
	if err == nil {
		t.Fatal("the Kubernetes backend was built outside a cluster")
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Fatalf("the refusal does not say what to use instead: %v", err)
	}
}

// The claim is the whole mechanism: without it there is no volume to mount and
// every gear would run against an empty directory.
func TestTheClaimIsRequired(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")
	_, err := NewKube("", t.TempDir(), "ns", "", "node")
	if err == nil {
		t.Fatal("a Kubernetes backend was built with no claim to mount")
	}
	if !strings.Contains(err.Error(), "COGITORIUM_KUBE_CLAIM") {
		t.Fatalf("the refusal does not name the setting to fix: %v", err)
	}
}

// The shim keeps the two streams apart. A pod log is one stream, so a gear's
// warnings would come back as its answer — and a gear whose result IS its
// stdout would return the wrong thing entirely.
func TestTheShimKeepsTheStreamsApart(t *testing.T) {
	for _, want := range []string{
		`> "$CTL"/stdout 2> "$CTL"/stderr`,
		`< "$CTL"/stdin`,
		`printf '%s' "$code" > "$CTL"/exit`,
	} {
		if !strings.Contains(shim, want) {
			t.Fatalf("the shim no longer does %q, which is what makes stdout and stderr separate here", want)
		}
	}
	// The exit code is captured before anything else runs, or a later command
	// in the shim would overwrite $?.
	if i, j := strings.Index(shim, "code=$?"), strings.Index(shim, `"$@"`); i < j {
		t.Fatal("the exit code is captured before the gear runs")
	}
}
