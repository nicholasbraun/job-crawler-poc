package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// errBoom is the failure a staged write is interrupted by. It stands for every way
// a write can die partway through -- an encode error, a full disk, a killed process
// -- because all of them reach atomicWriteAll the same way: the content never
// finishes arriving.
var errBoom = errors.New("atomicwrite_test: boom")

// seedFile writes known bytes at path so a test can assert they survived a failed
// write verbatim.
func seedFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("seed %q: %v", path, err)
	}
}

// mustRead reads path or fails the test.
func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return string(data)
}

// dirNames lists dir's entries so a test can assert no staging file was left behind.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %q: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestAtomicWriteNeverPublishesAPartialFile asserts the guarantee from INSIDE the
// write: while the new content is only half written, a reader of the target still
// sees the whole previous file, and the half-written bytes are sitting in a
// temporary in the SAME directory -- which is what makes the publish a rename rather
// than a cross-device copy, and therefore atomic (#283).
func TestAtomicWriteNeverPublishesAPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "goldset.jsonl")
	const old = "the previous, complete file\n"
	seedFile(t, path, old)

	err := atomicWrite(fileWrite{Path: path, Perm: goldSetPerm, Write: func(w io.Writer) error {
		if _, err := io.WriteString(w, "first half "); err != nil {
			return err
		}

		if got := mustRead(t, path); got != old {
			t.Errorf("mid-write the target reads %q, want the untouched %q", got, old)
		}
		names := dirNames(t, dir)
		if len(names) != 2 {
			t.Fatalf("mid-write the directory holds %v, want the target plus exactly one staging file", names)
		}
		staged := names[0]
		if staged == filepath.Base(path) {
			staged = names[1]
		}
		if prefix := "." + filepath.Base(path) + ".tmp-"; !strings.HasPrefix(staged, prefix) {
			t.Errorf("staging file %q does not start with %q -- it must be staged beside the target, or the rename is a cross-device copy", staged, prefix)
		}

		_, err := io.WriteString(w, "second half\n")
		return err
	}})
	if err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}

	const want = "first half second half\n"
	if got := mustRead(t, path); got != want {
		t.Errorf("after the write the target reads %q, want %q", got, want)
	}
	if names := dirNames(t, dir); len(names) != 1 || names[0] != filepath.Base(path) {
		t.Errorf("after the write the directory holds %v, want only the target -- the staging file must be gone", names)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if got := info.Mode().Perm(); got != goldSetPerm {
		t.Errorf("mode = %v, want %v -- os.CreateTemp makes 0600 and the chmod before the rename is what fixes it", got, goldSetPerm)
	}
}

// TestAtomicWriteLeavesThePreviousFileIntactWhenTheWriteFails is the interrupted
// write itself: the content stops arriving partway through, and the committed file
// must be exactly what it was, with no debris beside it.
func TestAtomicWriteLeavesThePreviousFileIntactWhenTheWriteFails(t *testing.T) {
	failing := func(w io.Writer) error {
		if _, err := io.WriteString(w, "half a new file, and then the process dies"); err != nil {
			return err
		}
		return errBoom
	}

	t.Run("an existing file survives", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "goldset.jsonl")
		const old = "457 rows of committed ground truth\n"
		seedFile(t, path, old)

		err := atomicWrite(fileWrite{Path: path, Perm: goldSetPerm, Write: failing})
		if err == nil {
			t.Fatal("atomicWrite returned nil for a write that failed")
		}
		if !errors.Is(err, errBoom) {
			t.Errorf("error = %v, want it to wrap the write's own failure", err)
		}
		if got := mustRead(t, path); got != old {
			t.Errorf("the committed file reads %q after a failed write, want the untouched %q", got, old)
		}
		if names := dirNames(t, dir); len(names) != 1 || names[0] != filepath.Base(path) {
			t.Errorf("directory holds %v after a failed write, want only the target", names)
		}
	})

	t.Run("no file is created where there was none", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "goldset.jsonl")

		if err := atomicWrite(fileWrite{Path: path, Perm: goldSetPerm, Write: failing}); err == nil {
			t.Fatal("atomicWrite returned nil for a write that failed")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stat %q = %v, want the target never to have been created", path, err)
		}
		if names := dirNames(t, dir); len(names) != 0 {
			t.Errorf("directory holds %v after a failed write into an empty directory, want nothing", names)
		}
	})

	t.Run("a missing directory is reported, not silently skipped", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "no-such-dir", "goldset.jsonl")

		err := atomicWrite(fileWrite{Path: path, Perm: goldSetPerm, Write: writeBytes([]byte("new\n"))})
		if err == nil {
			t.Fatal("atomicWrite returned nil for a target under a directory that does not exist")
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("stat %q = %v, want nothing to have been created", path, statErr)
		}
		if names := dirNames(t, dir); len(names) != 0 {
			t.Errorf("directory holds %v, want nothing", names)
		}
	})
}

// TestAtomicWriteAllCommitsEveryFileOrNone covers the group guarantee the substrate
// and its two review sheets depend on: they are rendered from the same rows and a
// reader is entitled to assume they agree (ADR-0048), so a failure anywhere in the
// group must leave every one of them on its previous version.
func TestAtomicWriteAllCommitsEveryFileOrNone(t *testing.T) {
	names := []string{"goldset.jsonl", "labels.tsv", "expected.tsv"}
	seed := func(t *testing.T, dir string) []string {
		t.Helper()
		paths := make([]string, len(names))
		for i, name := range names {
			paths[i] = filepath.Join(dir, name)
			seedFile(t, paths[i], "old "+name+"\n")
		}
		return paths
	}

	t.Run("a failure in the last file leaves every file on its previous version", func(t *testing.T) {
		dir := t.TempDir()
		paths := seed(t, dir)

		writes := make([]fileWrite, len(paths))
		for i, path := range paths {
			writes[i] = fileWrite{Path: path, Perm: goldSetPerm, Write: writeBytes([]byte("new " + names[i] + "\n"))}
		}
		writes[len(writes)-1].Write = func(w io.Writer) error {
			if _, err := io.WriteString(w, "half of expected.tsv"); err != nil {
				return err
			}
			return errBoom
		}

		err := atomicWriteAll(writes...)
		if err == nil {
			t.Fatal("atomicWriteAll returned nil for a group whose last file failed")
		}
		if !errors.Is(err, errBoom) {
			t.Errorf("error = %v, want it to wrap the write's own failure", err)
		}
		for i, path := range paths {
			want := "old " + names[i] + "\n"
			if got := mustRead(t, path); got != want {
				t.Errorf("%s reads %q, want the untouched %q -- the group must not commit partway", names[i], got, want)
			}
		}
		if got := dirNames(t, dir); len(got) != len(names) {
			t.Errorf("directory holds %v, want exactly the %d originals with no staging leftovers", got, len(names))
		}
	})

	t.Run("a successful group replaces every file", func(t *testing.T) {
		dir := t.TempDir()
		paths := seed(t, dir)

		writes := make([]fileWrite, len(paths))
		for i, path := range paths {
			writes[i] = fileWrite{Path: path, Perm: goldSetPerm, Write: writeBytes([]byte("new " + names[i] + "\n"))}
		}
		if err := atomicWriteAll(writes...); err != nil {
			t.Fatalf("atomicWriteAll: %v", err)
		}
		for i, path := range paths {
			want := "new " + names[i] + "\n"
			if got := mustRead(t, path); got != want {
				t.Errorf("%s reads %q, want %q", names[i], got, want)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat %q: %v", path, err)
			}
			if got := info.Mode().Perm(); got != goldSetPerm {
				t.Errorf("%s mode = %v, want %v", names[i], got, goldSetPerm)
			}
		}
		if got := dirNames(t, dir); len(got) != len(names) {
			t.Errorf("directory holds %v, want exactly the %d targets", got, len(names))
		}
	})
}

// TestAtomicWriteReportsWhichFileFailed keeps the error usable from a verb that
// writes three files in one call: atomicWriteAll names the path itself, which is why
// the callers dropped their own per-file "write %q" prefixes.
func TestAtomicWriteReportsWhichFileFailed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "labels.tsv")

	err := atomicWrite(fileWrite{Path: path, Perm: goldSetPerm, Write: func(io.Writer) error {
		return fmt.Errorf("render row 12: %w", errBoom)
	}})
	if err == nil {
		t.Fatal("atomicWrite returned nil for a write that failed")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the file it failed on (%q)", err, path)
	}
}
