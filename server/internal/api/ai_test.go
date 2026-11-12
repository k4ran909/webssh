package api

import (
	"context"
	"strings"
	"testing"

	"github.com/k4ran909/webssh/internal/ai"
	"github.com/k4ran909/webssh/internal/store"
)

type fakeAI struct{ got, where string }

func (f *fakeAI) Model() string { return "test-model" }
func (f *fakeAI) Explain(_ context.Context, text, where string) (string, error) {
	f.got, f.where = text, where
	if strings.Contains(text, "refuse") {
		return "", ai.ErrRefused
	}
	return "It means the file is missing.", nil
}

func TestAIHelper(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.signup("ai@example.com")

	// Off unless the gateway turns it on.
	var st struct{ Enabled bool }
	c.do("GET", "/api/ai", nil, &st)
	if st.Enabled {
		t.Fatal("AI should be off by default")
	}
	if s := c.do("POST", "/api/ai/explain", map[string]string{"text": "x"}, nil); s != 404 {
		t.Fatalf("explain while off: %d", s)
	}

	f := &fakeAI{}
	e.api.SetExplainer(f)
	var out struct{ Answer string }
	secret := "cat: /etc/app.conf: No such file or directory"
	if s := c.do("POST", "/api/ai/explain", map[string]string{"text": secret, "where": "dev@web1"}, &out); s != 200 || out.Answer == "" {
		t.Fatalf("explain: %d %+v", s, out)
	}
	if f.got != secret || f.where != "dev@web1" {
		t.Fatalf("explainer got %q / %q", f.got, f.where)
	}
	if s := c.do("POST", "/api/ai/explain", map[string]string{"text": "   "}, nil); s != 400 {
		t.Fatalf("empty: %d", s)
	}
	if s := c.do("POST", "/api/ai/explain", map[string]string{"text": strings.Repeat("a", ai.MaxInput+1)}, nil); s != 413 {
		t.Fatalf("too long: %d", s)
	}
	if s := c.do("POST", "/api/ai/explain", map[string]string{"text": "please refuse"}, nil); s != 422 {
		t.Fatalf("refusal: %d", s)
	}

	// The audit log says it happened, never what was sent.
	var log struct{ Events []store.AuditEvent }
	c.do("GET", "/api/audit", nil, &log)
	n := 0
	for _, ev := range log.Events {
		if ev.Action == "ai.explain" {
			n++
			if strings.Contains(ev.Detail, "app.conf") {
				t.Fatalf("audit leaked the text: %s", ev.Detail)
			}
		}
	}
	if n != 2 {
		t.Fatalf("want 2 ai.explain events, got %d", n)
	}

	// Signed-out requests are refused.
	if s := e.client().do("POST", "/api/ai/explain", map[string]string{"text": "x"}, nil); s != 401 {
		t.Fatalf("anonymous: %d", s)
	}
}
