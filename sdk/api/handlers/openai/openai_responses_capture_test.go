package openai

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

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
