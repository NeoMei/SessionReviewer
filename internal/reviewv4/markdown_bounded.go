package reviewv4

// markdownBoundedWriter is the shared rendering sink. It never allocates or
// exposes bytes beyond its configured document limit.
type markdownBoundedWriter struct {
	buffer []byte
	limit  int
	err    error
}

func newMarkdownBoundedWriter(limit int) *markdownBoundedWriter {
	return &markdownBoundedWriter{limit: limit}
}

func (w *markdownBoundedWriter) Write(p []byte) (int, error) {
	if err := w.reserve(len(p)); err != nil {
		return 0, err
	}
	w.buffer = append(w.buffer, p...)
	return len(p), nil
}

func (w *markdownBoundedWriter) WriteString(value string) (int, error) {
	if err := w.reserve(len(value)); err != nil {
		return 0, err
	}
	start := len(w.buffer)
	w.buffer = w.buffer[:start+len(value)]
	copy(w.buffer[start:], value)
	return len(value), nil
}

func (w *markdownBoundedWriter) WriteByte(value byte) error {
	if err := w.reserve(1); err != nil {
		return err
	}
	w.buffer = append(w.buffer, value)
	return nil
}

func (w *markdownBoundedWriter) WriteRepeat(value string, count int) error {
	if count < 0 || (len(value) != 0 && count > (w.limit-len(w.buffer))/len(value)) {
		return w.reject()
	}
	for range count {
		if _, err := w.WriteString(value); err != nil {
			return err
		}
	}
	return nil
}

func (w *markdownBoundedWriter) reserve(additional int) error {
	if w.err != nil {
		return w.err
	}
	if additional < 0 || additional > w.limit-len(w.buffer) {
		return w.reject()
	}
	required := len(w.buffer) + additional
	if required <= cap(w.buffer) {
		return nil
	}
	capacity := cap(w.buffer) * 2
	if capacity < 256 {
		capacity = 256
	}
	if capacity < required {
		capacity = required
	}
	if capacity > w.limit {
		capacity = w.limit
	}
	grown := make([]byte, len(w.buffer), capacity)
	copy(grown, w.buffer)
	w.buffer = grown
	return nil
}

func (w *markdownBoundedWriter) reject() error {
	if w.err == nil {
		w.err = &MarkdownError{Code: MarkdownFormatInvalid}
	}
	return w.err
}

func (w *markdownBoundedWriter) Len() int { return len(w.buffer) }

func (w *markdownBoundedWriter) Err() error { return w.err }

func (w *markdownBoundedWriter) take() ([]byte, error) {
	if w.err != nil {
		return nil, w.err
	}
	return w.buffer, nil
}

func renderFreshMarkdownLimit(p Presentation, limit int) (MarkdownPair, error) {
	return renderFreshMarkdownBounded(p, limit)
}

func replaceMarkdownBlocksLimit(document MarkdownDocument, replacements map[FieldKey]string, limit int) ([]byte, error) {
	return replaceMarkdownBlocksBounded(document, replacements, limit)
}
