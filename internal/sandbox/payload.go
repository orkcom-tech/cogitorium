package sandbox

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Who the payload belongs to once it is inside.
//
// The container runs as 65534 — nobody — so that a process which escapes its
// interpreter owns nothing on the host. Getting the code in used to be
// `docker cp <dir>/. <id>:/work`, and `docker cp` preserves the SOURCE's
// ownership and mode. Both halves were right on their own and wrong together:
//
//   - the server's directories are 0700 and owned by the server's account, and
//     a directory without its execute bit cannot be entered at all — so a shell
//     whose banner said it held this workspace's files answered "Permission
//     denied" to `cat notes/hello.md`, and a gear could not read its own code
//     out of a subdirectory;
//   - /work itself is made by Docker as root, and everything copied in arrived
//     owned by the host's uid, so the sandbox user owned nothing it had been
//     handed and could not create a file either. `Spec.Writable` was set by
//     every shell and read by nobody: the flag existed, the behaviour did not.
//
// Copying as a tar stream fixes both at the source, because a tar header
// carries its own uid, gid and mode. The host's modes stop mattering — the data
// directory stays 0700 and keeps being the one boundary that means something —
// and what lands inside is owned by the user that has to work with it.
const (
	payloadUID = 65534
	payloadGID = 65534
)

// payloadOwner decides who owns each entry inside the sandbox, and that is the
// whole of what makes a directory read-only there.
//
// This is the mechanism, stated once: the process is 65534 and holds no
// capabilities (--cap-drop=ALL), so a file it does not own and that grants no
// write bit to "other" cannot be written, and — the part that actually matters
// — a DIRECTORY it does not own cannot have entries created, renamed or
// removed in it, whatever the modes of the files inside. Root owns the code and
// the inputs; 65534 owns exactly one subtree, out/. No bind mount, no
// --read-only flag, nothing the daemon has to support: ownership in a tar
// header, which is the one lever this package already had.
type payloadOwner struct {
	// writable gives the whole payload to the sandbox user.
	writable bool
	// out is the single relative path (and everything under it) that stays the
	// sandbox user's when writable is false. Empty means nothing is.
	out string
}

// forEntry answers who owns one entry and what may be done to it. Executable
// bits survive in every case — an uploaded binary gear is only a gear if it can
// still be run, read-only or not.
func (p payloadOwner) forEntry(rel string, isDir, isExec bool) (uid, gid int, mode int64) {
	if p.writable || p.writableEntry(rel) {
		switch {
		case isDir:
			return payloadUID, payloadGID, 0o755
		case isExec:
			return payloadUID, payloadGID, 0o755
		default:
			return payloadUID, payloadGID, 0o644
		}
	}
	// Owned by root, and with no write bit anywhere: the sandbox user may read
	// it, enter it and execute it, and may not change it or what is in it.
	switch {
	case isDir:
		return 0, 0, 0o555
	case isExec:
		return 0, 0, 0o555
	default:
		return 0, 0, 0o444
	}
}

func (p payloadOwner) writableEntry(rel string) bool {
	if p.out == "" {
		return false
	}
	out := path.Clean(p.out)
	return rel == out || strings.HasPrefix(rel, out+"/")
}

// writePayload streams dir as a tar rooted at the container's working
// directory, ready for `docker cp - <id>:/`.
//
// Every entry is rewritten by own: directories traversable, files readable, and
// ownership set to whoever is meant to be able to change them.
func writePayload(w io.Writer, dir string, root string, own payloadOwner) error {
	tw := tar.NewWriter(w)
	root = strings.TrimPrefix(root, "/")

	// The working directory itself, so it exists owned by the intended user
	// rather than by whoever Docker made it as. This is what makes a shell able
	// to create a file — and, when the payload is read-only, what stops a gear
	// dropping a file beside its own code.
	uid, gid, mode := own.forEntry("", true, false)
	if err := tw.WriteHeader(&tar.Header{
		Name:     root + "/",
		Typeflag: tar.TypeDir,
		Mode:     mode,
		Uid:      uid,
		Gid:      gid,
	}); err != nil {
		return fmt.Errorf("write payload root: %w", err)
	}

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(rel)
		isExec := info.Mode().Perm()&0o111 != 0
		uid, gid, mode := own.forEntry(slash, d.IsDir(), isExec)

		// Symlinks are carried across as symlinks rather than followed: a link
		// that points outside the directory must not become a copy of whatever
		// it pointed at on the host.
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return tw.WriteHeader(&tar.Header{
				Name:     root + "/" + slash,
				Linkname: target,
				Typeflag: tar.TypeSymlink,
				Mode:     0o777,
				Uid:      uid,
				Gid:      gid,
			})
		case d.IsDir():
			return tw.WriteHeader(&tar.Header{
				Name:     root + "/" + slash + "/",
				Typeflag: tar.TypeDir,
				Mode:     mode,
				Uid:      uid,
				Gid:      gid,
			})
		case info.Mode().IsRegular():
			if err := tw.WriteHeader(&tar.Header{
				Name:     root + "/" + slash,
				Typeflag: tar.TypeReg,
				Mode:     mode,
				Size:     info.Size(),
				Uid:      uid,
				Gid:      gid,
			}); err != nil {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			// Copy exactly the number of bytes the header promised. A file
			// growing under us would otherwise desynchronise the whole stream.
			if _, err := io.CopyN(tw, f, info.Size()); err != nil {
				return fmt.Errorf("copy %q into the payload: %w", rel, err)
			}
			return nil
		default:
			// Sockets, devices and fifos are skipped rather than refused: they
			// cannot be meaningful to a gear, and one stray socket in a
			// workspace should not stop the shell from opening.
			return nil
		}
	})
	if err != nil {
		return err
	}
	return tw.Close()
}
