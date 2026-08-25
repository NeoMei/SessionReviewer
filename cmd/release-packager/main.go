package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type archiveEntry struct {
	Name string
	Body []byte
	Mode fs.FileMode
}

type releaseTarget struct {
	GOOS, GOARCH, Extension string
}

func main() {
	var source, dist, version, commit, builtAt string
	flag.StringVar(&source, "source", ".", "repository root")
	flag.StringVar(&dist, "dist", "dist", "artifact output directory")
	flag.StringVar(&version, "version", "", "semantic version without v prefix")
	flag.StringVar(&commit, "commit", "", "full lowercase Git commit")
	flag.StringVar(&builtAt, "built-at", "", "RFC3339 reproducible build timestamp")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal(errors.New("release packager does not accept positional arguments"))
	}
	stamp, err := time.Parse(time.RFC3339, builtAt)
	if err != nil {
		fatal(errors.New("--built-at must be RFC3339"))
	}
	stamp = stamp.UTC()
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(version) || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(commit) {
		fatal(errors.New("--version and full lowercase --commit are required"))
	}
	absSource, err := filepath.Abs(source)
	if err != nil {
		fatal(err)
	}
	absDist, err := filepath.Abs(dist)
	if err != nil {
		fatal(err)
	}
	if err := prepareOutputDirectory(absDist); err != nil {
		fatal(err)
	}

	common, err := commonEntries(absSource)
	if err != nil {
		fatal(err)
	}
	targets := []releaseTarget{{"darwin", "amd64", "tar.gz"}, {"darwin", "arm64", "tar.gz"}, {"windows", "amd64", "zip"}}
	checksums := make(map[string]string, len(targets))
	for _, target := range targets {
		artifact, body, err := packageTarget(absSource, version, commit, stamp, target, common)
		if err != nil {
			fatal(err)
		}
		if err := os.WriteFile(filepath.Join(absDist, artifact), body, 0o644); err != nil {
			fatal(err)
		}
		digest := sha256.Sum256(body)
		checksums[artifact] = hex.EncodeToString(digest[:])
	}
	names := make([]string, 0, len(checksums))
	for name := range checksums {
		names = append(names, name)
	}
	sort.Strings(names)
	var manifest strings.Builder
	for _, name := range names {
		fmt.Fprintf(&manifest, "%s  %s\n", checksums[name], name)
	}
	if err := os.WriteFile(filepath.Join(absDist, "SHA256SUMS"), []byte(manifest.String()), 0o644); err != nil {
		fatal(err)
	}
}

func prepareOutputDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(directory, 0o755)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("release output path is not a real directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("release output directory must be empty")
	}
	return nil
}

func packageTarget(source, version, commit string, stamp time.Time, target releaseTarget, common []archiveEntry) (string, []byte, error) {
	temporary, err := os.MkdirTemp("", "session-reviewer-release-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(temporary)
	binaryName := "session-reviewer"
	if target.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(temporary, binaryName)
	ldflags := fmt.Sprintf("-s -w -X github.com/neomei/SessionReviewer/internal/buildinfo.Version=%s -X github.com/neomei/SessionReviewer/internal/buildinfo.Commit=%s -X github.com/neomei/SessionReviewer/internal/buildinfo.BuiltAt=%s", version, commit, stamp.Format(time.RFC3339))
	command := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags, "-o", binaryPath, "./cmd/session-reviewer")
	command.Dir = source
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+target.GOOS, "GOARCH="+target.GOARCH)
	if output, err := command.CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("build %s/%s: %w: %s", target.GOOS, target.GOARCH, err, strings.TrimSpace(string(output)))
	}
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		return "", nil, err
	}
	entries := append([]archiveEntry(nil), common...)
	entries = append(entries, archiveEntry{Name: "session-reviewer/" + binaryName, Body: binary, Mode: 0o755})
	artifact := fmt.Sprintf("session-reviewer_%s_%s_%s.%s", version, target.GOOS, target.GOARCH, target.Extension)
	if target.Extension == "zip" {
		body, err := zipBytes(entries, stamp)
		return artifact, body, err
	}
	body, err := tarGzBytes(entries, stamp)
	return artifact, body, err
}

func commonEntries(source string) ([]archiveEntry, error) {
	entries := make([]archiveEntry, 0)
	for _, name := range []string{"README.md"} {
		body, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			return nil, err
		}
		entries = append(entries, archiveEntry{Name: "session-reviewer/" + name, Body: body, Mode: 0o644})
	}
	for _, name := range []string{"LICENSE", "NOTICE"} {
		body, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			return nil, errors.New("LICENSE and NOTICE are required")
		}
		entries = append(entries, archiveEntry{Name: "session-reviewer/" + name, Body: body, Mode: 0o644})
	}
	skillRoot := filepath.Join(source, "skill", "session-reviewer")
	err := filepath.WalkDir(skillRoot, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("skill package contains a non-regular file")
		}
		relative, err := filepath.Rel(source, filename)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		entries = append(entries, archiveEntry{Name: "session-reviewer/" + filepath.ToSlash(relative), Body: body, Mode: mode})
		return nil
	})
	return entries, err
}

func tarGzBytes(entries []archiveEntry, stamp time.Time) ([]byte, error) {
	if stamp.Location() != time.UTC {
		return nil, errors.New("archive timestamp must be UTC")
	}
	ordered, err := validateAndSortEntries(entries)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	gzipWriter, err := gzip.NewWriterLevel(&output, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	gzipWriter.Header.ModTime = stamp
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range ordered {
		header := &tar.Header{Name: entry.Name, Mode: int64(entry.Mode.Perm()), Size: int64(len(entry.Body)), ModTime: stamp, AccessTime: stamp, ChangeTime: stamp, Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tarWriter.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := tarWriter.Write(entry.Body); err != nil {
			return nil, err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func zipBytes(entries []archiveEntry, stamp time.Time) ([]byte, error) {
	if stamp.Location() != time.UTC {
		return nil, errors.New("archive timestamp must be UTC")
	}
	ordered, err := validateAndSortEntries(entries)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, entry := range ordered {
		header := &zip.FileHeader{Name: entry.Name, Method: zip.Deflate}
		header.SetMode(entry.Mode)
		header.SetModTime(stamp)
		file, err := writer.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := file.Write(entry.Body); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func validateAndSortEntries(entries []archiveEntry) ([]archiveEntry, error) {
	ordered := append([]archiveEntry(nil), entries...)
	seen := make(map[string]struct{}, len(ordered))
	for _, entry := range ordered {
		if entry.Name == "" || strings.Contains(entry.Name, `\`) || path.IsAbs(entry.Name) || path.Clean(entry.Name) != entry.Name || strings.HasPrefix(entry.Name, "../") || (entry.Mode != 0o644 && entry.Mode != 0o755) {
			return nil, errors.New("invalid archive entry")
		}
		if _, duplicate := seen[entry.Name]; duplicate {
			return nil, errors.New("duplicate archive entry")
		}
		seen[entry.Name] = struct{}{}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	return ordered, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "release-packager:", err)
	os.Exit(1)
}
