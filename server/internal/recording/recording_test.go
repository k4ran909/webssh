package recording

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecording(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.cast.gz")
	w, err := Create(path, 80, 24, "dev@web")
	if err != nil {
		t.Fatal(err)
	}
	var closedWith int64 = -1
	w.OnClose = func(n int64) { closedWith = n }
	w.Output([]byte("hello "))
	// "é" (C3 A9) split across two chunks must come out whole.
	w.Output([]byte{'c', 'a', 'f', 0xC3})
	w.Output([]byte{0xA9, '\n'})
	w.Resize(120, 40)
	w.Close()
	if closedWith != 12 {
		t.Fatalf("OnClose bytes = %d", closedWith)
	}

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, _ := io.ReadAll(r)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var head struct {
		Version, Width, Height int
		Title                  string
	}
	if json.Unmarshal([]byte(lines[0]), &head) != nil || head.Version != 2 || head.Width != 80 || head.Title != "dev@web" {
		t.Fatalf("header: %s", lines[0])
	}
	var out strings.Builder
	sawResize := false
	for _, l := range lines[1:] {
		var ev []any
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatal(err)
		}
		switch ev[1] {
		case "o":
			out.WriteString(ev[2].(string))
		case "r":
			sawResize = ev[2] == "120x40"
		}
	}
	if out.String() != "hello café\n" || !sawResize {
		t.Fatalf("output %q, resize %v", out.String(), sawResize)
	}
}
