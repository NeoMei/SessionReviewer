package session

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const defaultMaxRecordBytes = 64 << 20

func Stream(path string, opts DecodeOptions, visit func(Record) error) (DecodeSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return DecodeSummary{}, err
	}
	defer f.Close()
	summary, err := StreamFile(f, opts, visit)
	if err != nil {
		return summary, fmt.Errorf("stream session %q: %w", path, err)
	}
	return summary, nil
}

func StreamFile(file *os.File, opts DecodeOptions, visit func(Record) error) (DecodeSummary, error) {
	if file == nil {
		return DecodeSummary{}, fmt.Errorf("session file is required")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return DecodeSummary{}, fmt.Errorf("seek session file: %w", err)
	}
	return StreamReader(file, opts, visit)
}

// StreamFiles presents ordered rollout segments as one logical JSONL stream.
// It inserts a logical newline only when a non-empty segment is unterminated,
// so global line and byte offsets remain stable as later segments are added.
func StreamFiles(files []*os.File, opts DecodeOptions, visit func(Record) error) (DecodeSummary, error) {
	if len(files) == 0 {
		return DecodeSummary{}, fmt.Errorf("at least one session file is required")
	}
	readers := make([]io.Reader, 0, len(files)*2)
	for _, file := range files {
		if file == nil {
			return DecodeSummary{}, fmt.Errorf("session file is required")
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return DecodeSummary{}, fmt.Errorf("seek session file: %w", err)
		}
		info, err := file.Stat()
		if err != nil {
			return DecodeSummary{}, fmt.Errorf("inspect session file: %w", err)
		}
		readers = append(readers, file)
		if info.Size() == 0 {
			continue
		}
		var last [1]byte
		if _, err := file.ReadAt(last[:], info.Size()-1); err != nil {
			return DecodeSummary{}, fmt.Errorf("inspect session file ending: %w", err)
		}
		if last[0] != '\n' {
			readers = append(readers, strings.NewReader("\n"))
		}
	}
	return StreamReader(io.MultiReader(readers...), opts, visit)
}

func StreamReader(source io.Reader, opts DecodeOptions, visit func(Record) error) (DecodeSummary, error) {
	if source == nil {
		return DecodeSummary{}, fmt.Errorf("session reader is required")
	}
	if visit == nil {
		return DecodeSummary{}, fmt.Errorf("session record visitor is required")
	}
	if opts.MaxRecordBytes == 0 {
		opts.MaxRecordBytes = defaultMaxRecordBytes
	}

	reader := bufio.NewReaderSize(source, 64<<10)
	var summary DecodeSummary
	var offset int64
	for {
		line, bytesRead, readErr := readBoundedLine(reader, opts.MaxRecordBytes)
		if len(line) == 0 && readErr == io.EOF {
			return summary, nil
		}

		summary.Lines++
		start := offset
		offset += bytesRead
		if errors.Is(readErr, errRecordTooLarge) {
			return summary, fmt.Errorf("line %d exceeds %d bytes", summary.Lines, opts.MaxRecordBytes)
		}

		trimmed := bytes.TrimSpace(line)
		if summary.Lines >= opts.FromLine && len(trimmed) > 0 {
			var env envelope
			if err := json.Unmarshal(trimmed, &env); err != nil {
				summary.MalformedLines++
			} else {
				sum := sha256.Sum256(trimmed)
				record := Record{
					Line:       summary.Lines,
					ByteOffset: start,
					Timestamp:  env.Timestamp,
					Type:       env.Type,
					Payload:    env.Payload,
					SourceHash: hex.EncodeToString(sum[:]),
				}
				if err := visit(record); err != nil {
					return summary, err
				}
				summary.Records++
			}
		}

		if readErr == io.EOF {
			return summary, nil
		}
		if readErr != nil {
			return summary, readErr
		}
	}
}

var errRecordTooLarge = errors.New("record too large")

func readBoundedLine(reader *bufio.Reader, maxBytes int) ([]byte, int64, error) {
	capacity := maxBytes
	if capacity > 64<<10 {
		capacity = 64 << 10
	}
	if capacity < 0 {
		capacity = 0
	}
	line := make([]byte, 0, capacity)
	var bytesRead int64

	for {
		fragment, err := reader.ReadSlice('\n')
		bytesRead += int64(len(fragment))
		if len(fragment) > maxBytes-len(line) {
			return line, bytesRead, errRecordTooLarge
		}
		line = append(line, fragment...)

		if err == nil || err == io.EOF {
			return line, bytesRead, err
		}
		if err != bufio.ErrBufferFull {
			return line, bytesRead, err
		}
	}
}
