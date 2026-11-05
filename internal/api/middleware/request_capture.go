package middleware

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

type boundedCapture struct {
	bytes.Buffer
	truncated bool
}

func (b *boundedCapture) add(p []byte) {
	n := min(len(p), logging.CaptureBodyLimit-b.Len())
	if n < len(p) {
		b.truncated = true
	}
	_, _ = b.Buffer.Write(p[:n])
}

type captureReader struct {
	io.ReadCloser
	body *boundedCapture
	eof  bool
	read int64
}

func (r *captureReader) Read(p []byte) (int, error) {
	n, e := r.ReadCloser.Read(p)
	r.body.add(p[:n])
	r.read += int64(n)
	if e == io.EOF {
		r.eof = true
	}
	return n, e
}

type captureWriter struct {
	gin.ResponseWriter
	body        *boundedCapture
	hijacked    bool
	writeFailed bool
}

func (w *captureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *captureWriter) Write(p []byte) (int, error) {
	n, e := w.ResponseWriter.Write(p)
	w.body.add(p[:n])
	if e != nil || n != len(p) {
		w.writeFailed = true
	}
	return n, e
}
func (w *captureWriter) WriteString(p string) (int, error) {
	n, e := w.ResponseWriter.WriteString(p)
	limit := min(n, logging.CaptureBodyLimit-w.body.Len())
	w.body.add([]byte(p[:limit]))
	if limit < n {
		w.body.truncated = true
	}
	if e != nil || n != len(p) {
		w.writeFailed = true
	}
	return n, e
}
func (w *captureWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	return w.ResponseWriter.Hijack()
}

const archiveAuthenticatedKey = "CPA_ARCHIVE_AUTHENTICATED"

// RequestCaptureMiddleware observes consumed downstream bytes. It never reads ahead,
// writes disk, waits on a queue, or reads authentication headers.
type captureSink interface {
	Begin() (uint64, bool)
	Finish(uint64, logging.CaptureRecord)
}

func RequestCaptureMiddleware(spool captureSink) gin.HandlerFunc {
	return func(c *gin.Context) {
		trace := logging.GetGinRequestID(c)
		if trace == "" || !shouldLogRequest(c.Request.URL.Path) {
			c.Next()
			return
		}
		generation, enabled := spool.Begin()
		if !enabled {
			c.Next()
			return
		}
		r := logging.CaptureRecord{ID: uuid.NewString(), TraceID: trace, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Method: c.Request.Method, Path: c.Request.URL.Path, State: "complete", RequestType: c.GetHeader("Content-Type"), RequestEncoding: c.GetHeader("Content-Encoding")}
		r.Method = boundedCaptureField(r.Method, 16)
		r.Path = boundedCaptureField(r.Path, 2048)
		r.RequestType = boundedCaptureField(r.RequestType, 256)
		r.RequestEncoding = boundedCaptureField(r.RequestEncoding, 64)
		var request, response boundedCapture
		var reader *captureReader
		if c.Request.Body != nil {
			reader = &captureReader{ReadCloser: c.Request.Body, body: &request}
			c.Request.Body = reader
		}
		writer := &captureWriter{ResponseWriter: c.Writer, body: &response}
		c.Writer = writer
		defer func() {
			panicValue := recover()
			r.Status = writer.Status()
			r.ResponseType = writer.Header().Get("Content-Type")
			r.ResponseEncoding = writer.Header().Get("Content-Encoding")
			r.ResponseType = boundedCaptureField(r.ResponseType, 256)
			r.ResponseEncoding = boundedCaptureField(r.ResponseEncoding, 64)
			if !c.GetBool(archiveAuthenticatedKey) {
				r.State = "missing"
				r.Reason = "not-authenticated"
			} else if writer.hijacked || strings.EqualFold(c.GetHeader("Upgrade"), "websocket") {
				r.State = "partial"
				r.Reason = "websocket-frames-not-captured"
			} else {
				r.Request = strings.ToValidUTF8(request.String(), "�")
				r.Response = strings.ToValidUTF8(response.String(), "�")
				if request.truncated || response.truncated {
					r.State = "partial"
					r.Reason = "byte-limit"
				}
				if reader != nil && !reader.eof && (c.Request.ContentLength < 0 || reader.read < c.Request.ContentLength) {
					r.State = "partial"
					r.Reason = "request-not-fully-consumed"
				}
				if !utf8.Valid(request.Bytes()) || !utf8.Valid(response.Bytes()) {
					r.State = "partial"
					r.Reason = "binary-or-incomplete-utf8"
				}
				if c.Request.Context().Err() != nil {
					r.State = "partial"
					r.Reason = "connection-cancelled"
				}
				if writer.writeFailed {
					r.State = "partial"
					r.Reason = "response-write-failed"
				}
				if r.RequestEncoding != "" || r.ResponseEncoding != "" {
					r.State = "partial"
					r.Reason = "encoded-wire-body"
				}
			}
			if panicValue != nil {
				r.State = "partial"
				r.Reason = "handler-panic"
			}
			spool.Finish(generation, r)
			if panicValue != nil {
				panic(panicValue)
			}
		}()
		c.Next()
	}
}

var _ http.Hijacker = (*captureWriter)(nil)

func boundedCaptureField(value string, limit int) string {
	if len(value) > limit {
		value = value[:limit]
	}
	return strings.ToValidUTF8(value, "�")
}
