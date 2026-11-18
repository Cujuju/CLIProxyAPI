package openai

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

func TestResponsesWebsocketCaptureFinalizesFromCloseHandshake(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name     string
		graceful bool
		state    string
	}{
		{"normal-close", true, "complete"},
		{"interrupted-transport", false, "partial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spool := logging.NewDurableCaptureSpool(t.TempDir(), randBytesForCaptureTest(t))
			if err := spool.Lease(true); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = spool.Close() })
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil))
			done := make(chan struct{})
			router := gin.New()
			router.Use(func(c *gin.Context) {
				defer close(done)
				logging.SetGinRequestID(c, "test-capture-trace")
				c.Next()
			}, middleware.RequestCaptureMiddleware(spool))
			router.GET("/v1/responses", func(c *gin.Context) {
				ctx, cancel := context.WithCancel(c.Request.Context())
				defer cancel()
				c.Request = c.Request.WithContext(ctx)
				middleware.CaptureAuthenticatedRequestBody(c)
				h.ResponsesWebsocket(c)
			})
			server := httptest.NewServer(router)
			t.Cleanup(server.Close)
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			payload := []byte{0, 0xff, 0x80}
			if err := conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
				t.Fatal(err)
			}
			const testWait = 5 * time.Second
			if err := conn.SetReadDeadline(time.Now().Add(testWait)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := conn.ReadMessage(); err != nil {
				t.Fatal(err)
			}
			if tc.graceful {
				if err := conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Time{}); err != nil {
					t.Fatal(err)
				}
			} else {
				_ = conn.Close()
			}
			select {
			case <-done:
			case <-time.After(testWait):
				t.Fatal("capture did not finalize")
			}
			events := spool.Page(0).Records
			if len(events) != 4 || events[3].Event != "end" || events[3].State != tc.state {
				t.Fatalf("expected %s terminal capture, got %+v", tc.state, events)
			}
			if chunk := events[1].Chunk; chunk == nil || chunk.Type != "binary" || !bytes.Equal(chunk.Data, payload) {
				t.Fatalf("binary request was not preserved: %+v", chunk)
			}
		})
	}
}

func randBytesForCaptureTest(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestResponsesWebsocketCaptureOptInCompressedMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, enabled := range []bool{false, true} {
		h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil))
		capture := logging.NewCaptureMessages(nil)
		done := make(chan struct{})
		router := gin.New()
		router.GET("/v1/responses", func(c *gin.Context) {
			if enabled {
				c.Set(logging.CaptureMessagesContextKey, capture)
				c.Set("CPA_ARCHIVE_AUTHENTICATED", true)
			}
			h.ResponsesWebsocket(c)
			close(done)
		})
		server := httptest.NewServer(router)
		dialer := websocket.Dialer{EnableCompression: true}
		conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		conn.EnableWriteCompression(true)
		// Invalid message generates a real downstream protocol error without model/provider traffic.
		payload := []byte(`{"type":"unsupported","input":"readable Codex message"}`)
		if err = conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			t.Fatal(err)
		}
		_, response, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if len(response) == 0 {
			t.Fatal("expected protocol response")
		}
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Time{})
		_ = conn.Close()
		<-done
		server.Close()
		r := logging.NormalizeCapture(logging.CaptureRecord{Messages: capture})
		if enabled {
			if len(r.Frames) != 2 || r.Frames[0].Payload != string(payload) || r.Frames[1].Payload != string(response) || r.Frames[1].Direction != "response" {
				t.Fatalf("messages not captured: %+v", r.Frames)
			}
		} else if len(r.Frames) != 0 {
			t.Fatal("payload captured without opt-in")
		}
	}
}
