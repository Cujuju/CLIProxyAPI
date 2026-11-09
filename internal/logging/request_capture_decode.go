package logging

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
)

// NormalizeCapture runs only in the spool worker, with bounded wire input/output.
func NormalizeCapture(r CaptureRecord) CaptureRecord {
	if r.Messages != nil {
		frames, partial, closed, reason := r.Messages.Snapshot()
		r.Format = "websocket"
		r.RequestType = "application/x-cpa-websocket-messages"
		r.ResponseType = r.RequestType
		r.RequestEncoding = ""
		r.ResponseEncoding = ""
		r.RequestDecoded = true
		r.ResponseDecoded = true
		r.Frames = frames
		var request, response strings.Builder
		for _, f := range frames {
			target := &response
			if f.Direction == "request" {
				target = &request
			}
			target.WriteString("Message ")
			target.WriteString(f.At)
			target.WriteString(" (" + f.Type + ")\n")
			target.WriteString(f.Payload)
			target.WriteString("\n\n")
		}
		r.Request = request.String()
		r.Response = response.String()
		if partial {
			r.State = "partial"
			r.Reason = reason
		} else if closed {
			r.State = "complete"
			r.Reason = "websocket-messages-finalized"
		} else {
			r.State = "pending"
			r.Reason = "websocket-open"
		}
		r.Messages = nil
	} else {
		r.Format = "http"
		if r.RawRequest != nil {
			r.Request, r.RequestDecoded, r.RequestCut = decodeCaptureBody(r.RawRequest, r.RequestEncoding, r.RequestCut, &r)
		}
		if r.RawResponse != nil {
			r.Response, r.ResponseDecoded, r.ResponseCut = decodeCaptureBody(r.RawResponse, r.ResponseEncoding, r.ResponseCut, &r)
		}
	}
	r.RawRequest = nil
	r.RawResponse = nil
	return r
}

func capturePartial(r *CaptureRecord, reason string) {
	r.State = "partial"
	if r.Reason == "" {
		r.Reason = reason
	} else if !strings.Contains(r.Reason, reason) {
		r.Reason += ";" + reason
	}
}

func decodeCaptureBody(raw []byte, encoding string, cut *CaptureCut, r *CaptureRecord) (string, bool, *CaptureCut) {
	encodings := strings.Split(strings.ToLower(strings.TrimSpace(encoding)), ",")
	fallback := func() (string, bool, *CaptureCut) {
		capturePartial(r, "unsupported-or-invalid-content-encoding")
		text := "Base64 wire body:\n" + base64.StdEncoding.EncodeToString(raw)
		if cut != nil {
			return text, false, &CaptureCut{Offset: len(text)}
		}
		return text, false, nil
	}
	if len(encodings) > 2 {
		return fallback()
	}
	encoded := strings.TrimSpace(encoding) != "" && strings.TrimSpace(encoding) != "identity"
	var reader io.Reader = bytes.NewReader(raw)
	if encoded && cut != nil {
		reader = bytes.NewReader(raw[:cut.Offset])
	}
	var closers []func()
	defer func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}()
	for i := len(encodings) - 1; i >= 0; i-- {
		switch strings.TrimSpace(encodings[i]) {
		case "", "identity":
		case "gzip", "x-gzip":
			d, err := gzip.NewReader(reader)
			if err != nil {
				return fallback()
			}
			reader = d
			closers = append(closers, func() { _ = d.Close() })
		case "zstd":
			d, err := zstd.NewReader(reader, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(CaptureBodyLimit), zstd.WithDecoderMaxWindow(CaptureBodyLimit))
			if err != nil {
				return fallback()
			}
			reader = d
			closers = append(closers, d.Close)
		default:
			return fallback()
		}
	}
	if encoded {
		var buffer CaptureBuffer
		var err error
		if cut != nil {
			_, err = io.Copy(&buffer, io.LimitReader(reader, CaptureBodyLimit))
		} else {
			_, err = io.Copy(&buffer, reader)
		}
		raw = buffer.Bytes()
		if len(raw) == 0 && err != nil {
			return fallback()
		}
		if cut != nil || err != nil {
			capturePartial(r, "encoded-body-incomplete")
			if middle := buffer.Cut(); middle != nil {
				raw = raw[:middle.Offset]
			}
			cut = &CaptureCut{Offset: len(raw)}
		} else {
			cut = buffer.Cut()
		}
	} else if len(raw) > CaptureBodyLimit {
		var buffer CaptureBuffer
		_, _ = buffer.Write(raw)
		raw, cut = buffer.Bytes(), buffer.Cut()
	}
	if cut != nil && encoded {
		capturePartial(r, "decoded-byte-limit")
	}
	text, cut := captureText(raw, cut)
	if !utf8.ValidString(text) {
		capturePartial(r, "invalid-utf8")
		text = strings.ToValidUTF8(text, "\uFFFD")
	}
	return text, true, cut
}
