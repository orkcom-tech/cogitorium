package gear

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/workdir"
)

// A gear that cannot take a file is not a tool. This is the file protocol, and
// it is deliberately the one every program already speaks: two directories
// beside the gear's own code.
//
//	in/   the files this call was given, at the paths the caller named them by.
//	      Read-only — see sandbox/payload.go for what actually makes that true.
//	out/  empty when the gear starts. Whatever it leaves there is copied into
//	      the workspace afterwards, and the caller is told what appeared.
//
// The protocol is in effect only for a call that names the argument at all. A
// call that does not gets no in/, no out/, and a run directory identical to the
// one it has always had — which is the whole of what makes an existing gear
// keep working.
const (
	inDir  = "in"
	outDir = "out"
)

// The ceilings. They exist because both directions cross a boundary a gear
// controls one side of: a caller naming forty files, or a gear writing until
// the operator's disk is full. Per-file matches inlet.MaxFilePayload, so a file
// that arrived through an inlet can always be handed straight to a gear —
// a smaller number here would make the two halves of the same workflow
// disagree about what a file is.
const (
	maxInFiles       = 32
	maxFileBytes     = 32 << 20
	maxTransferBytes = 64 << 20
	maxOutFiles      = 64
)

// Produced is one file a gear left in out/, and where it landed.
type Produced struct {
	// Name is the path the gear wrote, relative to out/.
	Name string `json:"name"`
	// Path is where it now is, relative to the workspace root — the same form
	// the caller uses to hand it to the next gear, or to read it.
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	// Replaced says a file was already there and no longer is. Landing under a
	// fresh directory per run means this should never be true; it is reported
	// rather than assumed, because "should never" is not a promise a caller can
	// check and this is.
	Replaced bool `json:"replaced,omitempty"`
}

// staged is one file placed in in/.
type staged struct {
	from string // workspace-relative, as the caller named it
	at   string // where the gear opens it: in/<the same path>
	size int64
}

// runToken names one call's run directory and the workspace directory its
// output lands in. Time first so a listing sorts into the order things
// happened; random tail so two calls in the same second cannot collide.
func runToken() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("could not name this run's directory: %w", err)
	}
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:]), nil
}

// stageIn copies the named workspace files into the run's in/ directory.
//
// Every path goes through workdir.ResolveInside, which resolves symlinks and
// proves containment against the deepest ancestor that exists — so a caller
// naming "../../cogitorium.db", or a symlink inside the workspace pointing at
// it, is refused here rather than discovered later. What is copied is CONTENT,
// never a link: the gear receives a plain file, and nothing about where the
// original lives crosses into the sandbox.
func stageIn(root, runDir string, names []string) ([]staged, error) {
	if len(names) > maxInFiles {
		return nil, fmt.Errorf("this call names %d files; %d is the most a gear can be given at once — "+
			"call it once per batch, or forge a gear that takes a directory listing", len(names), maxInFiles)
	}
	base := filepath.Join(runDir, inDir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, fmt.Errorf("create the run's in/ directory: %w", err)
	}

	var out []staged
	var total int64
	for _, name := range names {
		rel := workdir.Clean(name)
		if rel == "" || rel == "." {
			return nil, fmt.Errorf("%q names the workspace itself, not a file in it", name)
		}
		full, err := workdir.ResolveInside(root, name)
		if err != nil {
			return nil, fmt.Errorf("file %q cannot be given to a gear: %w", name, err)
		}
		info, err := os.Stat(full)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("there is no %q in this workspace — list the workspace's files to see what is", rel)
			}
			return nil, fmt.Errorf("file %q cannot be read: %w", rel, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("%q is a directory; name the files inside it instead", rel)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%q is not an ordinary file, so there is nothing to hand over", rel)
		}
		if info.Size() > maxFileBytes {
			return nil, fmt.Errorf("%q is %d bytes and the limit is %d — a gear that must work on something this "+
				"large should read it in pieces", rel, info.Size(), maxFileBytes)
		}
		if total+info.Size() > maxTransferBytes {
			return nil, fmt.Errorf("these files come to more than %d bytes together, which is the most one call may "+
				"carry; give the gear fewer at a time", maxTransferBytes)
		}

		dest := filepath.Join(base, filepath.FromSlash(rel))
		// Defence in depth. rel has been through Clean, so this cannot fire —
		// which is exactly why it is cheap to keep: the day it does fire,
		// something upstream has changed meaning.
		if !strings.HasPrefix(dest, base+string(os.PathSeparator)) {
			return nil, fmt.Errorf("%q would be placed outside the gear's in/ directory", rel)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil, fmt.Errorf("prepare in/ for %q: %w", rel, err)
		}
		n, err := copyFile(full, dest, 0o444)
		if err != nil {
			return nil, fmt.Errorf("hand %q to the gear: %w", rel, err)
		}
		total += n
		out = append(out, staged{from: rel, at: inDir + "/" + rel, size: n})
	}
	return out, nil
}

// collectOut moves what the gear produced into the workspace and reports it.
//
// Only regular files are taken. A symlink in out/ is the interesting case: with
// the Docker backend the process could point one at anything in its own
// container, and on the unsandboxed path at anything on this machine — and
// following it would copy the target into a workspace the operator reads. It is
// refused and named in the second return value, because a gear whose output
// silently did not arrive is a bug report nobody can write.
//
// destRel is fresh for every run, so a gear cannot reach an existing file by
// choosing a name; every landing site is still proved to be inside the
// workspace, because "cannot" and "is proved not to" are different claims.
func collectOut(runDir, root, destRel string) ([]Produced, []string, error) {
	base := filepath.Join(runDir, outDir)
	if _, err := os.Stat(base); os.IsNotExist(err) {
		// Only reachable without a sandbox, where the gear owns the directory
		// it was given. It is a fact about the run, not a failure of it: the
		// caller is told its files went nowhere and why, and gets the gear's
		// output anyway.
		return nil, []string{"the gear removed its own out/ directory, so nothing was collected from it"}, nil
	}

	var produced []Produced
	var ignored []string
	var total int64

	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		slash := filepath.ToSlash(rel)
		if d.IsDir() {
			return nil
		}
		// d.Type() is the lstat type: a symlink reports as a symlink and is
		// never followed, here or by the walk.
		if !d.Type().IsRegular() {
			ignored = append(ignored, fmt.Sprintf("out/%s was left as a %s, not a file, so it was not taken", slash, kindOf(d.Type())))
			return nil
		}
		info, err := d.Info()
		if err != nil {
			ignored = append(ignored, fmt.Sprintf("out/%s vanished before it could be taken", slash))
			return nil
		}
		if len(produced) >= maxOutFiles {
			ignored = append(ignored, fmt.Sprintf("out/%s and anything after it was left behind: a gear may hand back %d files at most", slash, maxOutFiles))
			return fs.SkipAll
		}
		if info.Size() > maxFileBytes {
			ignored = append(ignored, fmt.Sprintf("out/%s is %d bytes, over the %d-byte limit, so it was not taken", slash, info.Size(), maxFileBytes))
			return nil
		}
		if total+info.Size() > maxTransferBytes {
			ignored = append(ignored, fmt.Sprintf("out/%s and anything after it was left behind: one run may hand back %d bytes in total", slash, maxTransferBytes))
			return fs.SkipAll
		}

		wsRel := path.Join(destRel, slash)
		dest, err := workdir.ResolveInside(root, wsRel)
		if err != nil {
			ignored = append(ignored, fmt.Sprintf("out/%s could not be placed in the workspace: %v", slash, err))
			return nil
		}
		replaced := false
		if prior, err := os.Stat(dest); err == nil {
			if prior.IsDir() {
				ignored = append(ignored, fmt.Sprintf("out/%s could not be placed in the workspace: %s is a directory", slash, wsRel))
				return nil
			}
			replaced = true
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return fmt.Errorf("prepare %s in the workspace: %w", path.Dir(wsRel), err)
		}
		n, err := copyFile(p, dest, 0o600)
		if err != nil {
			ignored = append(ignored, fmt.Sprintf("out/%s could not be written into the workspace: %v", slash, err))
			return nil
		}
		total += n
		produced = append(produced, Produced{Name: slash, Path: wsRel, Bytes: n, Replaced: replaced})
		return nil
	})
	if err != nil {
		return produced, ignored, fmt.Errorf("collect what the gear left in out/: %w", err)
	}
	return produced, ignored, nil
}

func kindOf(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "symlink"
	case m&fs.ModeDevice != 0:
		return "device"
	case m&fs.ModeNamedPipe != 0:
		return "named pipe"
	case m&fs.ModeSocket != 0:
		return "socket"
	default:
		return "special file"
	}
}

// copyFile writes src to dest with the given mode and returns what it copied.
// The source is opened and copied rather than linked: a hard link would share
// the file with the workspace, so a gear writing "its" copy would rewrite the
// original.
func copyFile(src, dest string, mode os.FileMode) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, err
	}
	return n, nil
}
