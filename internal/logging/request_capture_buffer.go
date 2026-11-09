package logging

import (
	"strings"
	"unicode/utf8"
)

type CaptureCut struct {
	Offset  int    `json:"offset"`
	Omitted *int64 `json:"omitted"`
}

// CaptureBuffer retains at most one bound per side, split between head and tail.
type CaptureBuffer struct {
	data   []byte
	cursor int
	total  int64
}

func (b *CaptureBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.total += int64(n)
	take := min(len(p), CaptureBodyLimit-len(b.data))
	if len(b.data)+take > cap(b.data) {
		grown := make([]byte, len(b.data), min(CaptureBodyLimit, max(len(b.data)+take, 2*cap(b.data))))
		copy(grown, b.data)
		b.data = grown
	}
	b.data = append(b.data, p[:take]...)
	p = p[take:]
	half := CaptureBodyLimit / 2
	tail := b.data[min(half, len(b.data)):]
	if len(p) >= half {
		copy(tail, p[len(p)-half:])
		b.cursor = 0
	} else if len(p) > 0 {
		first := min(len(p), half-b.cursor)
		copy(tail[b.cursor:], p[:first])
		copy(tail, p[first:])
		b.cursor = (b.cursor + len(p)) % half
	}
	return n, nil
}
func (b *CaptureBuffer) Bytes() []byte {
	if b.cursor > 0 {
		tail := b.data[CaptureBodyLimit/2:]
		reverse := func(p []byte) {
			for i, j := 0, len(p)-1; i < j; i, j = i+1, j-1 {
				p[i], p[j] = p[j], p[i]
			}
		}
		reverse(tail[:b.cursor])
		reverse(tail[b.cursor:])
		reverse(tail)
		b.cursor = 0
	}
	return b.data
}
func (b *CaptureBuffer) Cut() *CaptureCut {
	omitted := b.total - int64(len(b.data))
	if omitted <= 0 {
		return nil
	}
	return &CaptureCut{Offset: CaptureBodyLimit / 2, Omitted: &omitted}
}
func captureText(body []byte, cut *CaptureCut) (string, *CaptureCut) {
	if cut == nil {
		return string(body), nil
	}
	head, tail := body[:cut.Offset], body[cut.Offset:]
	original := len(body)
	for len(head) > 0 {
		start := len(head) - 1
		for start > 0 && !utf8.RuneStart(head[start]) {
			start--
		}
		if utf8.FullRune(head[start:]) {
			break
		}
		head = head[:start]
	}
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	adjusted := *cut
	headText := strings.ToValidUTF8(string(head), "\uFFFD")
	adjusted.Offset = len(headText)
	if cut.Omitted != nil {
		n := *cut.Omitted + int64(original-len(head)-len(tail))
		adjusted.Omitted = &n
	}
	return headText + strings.ToValidUTF8(string(tail), "\uFFFD"), &adjusted
}
