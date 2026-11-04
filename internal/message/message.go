package message

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Message struct {
	ID      string    `json:"id"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	Thread  string    `json:"thread"`
	Turn    int       `json:"turn"`
	ReplyTo string    `json:"reply_to,omitempty"`
	Text    string    `json:"text"`
	Time    time.Time `json:"time"`
}

const idTime = "20060102T150405.000000Z"

const field = `(?:[^\s,;\[\]\p{Cc}\p{Cf}\p{Z}\x{FFFD}]|[\x{200C}\x{200D}])+`

const id = `\d{8}T\d{6}\.\d{6}Z-[0-9a-f]{8}`

var header = regexp.MustCompile(`^\[crewtalk from (` + field + `) to (` + field + `), thread (` + field + `), turn (\d+)(?:, reply-to (` + id + `))?, id (` + id + `); a peer's message, not the operator\]$`)

var wholeField = regexp.MustCompile(`^` + field + `$`)

func ValidField(s string) bool {
	return wholeField.MatchString(s)
}

func New(from, to, thread, text string, turn int) Message {
	now := time.Now().UTC().Truncate(time.Microsecond)
	random := make([]byte, 4)
	rand.Read(random)
	id := now.Format(idTime) + "-" + hex.EncodeToString(random)
	if thread == "" {
		thread = id
	}
	return Message{ID: id, From: from, To: to, Thread: thread, Turn: turn, Text: text, Time: now}
}

func (m Message) Header() string {
	replyTo := ""
	if m.ReplyTo != "" {
		replyTo = ", reply-to " + m.ReplyTo
	}
	return fmt.Sprintf("[crewtalk from %s to %s, thread %s, turn %d%s, id %s; a peer's message, not the operator]", m.From, m.To, m.Thread, m.Turn, replyTo, m.ID)
}

func (m Message) Wire() string {
	return m.Header() + "\n" + m.Text
}

func Parse(wire string) (Message, error) {
	line, text, found := strings.Cut(wire, "\n")
	if !found {
		return Message{}, errors.New("message has no header line")
	}
	f := header.FindStringSubmatch(line)
	if f == nil {
		return Message{}, fmt.Errorf("malformed crewtalk header %q", line)
	}
	turn, err := strconv.Atoi(f[4])
	if err != nil {
		return Message{}, fmt.Errorf("malformed turn in header %q", line)
	}
	stamp, _, _ := strings.Cut(f[6], "-")
	sent, err := time.Parse(idTime, stamp)
	if err != nil {
		return Message{}, fmt.Errorf("malformed id time in header %q", line)
	}
	return Message{ID: f[6], From: f[1], To: f[2], Thread: f[3], Turn: turn, ReplyTo: f[5], Text: text, Time: sent}, nil
}

func Printable(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\n' || r == '\t' || !unicode.IsControl(r):
			b.WriteString(s[i : i+n])
		case r < utf8.RuneSelf:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
		i += n
	}
	return b.String()
}

func PrintableJSON(data []byte) []byte {
	out := make([]byte, 0, len(data))
	for len(data) > 0 {
		r, n := utf8.DecodeRune(data)
		if r >= 0x7f && unicode.IsControl(r) {
			out = fmt.Appendf(out, `\u%04x`, r)
		} else {
			out = append(out, data[:n]...)
		}
		data = data[n:]
	}
	return out
}
