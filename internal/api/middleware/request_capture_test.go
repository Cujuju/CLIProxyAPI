package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

type testCaptureSink struct{ records []logging.CaptureRecord }

func (s *testCaptureSink) Begin() (uint64, bool)                    { return 1, true }
func (s *testCaptureSink) Finish(_ uint64, r logging.CaptureRecord) { s.records = append(s.records, r) }

func TestCaptureMiddlewareBoundsBodiesAndPreservesWireResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := &testCaptureSink{}
	router := gin.New()
	router.Use(func(c *gin.Context) { logging.SetGinRequestID(c, "full-inbound-uuid"); c.Next() })
	router.Use(RequestCaptureMiddleware(sink))
	router.POST("/v1/responses", func(c *gin.Context) {
		CaptureAuthenticatedRequestBody(c)
		body, _ := io.ReadAll(c.Request.Body)
		c.Data(200, "application/json", body)
	})
	payload := strings.Repeat("x", logging.CaptureBodyLimit+123)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses?api_key=must-not-capture", strings.NewReader(payload))
	req.Header.Set("Authorization", "secret")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != 200 || res.Body.String() != payload {
		t.Fatal("capture changed response")
	}
	if len(sink.records) != 1 {
		t.Fatalf("captures=%d", len(sink.records))
	}
	r := sink.records[0]
	if r.TraceID != "full-inbound-uuid" || r.State != "partial" || r.Reason != "byte-limit" || len(r.Request) != logging.CaptureBodyLimit || len(r.Response) != logging.CaptureBodyLimit {
		t.Fatalf("bad capture: state=%s reason=%s req=%d resp=%d", r.State, r.Reason, len(r.Request), len(r.Response))
	}
	if strings.Contains(r.Path, "api_key") {
		t.Fatal("query secret captured")
	}
}

func TestCaptureMiddlewareUnauthenticatedAndWebsocketAreExplicit(t *testing.T) {
	for _, ws := range []bool{false, true} {
		sink := &testCaptureSink{}
		router := gin.New()
		router.Use(func(c *gin.Context) { logging.SetGinRequestID(c, "full-inbound-uuid"); c.Next() })
		router.Use(RequestCaptureMiddleware(sink))
		router.POST("/v1/responses", func(c *gin.Context) {
			if ws {
				CaptureAuthenticatedRequestBody(c)
			}
			c.String(401, "secret error")
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("secret prompt"))
		if ws {
			req.Header.Set("Upgrade", "websocket")
		}
		router.ServeHTTP(httptest.NewRecorder(), req)
		r := sink.records[0]
		if r.Request != "" || r.Response != "" {
			t.Fatal("unsupported/unauthenticated payload retained")
		}
		if ws && r.Reason != "websocket-frames-not-captured" || !ws && r.Reason != "not-authenticated" {
			t.Fatalf("wrong missing state: %+v", r)
		}
	}
}

func TestCaptureMiddlewareMarksUnreadBodyAndWriteString(t *testing.T) {
	sink := &testCaptureSink{}
	router := gin.New()
	router.Use(func(c *gin.Context) { logging.SetGinRequestID(c, "full-inbound-uuid"); c.Next() })
	router.Use(RequestCaptureMiddleware(sink))
	router.POST("/v1/responses", func(c *gin.Context) { CaptureAuthenticatedRequestBody(c); _, _ = c.Writer.WriteString("hello") })
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("not consumed")))
	if r := sink.records[0]; r.Response != "hello" || r.Reason != "request-not-fully-consumed" {
		t.Fatalf("bad record: %+v", r)
	}
}
