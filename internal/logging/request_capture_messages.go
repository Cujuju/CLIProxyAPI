package logging

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const CaptureMessagesContextKey = "CPA_CAPTURE_MESSAGES"
const CaptureFrameLimit = 64 << 10
const CaptureFrameCount = 128

type CaptureFrame struct {
	Index     int    `json:"index"`
	Direction string `json:"direction"`
	At        string `json:"at"`
	Type      string `json:"type"`
	Payload   string `json:"payload"`
	Truncated bool   `json:"truncated"`
}
type rawCaptureFrame struct {
	index     int
	direction string
	at        time.Time
	kind      int
	payload   []byte
	truncated bool
}

// CaptureMessages only copies bounded application-message prefixes on the socket path.
// JSON/text normalization and serialization happen in the spool worker.
type CaptureMessages struct {
	mu      sync.Mutex
	frames  []rawCaptureFrame
	bytes   map[string]int
	seen    int
	partial bool
	closed  bool
	reason  string
	notify  func()
}

func NewCaptureMessages(notify func()) *CaptureMessages {
	return &CaptureMessages{bytes: map[string]int{}, notify: notify}
}
func CaptureMessagesFromGin(c *gin.Context) *CaptureMessages {
	if c == nil || !c.GetBool("CPA_ARCHIVE_AUTHENTICATED") {
		return nil
	}
	v, _ := c.Get(CaptureMessagesContextKey)
	m, _ := v.(*CaptureMessages)
	return m
}
func (m *CaptureMessages) Append(direction string, kind int, payload []byte) {
	if m == nil || (direction != "request" && direction != "response") || (kind != 1 && kind != 2) {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.seen++
	if len(m.frames) >= CaptureFrameCount {
		notify := m.notify
		changed := !m.partial
		m.partial = true
		m.reason = "websocket-frame-count-limit"
		m.mu.Unlock()
		if changed && notify != nil {
			notify()
		}
		return
	}
	remaining := CaptureBodyLimit - m.bytes[direction]
	if remaining <= 0 {
		notify := m.notify
		changed := !m.partial
		m.partial = true
		m.reason = "websocket-byte-limit"
		m.mu.Unlock()
		if changed && notify != nil {
			notify()
		}
		return
	}
	limit := min(len(payload), CaptureFrameLimit, remaining)
	truncated := limit < len(payload)
	if truncated {
		m.partial = true
		m.reason = "websocket-frame-or-byte-limit"
	}
	m.frames = append(m.frames, rawCaptureFrame{m.seen, direction, time.Now().UTC(), kind, bytesCloneCapture(payload[:limit]), truncated})
	m.bytes[direction] += limit
	notify := m.notify
	m.mu.Unlock()
	if notify != nil {
		notify()
	}
}
func bytesCloneCapture(b []byte) []byte { return append([]byte(nil), b...) }
func (m *CaptureMessages) Close(partial bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if partial {
		m.partial = true
		m.reason = "websocket-disconnected"
	}
}
func (m *CaptureMessages) WriteError() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.partial = true
	m.reason = "websocket-write-failed"
}
func (m *CaptureMessages) Snapshot() ([]CaptureFrame, bool, bool, string) {
	m.mu.Lock()
	raw := append([]rawCaptureFrame(nil), m.frames...)
	partial, closed, reason := m.partial, m.closed, m.reason
	m.mu.Unlock()
	frames := make([]CaptureFrame, 0, len(raw))
	for _, f := range raw {
		kind := "text"
		if f.kind == 2 {
			kind = "binary"
		}
		if !utf8.Valid(f.payload) {
			partial = true
			reason = "websocket-invalid-utf8"
		}
		frames = append(frames, CaptureFrame{f.index, f.direction, f.at.Format(time.RFC3339Nano), kind, strings.ToValidUTF8(string(f.payload), "�"), f.truncated})
	}
	return frames, partial, closed, reason
}
