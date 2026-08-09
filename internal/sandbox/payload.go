package sandbox

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
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

// writePayload streams dir as a tar rooted at the container's working
// directory, ready for `docker cp - <id>:/`.
//
// Every entry is rewritten to the sandbox user with modes it can act on:
// directories traversable, files readable, and the executable bit preserved
// where the host had one — an uploaded binary gear is only a gear if it can
// still be run.
func writePayload(w io.Writer, dir string, root string) error {
	tw := tar.NewWriter(w)
	root = strings.TrimPrefix(root, "/")

	// The working directory itself, so it exists owned by the sandbox user
	// rather than by root. This is what makes a shell able to create a file.
	if err := tw.WriteHeader(&tar.Header{
		Name:     root + "/",
		Typeflag: tar.TypeDir,
		Mode:     0o755,
		Uid:      payloadUID,
		Gid:      payloadGID,
	}); err != nil {
		return fmt.Errorf("write payload root: %w", err)
	}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
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

		// Symlinks are carried across as symlinks rather than followed: a link
		// that points outside the directory must not become a copy of whatever
		// it pointed at on the host.
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return tw.WriteHeader(&tar.Header{
				Name:     root + "/" + filepath.ToSlash(rel),
				Linkname: target,
				Typeflag: tar.TypeSymlink,
				Mode:     0o777,
				Uid:      payloadUID,
				Gid:      payloadGID,
			})
		case d.IsDir():
			return tw.WriteHeader(&tar.Header{
				Name:     root + "/" + filepath.ToSlash(rel) + "/",
				Typeflag: tar.TypeDir,
				Mode:     0o755,
				Uid:      payloadUID,
				Gid:      payloadGID,
			})
		case info.Mode().IsRegular():
			mode := int64(0o644)
			if info.Mode().Perm()&0o111 != 0 {
				mode = 0o755
			}
			if err := tw.WriteHeader(&tar.Header{
				Name:     root + "/" + filepath.ToSlash(rel),
				Typeflag: tar.TypeReg,
				Mode:     mode,
				Size:     info.Size(),
				Uid:      payloadUID,
				Gid:      payloadGID,
			}); err != nil {
				return err
			}
			f, err := os.Open(path)
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
