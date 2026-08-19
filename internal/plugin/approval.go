package plugin

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Approval is the operator's decision, and it covers exact content.
//
// This is the rule the gear catalog already lives by and the MCP client
// already enforces: approval names a version's bytes, not its name. A plugin
// that changes returns to pending, because a decision made about code somebody
// read is not a decision about code they have not.
//
// It is stored beside the plugin rather than in the database on purpose. An
// operator whose server will not start needs to be able to see and revoke what
// was approved without the server, and a decision recorded only in a table
// they cannot reach is a decision they cannot undo.

const approvalFile = "approved"

// Approval records what was approved and by whom.
type Approval struct {
	// Digest is the archive this decision was made about. A different archive
	// for the same version is different content and needs a new decision.
	Digest  string
	Version string
	By      string
	At      time.Time
}

// Approved reads the recorded decision. The second result is false when there
// is none, which is a state rather than an error: a freshly installed plugin
// has not been approved and that is exactly right.
func (s *Store) Approved(id string) (Approval, bool) {
	b, err := os.ReadFile(filepath.Join(s.root, id, approvalFile))
	if err != nil {
		return Approval{}, false
	}
	var a Approval
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		key, val, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok {
			continue
		}
		switch key {
		case "digest":
			a.Digest = val
		case "version":
			a.Version = val
		case "by":
			a.By = val
		case "at":
			a.At, _ = time.Parse(time.RFC3339, val)
		}
	}
	if a.Digest == "" || a.Version == "" {
		return Approval{}, false
	}
	return a, true
}

// Approve records a decision about the version currently installed.
//
// The digest is read from what is on disk rather than taken from the caller,
// so a decision can only ever be recorded about content this machine actually
// holds.
func (s *Store) Approve(id, by string) (Approval, error) {
	in, err := s.read(id)
	if err != nil {
		return Approval{}, err
	}
	digest, err := s.InstalledDigest(id)
	if err != nil {
		return Approval{}, err
	}
	a := Approval{Digest: digest, Version: in.Version, By: by, At: time.Now().UTC()}

	body := fmt.Sprintf("# What this install approved, and about exactly which bytes.\n"+
		"# A plugin whose content changes returns to pending: a decision made about\n"+
		"# code somebody read is not a decision about code they have not.\n"+
		"digest %s\nversion %s\nby %s\nat %s\n",
		a.Digest, a.Version, a.By, a.At.Format(time.RFC3339))

	return a, writeFileAtomic(filepath.Join(s.root, id, approvalFile), []byte(body))
}

// Revoke removes the decision and disables the plugin.
//
// Both, because leaving something enabled whose approval was just withdrawn
// would make the withdrawal decorative until the next restart.
func (s *Store) Revoke(id string) error {
	if err := s.Disable(id); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(s.root, id, approvalFile))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Pending reports why a plugin may not be enabled yet, or "" when it may.
//
// Three states, and the middle one is the one that matters: a plugin that WAS
// approved and has since changed is not the same as one nobody has looked at,
// and telling an operator which is which is the difference between a decision
// and a habit.
func (s *Store) Pending(id string) string {
	in, err := s.read(id)
	if err != nil {
		return err.Error()
	}
	a, ok := s.Approved(id)
	if !ok {
		return "nobody has approved this plugin on this install yet"
	}
	digest, err := s.InstalledDigest(id)
	if err != nil {
		return err.Error()
	}
	if a.Version != in.Version {
		return fmt.Sprintf("version %s was approved and %s is installed — approval covers "+
			"exact content, so this needs looking at again", a.Version, in.Version)
	}
	if a.Digest != digest {
		return fmt.Sprintf("version %s was approved, and what is installed under that version "+
			"is different content — approval covers exact bytes", in.Version)
	}
	return ""
}

// digestFile records what was installed, so approval has something to bind to.
const digestFile = "digest"

// InstalledDigest is the archive digest of what is on disk.
func (s *Store) InstalledDigest(id string) (string, error) {
	b, err := os.ReadFile(filepath.Join(s.root, id, digestFile))
	if err != nil {
		return "", fmt.Errorf("plugin %q: no digest was recorded for what is installed, so "+
			"nothing can be approved about it. Reinstall it", id)
	}
	d := strings.TrimSpace(string(b))
	if d == "" {
		return "", fmt.Errorf("plugin %q: the recorded digest is empty", id)
	}
	return d, nil
}

func (s *Store) setDigest(id, digest string) error {
	return writeFileAtomic(filepath.Join(s.root, id, digestFile), []byte(digest+"\n"))
}
