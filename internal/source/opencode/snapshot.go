package opencode

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/neomei/SessionReviewer/internal/pathguard"
	_ "modernc.org/sqlite"
	"modernc.org/sqlite/vfs"
)

const (
	maxSnapshotBytes int64 = 512 << 20
	snapshotTimeout        = 10 * time.Second
)

// withSnapshot never gives SQLite a source filesystem path. It reconstructs a
// bounded database in RAM and registers only those immutable bytes with a
// read-only VFS. Acquisition and validation have a cooperative time budget;
// callers must use their context and bounded queries inside use.
//
// Two identical captures and file identity/metadata checks reject observed
// concurrent changes. This is a conservative optimistic snapshot, not a SQLite
// read transaction: without source locks it cannot exclude an adversarial ABA
// writer or a filesystem that hides intervening changes. Nonempty rollback
// journals are rejected. A different-salt suffix from normal WAL reuse is ignored;
// same-salt checksum corruption fails closed even after a valid commit.
// SHM is neither opened nor read: reader read marks are irrelevant to replay.
func withSnapshot(ctx context.Context, path string, use func(*sql.DB) error) error {
	if ctx == nil || use == nil {
		return errors.New("snapshot context and callback are required")
	}
	ctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("SQLite source path must be absolute and clean")
	}
	content, err := captureSQLite(ctx, path)
	if err != nil {
		return fmt.Errorf("capture SQLite snapshot: %w", err)
	}
	vfsName, memoryVFS, err := vfs.New(snapshotFS{content})
	if err != nil {
		return fmt.Errorf("register SQLite snapshot: %w", err)
	}
	defer memoryVFS.Close()
	db, err := sql.Open("sqlite", "file:snapshot.db?mode=ro&immutable=1&vfs="+vfsName)
	if err != nil {
		return fmt.Errorf("open SQLite snapshot: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA query_only=ON; PRAGMA temp_store=MEMORY; PRAGMA trusted_schema=OFF"); err != nil {
		return fmt.Errorf("protect SQLite snapshot: %w", err)
	}
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&check); err != nil {
		return fmt.Errorf("validate SQLite snapshot: %w", err)
	}
	if check != "ok" {
		return errors.New("SQLite snapshot failed integrity validation")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return use(db)
}

type capturedSQLiteFile struct {
	name    string
	file    *os.File
	info    os.FileInfo
	content []byte
}

func captureSQLite(ctx context.Context, path string) ([]byte, error) {
	dir, err := pathguard.Open(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	base := filepath.Base(path)
	files := make([]capturedSQLiteFile, 0, 3)
	defer func() {
		for _, file := range files {
			if file.file != nil {
				_ = file.file.Close()
			}
		}
	}()
	var total int64
	for _, suffix := range []string{"", "-wal", "-journal"} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := base + suffix
		file, info, err := dir.OpenRegular(name)
		if errors.Is(err, os.ErrNotExist) && suffix != "" {
			files = append(files, capturedSQLiteFile{name: name})
			continue
		}
		if err != nil {
			return nil, err
		}
		files = append(files, capturedSQLiteFile{name: name, file: file, info: info})
		if info.Size() < 0 || info.Size() > maxSnapshotBytes-total {
			return nil, errors.New("SQLite snapshot exceeds aggregate byte limit")
		}
		total += info.Size()
		// Even an unrecognized nonempty journal may be an incomplete transaction.
		if suffix == "-journal" && info.Size() != 0 {
			return nil, errors.New("nonempty SQLite rollback journal is unsupported")
		}
	}
	for pass := 0; pass < 2; pass++ {
		if err := verifySQLiteFiles(ctx, dir, files); err != nil {
			return nil, err
		}
		for index := range files {
			file := &files[index]
			if file.file == nil {
				continue
			}
			if _, err := file.file.Seek(0, io.SeekStart); err != nil {
				return nil, err
			}
			if pass == 0 {
				file.content = make([]byte, int(file.info.Size()))
			}
			buffer := make([]byte, 64<<10)
			for offset := 0; offset < len(file.content); {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				size := min(len(buffer), len(file.content)-offset)
				if _, err := io.ReadFull(file.file, buffer[:size]); err != nil {
					return nil, errors.New("SQLite source changed during capture")
				}
				if pass == 0 {
					copy(file.content[offset:offset+size], buffer[:size])
				} else if !bytes.Equal(file.content[offset:offset+size], buffer[:size]) {
					return nil, errors.New("SQLite source content changed between captures")
				}
				offset += size
			}
		}
		if err := verifySQLiteFiles(ctx, dir, files); err != nil {
			return nil, err
		}
	}
	return reconstructSQLite(ctx, files[0].content, files[1].content, total)
}

func verifySQLiteFiles(ctx context.Context, dir *pathguard.Directory, files []capturedSQLiteFile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Reopen the complete path to catch replacement of any ancestor, too.
	current, err := pathguard.Open(dir.Path)
	if err != nil {
		return err
	}
	defer current.Close()
	if len(current.Ancestors) != len(dir.Ancestors) {
		return errors.New("SQLite source parent changed")
	}
	for i, before := range dir.Ancestors {
		if !os.SameFile(before, current.Ancestors[i]) {
			return errors.New("SQLite source ancestor changed")
		}
	}
	for _, file := range files {
		after, err := dir.Root.Lstat(file.name)
		if file.file == nil {
			if !errors.Is(err, os.ErrNotExist) {
				return errors.New("SQLite source sidecars changed")
			}
			continue
		}
		if err != nil || !sameSQLiteFile(file.info, after) {
			return errors.New("SQLite source namespace or metadata changed")
		}
		opened, err := file.file.Stat()
		if err != nil || !sameSQLiteFile(file.info, opened) {
			return errors.New("SQLite source file changed")
		}
	}
	return nil
}

func sameSQLiteFile(first, second os.FileInfo) bool {
	return first != nil && second != nil && second.Mode().IsRegular() && os.SameFile(first, second) && first.Size() == second.Size() && first.Mode() == second.Mode() && first.ModTime().Equal(second.ModTime())
}

func sqlitePageSize(main []byte) (int, error) {
	if len(main) < 100 || string(main[:16]) != "SQLite format 3\x00" {
		return 0, errors.New("invalid SQLite database header")
	}
	pageSize := int(binary.BigEndian.Uint16(main[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	if pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 || len(main)%pageSize != 0 {
		return 0, errors.New("invalid SQLite database page size or length")
	}
	if (main[18] != 1 && main[18] != 2) || (main[19] != 1 && main[19] != 2) || main[21] != 64 || main[22] != 32 || main[23] != 32 || pageSize-int(main[20]) < 480 {
		return 0, errors.New("unsupported SQLite database header")
	}
	return pageSize, nil
}

// WAL layout and checksum algorithm: https://sqlite.org/fileformat2.html#walformat
// Follow the valid salt/checksum prefix and apply through its last commit.
// SQLite reuses WAL files without truncating different-salt stale frames, so
// those suffixes after a valid commit are ignored. Same-salt checksum corruption
// fails closed. A partial frame cannot commit.
func reconstructSQLite(ctx context.Context, main, wal []byte, aggregate int64) ([]byte, error) {
	pageSize, err := sqlitePageSize(main)
	if err != nil {
		return nil, err
	}
	if len(wal) == 0 {
		return main, nil
	}
	if len(wal) < 32 {
		return nil, errors.New("truncated SQLite WAL header")
	}
	be := binary.BigEndian
	magic := be.Uint32(wal[:4])
	if (magic != 0x377f0682 && magic != 0x377f0683) || be.Uint32(wal[4:8]) != 3007000 || be.Uint32(wal[8:12]) != uint32(pageSize) || main[18] != 2 || main[19] != 2 {
		return nil, errors.New("unsupported SQLite WAL header")
	}
	var order binary.ByteOrder = binary.LittleEndian
	if magic == 0x377f0683 {
		order = binary.BigEndian
	}
	s0, s1 := walChecksum(order, wal[:24], 0, 0)
	if s0 != be.Uint32(wal[24:28]) || s1 != be.Uint32(wal[28:32]) {
		return nil, errors.New("SQLite WAL header checksum mismatch")
	}
	frameSize := 24 + pageSize
	lastCommit, pages := 0, uint32(0)
	for offset := 32; offset+frameSize <= len(wal); offset += frameSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		frame := wal[offset : offset+frameSize]
		if !bytes.Equal(frame[8:16], wal[16:24]) {
			if lastCommit != 0 {
				break
			}
			return nil, errors.New("SQLite WAL frame salt mismatch")
		}
		s0, s1 = walChecksum(order, frame[:8], s0, s1)
		s0, s1 = walChecksum(order, frame[24:], s0, s1)
		if s0 != be.Uint32(frame[16:20]) || s1 != be.Uint32(frame[20:24]) {
			return nil, errors.New("SQLite WAL frame checksum mismatch")
		}
		page := be.Uint32(frame[:4])
		if page == 0 || uint64(page)*uint64(pageSize) > uint64(maxSnapshotBytes) {
			return nil, errors.New("SQLite WAL page exceeds snapshot limit")
		}
		if commitPages := be.Uint32(frame[4:8]); commitPages != 0 {
			if uint64(commitPages)*uint64(pageSize) > uint64(maxSnapshotBytes) {
				return nil, errors.New("SQLite WAL commit exceeds snapshot limit")
			}
			lastCommit, pages = offset+frameSize, commitPages
		}
	}
	if lastCommit == 0 {
		return main, nil
	}
	size := int64(pages) * int64(pageSize)
	// Captured source buffers remain reachable while a larger main image is
	// allocated. Count both instead of budgeting only the final image size.
	peakBytes := aggregate
	if size > int64(len(main)) {
		peakBytes += size
	}
	if peakBytes > maxSnapshotBytes {
		return nil, errors.New("reconstructed SQLite snapshot exceeds aggregate byte limit")
	}
	if size > int64(len(main)) {
		expanded := make([]byte, int(size))
		copy(expanded, main)
		main = expanded
	} else {
		main = main[:int(size)]
	}
	for offset := 32; offset < lastCommit; offset += frameSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page := be.Uint32(wal[offset : offset+4])
		if page > pages {
			continue
		} // Later commits may truncate the database.
		start := int(page-1) * pageSize
		copy(main[start:start+pageSize], wal[offset+24:offset+frameSize])
	}
	if _, err := sqlitePageSize(main); err != nil {
		return nil, err
	}
	return main, nil
}

func walChecksum(order binary.ByteOrder, data []byte, s0, s1 uint32) (uint32, uint32) {
	for i := 0; i < len(data); i += 8 {
		s0 += order.Uint32(data[i:i+4]) + s1
		s1 += order.Uint32(data[i+4:i+8]) + s0
	}
	return s0, s1
}

// No FS method can reach a source file, open a sidecar, or mutate these bytes.
type snapshotFS struct{ data []byte }

func (s snapshotFS) Open(name string) (fs.File, error) {
	if name != "snapshot.db" {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &snapshotFile{Reader: bytes.NewReader(s.data), size: int64(len(s.data))}, nil
}

type snapshotFile struct {
	*bytes.Reader
	size int64
}

func (s *snapshotFile) Close() error               { return nil }
func (s *snapshotFile) Stat() (fs.FileInfo, error) { return snapshotInfo{s.size}, nil }

type snapshotInfo struct{ size int64 }

func (s snapshotInfo) Name() string       { return "snapshot.db" }
func (s snapshotInfo) Size() int64        { return s.size }
func (s snapshotInfo) Mode() fs.FileMode  { return 0400 }
func (s snapshotInfo) ModTime() time.Time { return time.Time{} }
func (s snapshotInfo) IsDir() bool        { return false }
func (s snapshotInfo) Sys() any           { return nil }
