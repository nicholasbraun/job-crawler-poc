// This file is llmbench's write discipline for the COMMITTED Extract Gold Set
// files (ADR-0043, #283): content is staged in a temporary file beside its target
// and renamed over it, so a reader sees either the whole previous file or the whole
// new one and never a truncated prefix of committed ground truth. A group is staged
// in full before any of it is renamed, so a failure cannot leave the substrate and
// its review sheets disagreeing about which rows carry a confirmer (ADR-0048). It is
// deliberately NOT used for the verbs' working artifacts -- a worksheet or a
// confirmation chunk is regenerable output written to a directory the operator
// names, not the record.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// fileWrite is one file in an atomic write: where it lands, the mode it lands with,
// and its content, produced by streaming into a writer rather than buffered whole --
// the substrate is 6.4 MB and the encoder already writes it row by row.
type fileWrite struct {
	Path string
	Perm os.FileMode
	// Write renders the file's whole content. It is called at most once, and an
	// error from it aborts the write with every target untouched.
	Write func(io.Writer) error
}

// writeBytes adapts already-rendered content to fileWrite.Write. Both review sheets
// are rendered whole in memory, so they have nothing to stream.
func writeBytes(data []byte) func(io.Writer) error {
	return func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	}
}

// atomicWrite stages one file and renames it into place. See atomicWriteAll.
func atomicWrite(w fileWrite) error { return atomicWriteAll(w) }

// atomicWriteAll stages EVERY file in writes into a temporary file in that file's
// own directory, and renames them into place only once all of them are written,
// permissioned and flushed to disk. Until the first rename nothing at any target
// path has changed, so an error partway through -- an encode failure, a full disk, a
// process killed mid-write -- leaves every previous file exactly as it was and
// leaves no partial file behind; the temporaries are removed on the way out.
//
// The staging file is created in the TARGET's directory because rename is only
// atomic within a filesystem: a temporary under $TMPDIR could be on another device
// and would degrade to a copy, which is the truncation this exists to prevent.
//
// The file is synced before the rename so a write that cannot land -- ENOSPC is the
// one this tool actually meets -- fails here rather than after the rename has
// published it. The parent directory is deliberately not synced: losing the rename
// to a power cut yields the PREVIOUS file, which is a correct outcome, and never a
// mixture of the two.
//
// The group is atomic only to the width of the rename loop. A process killed between
// two renames still leaves the substrate and a review sheet disagreeing -- the
// "half-applied write" ADR-0048's guard test exists to catch -- but the window is the
// microseconds between two renames rather than the seconds it takes to encode the
// whole substrate.
func atomicWriteAll(writes ...fileWrite) error {
	type stagedFile struct {
		tmp     string
		target  string
		renamed bool
	}
	staged := make([]*stagedFile, 0, len(writes))
	defer func() {
		for _, s := range staged {
			if s.renamed {
				continue
			}
			// Best effort: the caller already has the failure that brought us here,
			// and a temporary that cannot be removed is not something it can act on.
			_ = os.Remove(s.tmp)
		}
	}()

	for _, w := range writes {
		// The leading dot keeps a survivor of a hard kill out of a casual `ls` and out
		// of shell globs; the ".tmp-" infix is what .gitignore matches.
		f, err := os.CreateTemp(filepath.Dir(w.Path), "."+filepath.Base(w.Path)+".tmp-*")
		if err != nil {
			return fmt.Errorf("stage %q: %w", w.Path, err)
		}
		staged = append(staged, &stagedFile{tmp: f.Name(), target: w.Path})
		if err := stageFile(f, w); err != nil {
			return err
		}
	}

	for _, s := range staged {
		// A rename over an existing name inside one directory fails only on a
		// catastrophic filesystem error. Files earlier in the group are already
		// committed at this point; reporting that honestly beats pretending the group
		// rolled back.
		if err := os.Rename(s.tmp, s.target); err != nil {
			return fmt.Errorf("commit %q: %w", s.target, err)
		}
		s.renamed = true
	}
	return nil
}

// stageFile renders w's content into the staging file f, gives it w's mode, flushes
// it to disk and closes it. It closes f on every path, so a failure leaves no open
// handle for the caller's cleanup to trip over.
//
// The Chmod is load-bearing: os.CreateTemp creates 0600, and a committed gold-set
// file silently turning owner-only on the next apply is exactly the kind of surprise
// this write path must not introduce.
func stageFile(f *os.File, w fileWrite) error {
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()

	bw := bufio.NewWriterSize(f, 64*1024)
	if err := w.Write(bw); err != nil {
		return fmt.Errorf("write %q: %w", w.Path, err)
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("write %q: %w", w.Path, err)
	}
	if err := f.Chmod(w.Perm); err != nil {
		return fmt.Errorf("chmod %q: %w", w.Path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %q: %w", w.Path, err)
	}
	closed = true
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %q: %w", w.Path, err)
	}
	return nil
}
