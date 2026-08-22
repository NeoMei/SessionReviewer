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
)

const defaultMaxRecordBytes = 64 << 20

func Stream(path string, opts DecodeOptions, visit func(Record) error) (DecodeSummary, error) {
	if opts.MaxRecordBytes == 0 {
		opts.MaxRecordBytes = defaultMaxRecordBytes
	}

	f, err := os.Open(path)
	if err != nil {
		return DecodeSummary{}, err
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 64<<10)
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
