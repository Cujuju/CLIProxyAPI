package logging

import (
	"bytes"
	"compress/gzip"
	"strings"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func gzipCapture(t *testing.T, p []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(p); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func zstdCapture(t *testing.T, p []byte) []byte {
	t.Helper()
	w, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	return w.EncodeAll(p, nil)
}

func TestNormalizeCaptureReadableHTTPEncodings(t *testing.T) {
	text := []byte(`{"input":"Claude and Codex readable prompt"}`)
	for _, test := range []struct {
		encoding string
		wire     []byte
	}{
		{"", text}, {"gzip", gzipCapture(t, text)}, {"zstd", zstdCapture(t, text)}, {"gzip, zstd", zstdCapture(t, gzipCapture(t, text))},
	} {
		r := NormalizeCapture(CaptureRecord{State: "complete", RawRequest: test.wire, RawResponse: test.wire, RequestEncoding: test.encoding, ResponseEncoding: test.encoding})
		if r.Request != string(text) || r.Response != string(text) || !r.RequestDecoded || !r.ResponseDecoded || r.State != "complete" || r.RawRequest != nil {
			t.Fatalf("%s: state=%s reason=%s text=%q", test.encoding, r.State, r.Reason, r.Request)
		}
	}
}
func TestNormalizeCaptureDecodedLimitsCorruptionAndUnsupported(t *testing.T) {
	for _, encoding := range []string{"gzip", "zstd"} {
		expanded := bytes.Repeat([]byte("x"), CaptureBodyLimit*8)
		compressed := gzipCapture(t, expanded)
		if encoding == "zstd" {
			compressed = zstdCapture(t, expanded)
		}
		r := NormalizeCapture(CaptureRecord{State: "complete", RawRequest: compressed, RequestEncoding: encoding})
		if len(r.Request) != CaptureBodyLimit || r.State != "partial" || !strings.Contains(r.Reason, "decoded-byte-limit") {
			t.Fatalf("%s: %d %s", encoding, len(r.Request), r.Reason)
		}
	}
	corrupt := gzipCapture(t, []byte("readable prefix"))
	corrupt = corrupt[:len(corrupt)-5]
	r := NormalizeCapture(CaptureRecord{State: "complete", RawRequest: corrupt, RequestEncoding: "gzip"})
	if r.State != "partial" || !strings.Contains(r.Request, "readable prefix") {
		t.Fatalf("corrupt stream not partial: %+v", r)
	}
	r = NormalizeCapture(CaptureRecord{State: "complete", RawRequest: []byte{0xff, 1, 2}, RequestEncoding: "br"})
	if r.State != "partial" || r.RequestDecoded || !strings.Contains(r.Request, "Base64 wire body") {
		t.Fatal("unsupported encoding misrepresented")
	}
}
func TestCaptureMessagesBoundsLiveRevisionsAndImmutablePrefixes(t *testing.T) {
	notifications := 0
	m := NewCaptureMessages(func() { notifications++ })
	p := []byte(`{"type":"response.create","input":"readable"}`)
	m.Append("request", 1, p)
	p[0] = 'x'
	m.Append("response", 1, []byte(`{"type":"response.output_text.delta","delta":"hello"}`))
	r := NormalizeCapture(CaptureRecord{Messages: m})
	if r.State != "pending" || r.Format != "websocket" || len(r.Frames) != 2 || r.Frames[0].Payload[0] != '{' || notifications != 2 {
		t.Fatalf("bad live capture: %+v", r)
	}
	m.Close(false)
	r = NormalizeCapture(CaptureRecord{Messages: m})
	if r.State != "complete" {
		t.Fatal("clean close not finalized")
	}
	m = NewCaptureMessages(nil)
	for i := 0; i < 10; i++ {
		m.Append("request", 1, bytes.Repeat([]byte("a"), CaptureFrameLimit+1))
	}
	r = NormalizeCapture(CaptureRecord{Messages: m})
	total := 0
	for _, f := range r.Frames {
		total += len(f.Payload)
		if len(f.Payload) > CaptureFrameLimit {
			t.Fatal("frame cap exceeded")
		}
	}
	if total > CaptureBodyLimit || r.State != "partial" {
		t.Fatal("direction byte limit not enforced")
	}
	m = NewCaptureMessages(nil)
	for i := 0; i < CaptureFrameCount+1; i++ {
		m.Append("response", 1, []byte("x"))
	}
	r = NormalizeCapture(CaptureRecord{Messages: m})
	if len(r.Frames) != CaptureFrameCount || r.State != "partial" {
		t.Fatal("frame count not bounded")
	}
}
func TestCaptureWebsocketPermitsCannotStarveHTTP(t *testing.T) {
	s := testCaptureSpool()
	s.Lease(true)
	for i := 0; i < 16; i++ {
		if _, ok := s.BeginWebsocket(); !ok {
			t.Fatal("WS permit missing")
		}
	}
	if _, ok := s.BeginWebsocket(); ok {
		t.Fatal("WS active limit missing")
	}
	if _, ok := s.Begin(); !ok {
		t.Fatal("long WS sessions starved HTTP capture")
	}
}

func TestCaptureMessagesConcurrentSnapshots(t *testing.T) {
	m := NewCaptureMessages(nil)
	var wg sync.WaitGroup
	for _, direction := range []string{"request", "response"} {
		wg.Add(1)
		go func(d string) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				m.Append(d, 1, []byte(`{"delta":"text"}`))
			}
		}(direction)
	}
	for i := 0; i < 100; i++ {
		_ = NormalizeCapture(CaptureRecord{Messages: m})
	}
	wg.Wait()
	m.Close(false)
	r := NormalizeCapture(CaptureRecord{Messages: m})
	if len(r.Frames) != CaptureFrameCount || r.State != "partial" {
		t.Fatalf("unsafe concurrent capture: %d %s", len(r.Frames), r.State)
	}
}
