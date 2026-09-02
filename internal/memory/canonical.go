package memory

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

const canonicalContextChunkSize = 64 * 1024

type digestPhase string

const (
	digestPhaseTraversal          digestPhase = "traversal"
	digestPhaseMapKeySort         digestPhase = "map_key_sort"
	digestPhaseUnorderedArraySort digestPhase = "unordered_array_sort"
	digestPhaseEncoding           digestPhase = "encoding"
	digestPhaseHashing            digestPhase = "hashing"
)

type digestPhaseHookKey struct{}

func withDigestPhaseHook(ctx context.Context, hook func(digestPhase)) context.Context {
	return context.WithValue(ctx, digestPhaseHookKey{}, hook)
}

func canonicalCheckpoint(ctx context.Context, phase digestPhase) error {
	if ctx == nil {
		return errors.New("canonical context is required")
	}
	if hook, ok := ctx.Value(digestPhaseHookKey{}).(func(digestPhase)); ok && hook != nil {
		hook(phase)
	}
	return context.Cause(ctx)
}

// CanonicalJSONWriter is the narrow writer contract used by the streaming
// canonical encoder. It intentionally matches bytes.Buffer, hash.Hash, and the
// store's exact-byte comparison writer without importing a broader I/O API.
type CanonicalJSONWriter interface {
	Write([]byte) (int, error)
}

// WriteCanonicalJSONContext writes encoding/json-compatible canonical JSON for
// the supported memory contracts without first materializing the whole value.
// Unlike DigestContext, it preserves ordinary array order and nil collection
// representation because it is also used to authenticate stored JSON bytes.
func WriteCanonicalJSONContext(ctx context.Context, writer CanonicalJSONWriter, value any) error {
	if writer == nil {
		return errors.New("canonical JSON writer is required")
	}
	if err := validateCanonicalValueContext(ctx, reflect.ValueOf(value), make(map[digestVisit]bool)); err != nil {
		return err
	}
	encoder := canonicalEncoder{ctx: ctx, writer: writer, visiting: make(map[digestVisit]bool)}
	if err := encoder.encode(reflect.ValueOf(value), ""); err != nil {
		return err
	}
	return canonicalCheckpoint(ctx, digestPhaseEncoding)
}

type canonicalEncoder struct {
	ctx           context.Context
	writer        CanonicalJSONWriter
	normalizeSets bool
	visiting      map[digestVisit]bool
}

func (encoder *canonicalEncoder) encode(value reflect.Value, field string) error {
	if err := canonicalCheckpoint(encoder.ctx, digestPhaseEncoding); err != nil {
		return err
	}
	if !value.IsValid() {
		return encoder.writeRaw([]byte("null"))
	}
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return encoder.writeRaw([]byte("null"))
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return encoder.writeRaw([]byte("null"))
		}
		visit := digestVisit{typeOf: value.Type(), ptr: value.Pointer()}
		if encoder.visiting[visit] {
			return errors.New("digest input contains a cycle")
		}
		encoder.visiting[visit] = true
		defer delete(encoder.visiting, visit)
		return encoder.encode(value.Elem(), field)
	case reflect.Bool:
		return encoder.writeRaw(strconv.AppendBool(nil, value.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return encoder.writeRaw(strconv.AppendInt(nil, value.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return encoder.writeRaw(strconv.AppendUint(nil, value.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		return errors.New("digest input contains a floating-point value")
	case reflect.String:
		return encoder.writeString(value.String())
	case reflect.Struct:
		return encoder.writeStruct(value)
	case reflect.Map:
		return encoder.writeMap(value, field)
	case reflect.Slice:
		return encoder.writeSlice(value, field)
	case reflect.Array:
		return encoder.writeArray(value)
	default:
		return fmt.Errorf("encode digest input: unsupported kind %s", value.Kind())
	}
}

func (encoder *canonicalEncoder) writeStruct(value reflect.Value) error {
	fields := canonicalStructFields(value.Type(), nil)
	if encoder.normalizeSets {
		if err := mergeSortStructFieldsContext(encoder.ctx, fields); err != nil {
			return err
		}
	}
	if err := encoder.writeRaw([]byte{'{'}); err != nil {
		return err
	}
	written := 0
	for _, field := range fields {
		current, found := canonicalFieldByIndex(value, field.index)
		if !found || (field.omitEmpty && canonicalEmptyValue(current)) {
			continue
		}
		if written > 0 {
			if err := encoder.writeRaw([]byte{','}); err != nil {
				return err
			}
		}
		if err := encoder.writeString(field.name); err != nil {
			return err
		}
		if err := encoder.writeRaw([]byte{':'}); err != nil {
			return err
		}
		if err := encoder.encode(current, field.name); err != nil {
			return err
		}
		written++
	}
	return encoder.writeRaw([]byte{'}'})
}

func mergeSortStructFieldsContext(ctx context.Context, values []canonicalField) error {
	if len(values) < 2 {
		return canonicalCheckpoint(ctx, digestPhaseMapKeySort)
	}
	buffer := make([]canonicalField, len(values))
	for width := 1; width < len(values); width *= 2 {
		if err := canonicalCheckpoint(ctx, digestPhaseMapKeySort); err != nil {
			return err
		}
		for left := 0; left < len(values); left += 2 * width {
			middle := left + width
			if middle > len(values) {
				middle = len(values)
			}
			right := left + 2*width
			if right > len(values) {
				right = len(values)
			}
			first, second, output := left, middle, left
			for first < middle && second < right {
				if err := canonicalCheckpoint(ctx, digestPhaseMapKeySort); err != nil {
					return err
				}
				comparison, err := compareStringsContext(ctx, values[first].name, values[second].name, digestPhaseMapKeySort)
				if err != nil {
					return err
				}
				if comparison <= 0 {
					buffer[output] = values[first]
					first++
				} else {
					buffer[output] = values[second]
					second++
				}
				output++
			}
			for first < middle {
				if err := canonicalCheckpoint(ctx, digestPhaseMapKeySort); err != nil {
					return err
				}
				buffer[output], first, output = values[first], first+1, output+1
			}
			for second < right {
				if err := canonicalCheckpoint(ctx, digestPhaseMapKeySort); err != nil {
					return err
				}
				buffer[output], second, output = values[second], second+1, output+1
			}
		}
		if err := copyCanonicalFieldsContext(ctx, values, buffer); err != nil {
			return err
		}
	}
	return canonicalCheckpoint(ctx, digestPhaseMapKeySort)
}

func (encoder *canonicalEncoder) writeMap(value reflect.Value, field string) error {
	if value.Type().Key().Kind() != reflect.String {
		return errors.New("encode digest input: only string-keyed maps are supported")
	}
	if value.IsNil() {
		if encoder.normalizeSets {
			if _, unordered := unorderedJSONObjects[field]; unordered {
				return encoder.writeRaw([]byte("{}"))
			}
		}
		return encoder.writeRaw([]byte("null"))
	}
	visit := digestVisit{typeOf: value.Type(), ptr: value.Pointer()}
	if encoder.visiting[visit] {
		return errors.New("digest input contains a cycle")
	}
	encoder.visiting[visit] = true
	defer delete(encoder.visiting, visit)
	names := make([]string, 0, value.Len())
	iterator := value.MapRange()
	for iterator.Next() {
		if err := canonicalCheckpoint(encoder.ctx, digestPhaseMapKeySort); err != nil {
			return err
		}
		names = append(names, iterator.Key().String())
	}
	if err := mergeSortStringsContext(encoder.ctx, names, digestPhaseMapKeySort); err != nil {
		return err
	}
	if err := encoder.writeRaw([]byte{'{'}); err != nil {
		return err
	}
	for index, name := range names {
		if index > 0 {
			if err := encoder.writeRaw([]byte{','}); err != nil {
				return err
			}
		}
		if err := encoder.writeString(name); err != nil {
			return err
		}
		if err := encoder.writeRaw([]byte{':'}); err != nil {
			return err
		}
		if err := encoder.encode(value.MapIndex(reflect.ValueOf(name).Convert(value.Type().Key())), name); err != nil {
			return err
		}
	}
	return encoder.writeRaw([]byte{'}'})
}

func (encoder *canonicalEncoder) writeSlice(value reflect.Value, field string) error {
	if value.IsNil() {
		if encoder.normalizeSets {
			if _, unordered := unorderedJSONArrays[field]; unordered {
				return encoder.writeRaw([]byte("[]"))
			}
		}
		return encoder.writeRaw([]byte("null"))
	}
	if value.Type().Elem().Kind() == reflect.Uint8 {
		return encoder.writeBytes(value.Bytes())
	}
	visit := digestVisit{typeOf: value.Type(), ptr: value.Pointer()}
	if encoder.visiting[visit] {
		return errors.New("digest input contains a cycle")
	}
	encoder.visiting[visit] = true
	defer delete(encoder.visiting, visit)
	if encoder.normalizeSets {
		if _, unordered := unorderedJSONArrays[field]; unordered {
			return encoder.writeUnorderedSlice(value)
		}
	}
	return encoder.writeOrdered(value)
}

func (encoder *canonicalEncoder) writeArray(value reflect.Value) error {
	return encoder.writeOrdered(value)
}

func (encoder *canonicalEncoder) writeOrdered(value reflect.Value) error {
	if err := encoder.writeRaw([]byte{'['}); err != nil {
		return err
	}
	for index := 0; index < value.Len(); index++ {
		if index > 0 {
			if err := encoder.writeRaw([]byte{','}); err != nil {
				return err
			}
		}
		if err := encoder.encode(value.Index(index), ""); err != nil {
			return err
		}
	}
	return encoder.writeRaw([]byte{']'})
}

type canonicalSortEntry struct {
	body []byte
}

func (encoder *canonicalEncoder) writeUnorderedSlice(value reflect.Value) error {
	entries := make([]canonicalSortEntry, value.Len())
	for index := 0; index < value.Len(); index++ {
		if err := canonicalCheckpoint(encoder.ctx, digestPhaseUnorderedArraySort); err != nil {
			return err
		}
		var buffer bytes.Buffer
		child := canonicalEncoder{ctx: encoder.ctx, writer: &buffer, normalizeSets: true, visiting: make(map[digestVisit]bool)}
		if err := child.encode(value.Index(index), ""); err != nil {
			return err
		}
		entries[index].body = buffer.Bytes()
	}
	if err := mergeSortCanonicalEntriesContext(encoder.ctx, entries); err != nil {
		return err
	}
	if err := encoder.writeRaw([]byte{'['}); err != nil {
		return err
	}
	for index := range entries {
		if index > 0 {
			if err := encoder.writeRaw([]byte{','}); err != nil {
				return err
			}
		}
		if err := encoder.writeRaw(entries[index].body); err != nil {
			return err
		}
	}
	return encoder.writeRaw([]byte{']'})
}

func (encoder *canonicalEncoder) writeBytes(value []byte) error {
	if err := encoder.writeRaw([]byte{'"'}); err != nil {
		return err
	}
	const sourceChunk = 48 * 1024
	encoded := make([]byte, base64.StdEncoding.EncodedLen(sourceChunk))
	for offset := 0; offset < len(value); {
		if err := canonicalCheckpoint(encoder.ctx, digestPhaseEncoding); err != nil {
			return err
		}
		end := min(len(value), offset+sourceChunk)
		if end < len(value) {
			end -= (end - offset) % 3
		}
		length := base64.StdEncoding.EncodedLen(end - offset)
		if cap(encoded) < length {
			encoded = make([]byte, length)
		}
		base64.StdEncoding.Encode(encoded[:length], value[offset:end])
		if err := encoder.writeRaw(encoded[:length]); err != nil {
			return err
		}
		offset = end
	}
	return encoder.writeRaw([]byte{'"'})
}

func (encoder *canonicalEncoder) writeString(value string) error {
	if err := encoder.writeRaw([]byte{'"'}); err != nil {
		return err
	}
	start := 0
	for index := 0; index < len(value); {
		if index-start >= canonicalContextChunkSize {
			if err := encoder.writeRaw([]byte(value[start:index])); err != nil {
				return err
			}
			start = index
		}
		character := value[index]
		if character < utf8.RuneSelf {
			if character >= 0x20 && character != '\\' && character != '"' && character != '<' && character != '>' && character != '&' {
				index++
				continue
			}
			if start < index {
				if err := encoder.writeRaw([]byte(value[start:index])); err != nil {
					return err
				}
			}
			var escaped []byte
			switch character {
			case '\\', '"':
				escaped = []byte{'\\', character}
			case '\n':
				escaped = []byte(`\n`)
			case '\r':
				escaped = []byte(`\r`)
			case '\t':
				escaped = []byte(`\t`)
			case '\b':
				escaped = []byte(`\b`)
			case '\f':
				escaped = []byte(`\f`)
			default:
				const hexadecimal = "0123456789abcdef"
				escaped = []byte{'\\', 'u', '0', '0', hexadecimal[character>>4], hexadecimal[character&0xf]}
			}
			if err := encoder.writeRaw(escaped); err != nil {
				return err
			}
			index++
			start = index
			continue
		}
		runeValue, size := utf8.DecodeRuneInString(value[index:])
		if runeValue == '\u2028' || runeValue == '\u2029' {
			if start < index {
				if err := encoder.writeRaw([]byte(value[start:index])); err != nil {
					return err
				}
			}
			if runeValue == '\u2028' {
				if err := encoder.writeRaw([]byte(`\u2028`)); err != nil {
					return err
				}
			} else if err := encoder.writeRaw([]byte(`\u2029`)); err != nil {
				return err
			}
			index += size
			start = index
			continue
		}
		index += size
	}
	if start < len(value) {
		if err := encoder.writeRaw([]byte(value[start:])); err != nil {
			return err
		}
	}
	return encoder.writeRaw([]byte{'"'})
}

func (encoder *canonicalEncoder) writeRaw(body []byte) error {
	for len(body) > 0 {
		if err := canonicalCheckpoint(encoder.ctx, digestPhaseEncoding); err != nil {
			return err
		}
		chunk := min(len(body), canonicalContextChunkSize)
		written, err := encoder.writer.Write(body[:chunk])
		if err != nil {
			return err
		}
		if written != chunk {
			return errors.New("short canonical JSON write")
		}
		body = body[chunk:]
	}
	return nil
}

type canonicalField struct {
	name      string
	index     []int
	omitEmpty bool
}

func canonicalStructFields(value reflect.Type, prefix []int) []canonicalField {
	fields := make([]canonicalField, 0, value.NumField())
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		if field.PkgPath != "" {
			continue
		}
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, options, _ := strings.Cut(tag, ",")
		path := append(append([]int(nil), prefix...), index)
		fieldType := field.Type
		if fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}
		if field.Anonymous && name == "" && fieldType.Kind() == reflect.Struct {
			fields = append(fields, canonicalStructFields(fieldType, path)...)
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields = append(fields, canonicalField{name: name, index: path, omitEmpty: canonicalTagOption(options, "omitempty")})
	}
	return fields
}

func canonicalTagOption(options, wanted string) bool {
	for options != "" {
		var option string
		option, options, _ = strings.Cut(options, ",")
		if option == wanted {
			return true
		}
	}
	return false
}

func canonicalFieldByIndex(value reflect.Value, index []int) (reflect.Value, bool) {
	current := value
	for _, component := range index {
		for current.Kind() == reflect.Pointer {
			if current.IsNil() {
				return reflect.Value{}, false
			}
			current = current.Elem()
		}
		current = current.Field(component)
	}
	return current, true
}

func canonicalEmptyValue(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return value.Len() == 0
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Interface, reflect.Pointer:
		return value.IsZero()
	}
	return false
}

func validateCanonicalValueContext(ctx context.Context, value reflect.Value, visiting map[digestVisit]bool) error {
	if err := canonicalCheckpoint(ctx, digestPhaseTraversal); err != nil {
		return err
	}
	if !value.IsValid() {
		return nil
	}
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		visit := digestVisit{typeOf: value.Type(), ptr: value.Pointer()}
		if visiting[visit] {
			return errors.New("digest input contains a cycle")
		}
		visiting[visit] = true
		defer delete(visiting, visit)
		return validateCanonicalValueContext(ctx, value.Elem(), visiting)
	case reflect.String:
		return validateCanonicalStringContext(ctx, value.String())
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return errors.New("digest input contains NaN or Inf")
		}
		return errors.New("digest input contains a floating-point value")
	case reflect.Struct:
		typeOf := value.Type()
		for index := 0; index < value.NumField(); index++ {
			if typeOf.Field(index).PkgPath != "" {
				continue
			}
			if err := validateCanonicalValueContext(ctx, value.Field(index), visiting); err != nil {
				return err
			}
		}
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		if value.Type().Key().Kind() != reflect.String {
			return errors.New("encode digest input: only string-keyed maps are supported")
		}
		visit := digestVisit{typeOf: value.Type(), ptr: value.Pointer()}
		if visiting[visit] {
			return errors.New("digest input contains a cycle")
		}
		visiting[visit] = true
		defer delete(visiting, visit)
		iterator := value.MapRange()
		for iterator.Next() {
			if err := validateCanonicalStringContext(ctx, iterator.Key().String()); err != nil {
				return err
			}
			if err := validateCanonicalValueContext(ctx, iterator.Value(), visiting); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if value.IsNil() || value.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		visit := digestVisit{typeOf: value.Type(), ptr: value.Pointer()}
		if visiting[visit] {
			return errors.New("digest input contains a cycle")
		}
		visiting[visit] = true
		defer delete(visiting, visit)
		for index := 0; index < value.Len(); index++ {
			if err := validateCanonicalValueContext(ctx, value.Index(index), visiting); err != nil {
				return err
			}
		}
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateCanonicalValueContext(ctx, value.Index(index), visiting); err != nil {
				return err
			}
		}
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return nil
	default:
		return fmt.Errorf("encode digest input: unsupported kind %s", value.Kind())
	}
	return nil
}

func validateCanonicalStringContext(ctx context.Context, value string) error {
	for index := 0; index < len(value); {
		if index%canonicalContextChunkSize == 0 {
			if err := canonicalCheckpoint(ctx, digestPhaseTraversal); err != nil {
				return err
			}
		}
		runeValue, size := utf8.DecodeRuneInString(value[index:])
		if runeValue == utf8.RuneError && size == 1 {
			return errors.New("digest input contains invalid UTF-8")
		}
		index += size
	}
	return nil
}

func mergeSortStringsContext(ctx context.Context, values []string, phase digestPhase) error {
	if len(values) < 2 {
		return canonicalCheckpoint(ctx, phase)
	}
	temporary := make([]string, len(values))
	for width := 1; width < len(values); width *= 2 {
		for start := 0; start < len(values); start += 2 * width {
			middle := min(start+width, len(values))
			end := min(start+2*width, len(values))
			left, right, output := start, middle, start
			for left < middle && right < end {
				if err := canonicalCheckpoint(ctx, phase); err != nil {
					return err
				}
				comparison, err := compareStringsContext(ctx, values[left], values[right], phase)
				if err != nil {
					return err
				}
				if comparison <= 0 {
					temporary[output], left = values[left], left+1
				} else {
					temporary[output], right = values[right], right+1
				}
				output++
			}
			for left < middle {
				if err := canonicalCheckpoint(ctx, phase); err != nil {
					return err
				}
				temporary[output], left, output = values[left], left+1, output+1
			}
			for right < end {
				if err := canonicalCheckpoint(ctx, phase); err != nil {
					return err
				}
				temporary[output], right, output = values[right], right+1, output+1
			}
		}
		if err := copyStringsContext(ctx, values, temporary, phase); err != nil {
			return err
		}
	}
	return canonicalCheckpoint(ctx, phase)
}

func mergeSortCanonicalEntriesContext(ctx context.Context, values []canonicalSortEntry) error {
	if len(values) < 2 {
		return canonicalCheckpoint(ctx, digestPhaseUnorderedArraySort)
	}
	temporary := make([]canonicalSortEntry, len(values))
	for width := 1; width < len(values); width *= 2 {
		for start := 0; start < len(values); start += 2 * width {
			middle := min(start+width, len(values))
			end := min(start+2*width, len(values))
			left, right, output := start, middle, start
			for left < middle && right < end {
				if err := canonicalCheckpoint(ctx, digestPhaseUnorderedArraySort); err != nil {
					return err
				}
				comparison, err := compareBytesContext(ctx, values[left].body, values[right].body, digestPhaseUnorderedArraySort)
				if err != nil {
					return err
				}
				if comparison <= 0 {
					temporary[output], left = values[left], left+1
				} else {
					temporary[output], right = values[right], right+1
				}
				output++
			}
			for left < middle {
				if err := canonicalCheckpoint(ctx, digestPhaseUnorderedArraySort); err != nil {
					return err
				}
				temporary[output], left, output = values[left], left+1, output+1
			}
			for right < end {
				if err := canonicalCheckpoint(ctx, digestPhaseUnorderedArraySort); err != nil {
					return err
				}
				temporary[output], right, output = values[right], right+1, output+1
			}
		}
		if err := copyCanonicalEntriesContext(ctx, values, temporary); err != nil {
			return err
		}
	}
	return canonicalCheckpoint(ctx, digestPhaseUnorderedArraySort)
}

const canonicalSortCopyChunk = 4096

func copyCanonicalFieldsContext(ctx context.Context, destination, source []canonicalField) error {
	for offset := 0; offset < len(source); offset += canonicalSortCopyChunk {
		if err := canonicalCheckpoint(ctx, digestPhaseMapKeySort); err != nil {
			return err
		}
		end := min(len(source), offset+canonicalSortCopyChunk)
		copy(destination[offset:end], source[offset:end])
	}
	return nil
}

func copyStringsContext(ctx context.Context, destination, source []string, phase digestPhase) error {
	for offset := 0; offset < len(source); offset += canonicalSortCopyChunk {
		if err := canonicalCheckpoint(ctx, phase); err != nil {
			return err
		}
		end := min(len(source), offset+canonicalSortCopyChunk)
		copy(destination[offset:end], source[offset:end])
	}
	return nil
}

func copyCanonicalEntriesContext(ctx context.Context, destination, source []canonicalSortEntry) error {
	for offset := 0; offset < len(source); offset += canonicalSortCopyChunk {
		if err := canonicalCheckpoint(ctx, digestPhaseUnorderedArraySort); err != nil {
			return err
		}
		end := min(len(source), offset+canonicalSortCopyChunk)
		copy(destination[offset:end], source[offset:end])
	}
	return nil
}

func compareStringsContext(ctx context.Context, left, right string, phase digestPhase) (int, error) {
	limit := min(len(left), len(right))
	for offset := 0; offset < limit; offset += canonicalContextChunkSize {
		if err := canonicalCheckpoint(ctx, phase); err != nil {
			return 0, err
		}
		end := min(limit, offset+canonicalContextChunkSize)
		if comparison := strings.Compare(left[offset:end], right[offset:end]); comparison != 0 {
			return comparison, nil
		}
	}
	if err := canonicalCheckpoint(ctx, phase); err != nil {
		return 0, err
	}
	switch {
	case len(left) < len(right):
		return -1, nil
	case len(left) > len(right):
		return 1, nil
	default:
		return 0, nil
	}
}

func compareBytesContext(ctx context.Context, left, right []byte, phase digestPhase) (int, error) {
	limit := min(len(left), len(right))
	for offset := 0; offset < limit; offset += canonicalContextChunkSize {
		if err := canonicalCheckpoint(ctx, phase); err != nil {
			return 0, err
		}
		end := min(limit, offset+canonicalContextChunkSize)
		if comparison := bytes.Compare(left[offset:end], right[offset:end]); comparison != 0 {
			return comparison, nil
		}
	}
	if err := canonicalCheckpoint(ctx, phase); err != nil {
		return 0, err
	}
	switch {
	case len(left) < len(right):
		return -1, nil
	case len(left) > len(right):
		return 1, nil
	default:
		return 0, nil
	}
}
