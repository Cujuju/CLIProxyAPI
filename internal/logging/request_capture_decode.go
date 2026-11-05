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
			r.Request, r.RequestDecoded = decodeCaptureBody(r.RawRequest, r.RequestEncoding, &r)
		}
		if r.RawResponse != nil {
			r.Response, r.ResponseDecoded = decodeCaptureBody(r.RawResponse, r.ResponseEncoding, &r)
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

func decodeCaptureBody(raw []byte, encoding string, r *CaptureRecord) (string, bool) {
	body := raw
	encodings := strings.Split(strings.ToLower(strings.TrimSpace(encoding)), ",")
	if len(encodings) > 2 {
		capturePartial(r, "unsupported-content-encoding")
		return "Base64 wire body:\n" + base64.StdEncoding.EncodeToString(raw), false
	}
	for i := len(encodings) - 1; i >= 0; i-- {
		var reader io.Reader
		var closeReader func()
		switch strings.TrimSpace(encodings[i]) {
		case "", "identity":
			continue
		case "gzip", "x-gzip":
			decoder, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				capturePartial(r, "gzip-decode-failed")
				return "Base64 wire body:\n" + base64.StdEncoding.EncodeToString(raw), false
			}
			reader = decoder
			closeReader = func() { _ = decoder.Close() }
		case "zstd":
			decoder, err := zstd.NewReader(bytes.NewReader(body), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(8<<20), zstd.WithDecoderMaxWindow(8<<20))
			if err != nil {
				capturePartial(r, "zstd-decode-failed")
				return "Base64 wire body:\n" + base64.StdEncoding.EncodeToString(raw), false
			}
			reader = decoder
			closeReader = decoder.Close
		default:
			capturePartial(r, "unsupported-content-encoding")
			return "Base64 wire body:\n" + base64.StdEncoding.EncodeToString(raw), false
		}
		decoded, err := io.ReadAll(io.LimitReader(reader, CaptureBodyLimit+1))
		closeReader()
		if err != nil {
			capturePartial(r, "encoded-body-incomplete")
		}
		if len(decoded) > CaptureBodyLimit {
			decoded = decoded[:CaptureBodyLimit]
			capturePartial(r, "decoded-byte-limit")
		}
		if len(decoded) == 0 && err != nil {
			return "Base64 wire body:\n" + base64.StdEncoding.EncodeToString(raw), false
		}
		body = decoded
	}
	if len(body) > CaptureBodyLimit {
		body = body[:CaptureBodyLimit]
		capturePartial(r, "decoded-byte-limit")
	}
	if !utf8.Valid(body) {
		capturePartial(r, "invalid-utf8")
	}
	return strings.ToValidUTF8(string(body), "�"), true
}
