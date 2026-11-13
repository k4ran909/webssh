// Package recording writes a terminal session's output to a gzipped
// asciicast v2 file (https://docs.asciinema.org/manual/asciicast/v2/), the
// same format the browser's own recorder and player use. Only output and
// window sizes are recorded, never keystrokes.
//
// Writing never blocks the session: events go through a buffer, and if the
// disk can't keep up the recording skips output and notes the gap, rather
// than slowing anyone's terminal down.
package recording

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"time"
	"unicode/utf8"
)

const buffer = 1024 // queued events

type event struct {
	at   time.Duration
	kind string // "o" output, "r" resize, "m" marker
	data string
}

// Writer is one session's recording. Its methods are safe for concurrent use.
type Writer struct {
	start time.Time
	ch    chan event
	done  chan struct{}

	mu      sync.Mutex
	tail    []byte // an incomplete UTF-8 sequence carried to the next chunk
	closed  bool
	dropped bool
	bytes   int64

	// OnClose runs once the file is complete, with the number of output bytes.
	OnClose func(bytes int64)
}

// Create starts a recording at path (created with mode 0600).
func Create(path string, cols, rows int, title string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	w := &Writer{start: time.Now(), ch: make(chan event, buffer), done: make(chan struct{})}
	header, _ := json.Marshal(map[string]any{
		"version": 2, "width": cols, "height": rows, "timestamp": w.start.Unix(), "title": title,
		"env": map[string]string{"TERM": "xterm-256color"},
	})
	go w.run(f, header)
	return w, nil
}

func (w *Writer) run(f *os.File, header []byte) {
	defer close(w.done)
	gz := gzip.NewWriter(f)
	bw := bufio.NewWriterSize(gz, 64<<10)
	_, _ = bw.Write(append(header, '\n'))
	// Flush now and then, so a recording of a running session can be played.
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	flush := func() {
		_ = bw.Flush()
		_ = gz.Flush()
	}
	for {
		select {
		case ev, ok := <-w.ch:
			if !ok {
				flush()
				_ = gz.Close()
				_ = f.Close()
				return
			}
			line, _ := json.Marshal([]any{float64(ev.at.Milliseconds()) / 1000, ev.kind, ev.data})
			_, _ = bw.Write(append(line, '\n'))
		case <-tick.C:
			flush()
		}
	}
}

func (w *Writer) send(ev event) {
	select {
	case w.ch <- ev:
	default:
		w.dropped = true
	}
}

// Output records terminal output.
func (w *Writer) Output(b []byte) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.bytes += int64(len(b))
	data := append(w.tail, b...)
	// Keep a trailing partial UTF-8 sequence for the next chunk, so characters
	// split across reads aren't mangled.
	cut := len(data)
	for i := 1; i <= utf8.UTFMax && i <= len(data); i++ {
		c := data[len(data)-i]
		if c < 0x80 {
			break
		}
		if utf8.RuneStart(c) {
			if !utf8.FullRune(data[len(data)-i:]) {
				cut = len(data) - i
			}
			break
		}
	}
	w.tail = append([]byte(nil), data[cut:]...)
	if cut == 0 {
		return
	}
	if w.dropped {
		w.dropped = false
		w.send(event{time.Since(w.start), "m", "output skipped: the recording fell behind"})
	}
	w.send(event{time.Since(w.start), "o", string(data[:cut])})
}

// Resize records a new window size.
func (w *Writer) Resize(cols, rows int) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.send(event{time.Since(w.start), "r", itoa(cols) + "x" + itoa(rows)})
	}
}

// Close finishes the file and calls OnClose.
func (w *Writer) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	close(w.ch)
	n := w.bytes
	w.mu.Unlock()
	<-w.done
	if w.OnClose != nil {
		w.OnClose(n)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// Open returns a reader for a recording's asciicast text. A recording that is
// still being written reads up to its last flush.
func Open(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &reader{gz: gz, f: f}, nil
}

type reader struct {
	gz *gzip.Reader
	f  *os.File
}

func (r *reader) Read(p []byte) (int, error) {
	n, err := r.gz.Read(p)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = io.EOF // still being written: stop at the last flush
	}
	return n, err
}

func (r *reader) Close() error {
	r.gz.Close()
	return r.f.Close()
}
