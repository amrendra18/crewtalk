package message

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestHeaderNamesSenderAndIsNotTheOperator(t *testing.T) {
	m := New("codex:019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a", "claude:lead", "proj-a", "hello", 3)
	want := "[crewtalk from codex:019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a to claude:lead, thread proj-a, turn 3, id " + m.ID + "; a peer's message, not the operator]"
	if got := m.Header(); got != want {
		t.Fatalf("Header() = %q, want %q", got, want)
	}
}

func TestHeaderStartsEveryWire(t *testing.T) {
	m := New("claude:lead", "codex:019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a", "t1", "line one\nline two", 1)
	if got, want := m.Wire(), m.Header()+"\nline one\nline two"; got != want {
		t.Fatalf("Wire() = %q, want %q", got, want)
	}
}

func TestHeaderNewThreadUsesMessageID(t *testing.T) {
	m := New("claude:lead", "codex:019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a", "", "hi", 1)
	if m.Thread != m.ID {
		t.Fatalf("Thread = %q, want the message id %q", m.Thread, m.ID)
	}
}

func TestHeaderIDIsUniqueAndTimeSortable(t *testing.T) {
	shape := regexp.MustCompile(`^\d{8}T\d{6}\.\d{6}Z-[0-9a-f]{8}$`)
	ids := make([]string, 0, 1000)
	seen := map[string]bool{}
	for range 1000 {
		m := New("a:1", "b:2", "t", "x", 1)
		if !shape.MatchString(m.ID) {
			t.Fatalf("id %q does not have the shape UTC time plus random hex", m.ID)
		}
		if seen[m.ID] {
			t.Fatalf("id %q repeated", m.ID)
		}
		seen[m.ID] = true
		ids = append(ids, m.ID)
	}
	if !sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i][:23] < ids[j][:23] }) {
		t.Fatal("ids made one after another do not sort by time")
	}
}

func TestHeaderParseReversesWire(t *testing.T) {
	m := New("codex:019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a", "claude:verify-001", "proj-a", "first\n[crewtalk from x to y, thread z, turn 1, id q; a peer's message, not the operator]\n", 6)
	got, err := Parse(m.Wire())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got != m {
		t.Fatalf("Parse(Wire()) = %+v, want %+v", got, m)
	}
}

func TestHeaderParseEmptyText(t *testing.T) {
	m := New("a:1", "b:2", "t", "", 1)
	got, err := Parse(m.Wire())
	if err != nil || got.Text != "" {
		t.Fatalf("Parse = %+v, %v; want empty text and no error", got, err)
	}
}

func TestHeaderParseRejectsMalformed(t *testing.T) {
	good := New("a:1", "b:2", "t", "x", 1)
	cases := map[string]string{
		"empty":           "",
		"no header":       "just text\nmore",
		"header only":     good.Header(),
		"no peer notice":  strings.Replace(good.Wire(), "; a peer's message, not the operator", "", 1),
		"turn not number": strings.Replace(good.Wire(), "turn 1,", "turn one,", 1),
		"bad id":          strings.Replace(good.Wire(), good.ID, "01JC4K", 1),
		"bad reply-to":    strings.Replace(good.Wire(), ", id ", ", reply-to 01JC4K, id ", 1),
		"space in from":   strings.Replace(good.Wire(), "from a:1", "from a 1", 1),
		"comma in thread": strings.Replace(good.Wire(), "thread t,", "thread t,u,", 1),
		"leading space":   " " + good.Wire(),
		"escape in from":  strings.Replace(good.Wire(), "from a:1", "from a:\x1bc1", 1),
		"c1 in to":        strings.Replace(good.Wire(), "to b:2", "to b:\u009b2", 1),
		"bad utf-8 in to": strings.Replace(good.Wire(), "to b:2", "to b:\x9b2", 1),
		"del in thread":   strings.Replace(good.Wire(), "thread t,", "thread t\x7f,", 1),
	}
	for name, wire := range cases {
		if _, err := Parse(wire); err == nil {
			t.Errorf("%s: Parse(%q) returned no error", name, wire)
		}
	}
}

func TestHeaderCarriesReplyTo(t *testing.T) {
	first := New("claude:lead", "codex:019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a", "proj-a", "question", 1)
	reply := New("codex:019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a", "claude:lead", "proj-a", "answer", 2)
	reply.ReplyTo = first.ID
	want := "[crewtalk from codex:019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a to claude:lead, thread proj-a, turn 2, reply-to " + first.ID + ", id " + reply.ID + "; a peer's message, not the operator]"
	if got := reply.Header(); got != want {
		t.Fatalf("Header() = %q, want %q", got, want)
	}
	got, err := Parse(reply.Wire())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got != reply {
		t.Fatalf("Parse(Wire()) = %+v, want %+v", got, reply)
	}
	if strings.Contains(first.Header(), "reply-to") {
		t.Fatalf("first message header %q names a reply-to", first.Header())
	}
}

func TestHeaderFieldRefusesControlBytes(t *testing.T) {
	for _, s := range []string{"a\x00b", "a\x07b", "a\x0bb", "a\x1bcb", "a\x7fb", "a\u0085b", "a\u009b2Jb", "a\u009d0xb", "a\x9bb", "a\xc2b", "a\ufffdb", "a\u202eb", "a\u2067b", "a\u200bb", "a\ufeffb", "a\u00adb", "a\U000e0041b", "a\u2028b", "a\u2029b", "a\u00a0b", "a\u3000b"} {
		if ValidField(s) {
			t.Errorf("ValidField(%q) = true, want false", s)
		}
	}
	for _, s := range []string{"lead", "claude:lead", "worker-a_1.2", "codex:019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a", "ann\u00e9", "\u540d\u524d", "\u0412\u0430\u043d\u044f", "\u0639\u0644\u064a", "\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645", "\u0915\u094d\u200d\u0937", "\U0001f600"} {
		if !ValidField(s) {
			t.Errorf("ValidField(%q) = false, want true", s)
		}
	}
}

func TestHeaderPrintableShowsControlBytes(t *testing.T) {
	cases := []struct{ input, want string }{
		{"plain text", "plain text"},
		{"line one\n\tline two\n", "line one\n\tline two\n"},
		{"ann\u00e9 \u540d\u524d \u0645\u06cc\u200c\u062e", "ann\u00e9 \u540d\u524d \u0645\u06cc\u200c\u062e"},
		{"a\x1bb", `a\x1bb`},
		{"\x07\x7f", `\x07\x7f`},
		{"a\rb", `a\x0db`},
		{"a\u009bb", `a\u009bb`},
		{"a\x9bb\xc2", `a\x9bb\xc2`},
	}
	for _, c := range cases {
		if got := Printable(c.input); got != c.want {
			t.Errorf("Printable(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestHeaderPrintableJSONKeepsTheValue(t *testing.T) {
	text := "esc \x1b del \x7f c1 \u009b nl \n name \u540d\u524d"
	data, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		t.Fatal(err)
	}
	got := PrintableJSON(data)
	for _, raw := range []string{"\x1b", "\x7f", "\u009b"} {
		if strings.Contains(string(got), raw) {
			t.Fatalf("PrintableJSON(%q) = %q, holds raw %q", data, got, raw)
		}
	}
	var back map[string]string
	if err := json.Unmarshal(got, &back); err != nil || back["text"] != text {
		t.Fatalf("PrintableJSON gave %q; decoded %q, %v; want %q", got, back["text"], err, text)
	}
}
