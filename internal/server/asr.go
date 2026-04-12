package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

const (
	dashscopeWSURL     = "wss://dashscope.aliyuncs.com/api-ws/v1/inference/"
	defaultASRModel    = "qwen3-asr-flash"
	defaultSampleRate  = 16000
)

// DashScope WebSocket protocol structures
type dsHeader struct {
	Action    string `json:"action"`
	TaskID    string `json:"task_id"`
	Streaming string `json:"streaming,omitempty"`
	Event     string `json:"event,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	ErrorMsg  string `json:"error_message,omitempty"`
}

type dsPayload struct {
	TaskGroup  string         `json:"task_group,omitempty"`
	Task       string         `json:"task,omitempty"`
	Function   string         `json:"function,omitempty"`
	Model      string         `json:"model,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
	Input      map[string]any `json:"input,omitempty"`
	Output     *dsOutput      `json:"output,omitempty"`
}

type dsOutput struct {
	Sentence *dsSentence `json:"sentence,omitempty"`
}

type dsSentence struct {
	Text     string `json:"text"`
	EndTime  int    `json:"end_time"`
	BeginTime int   `json:"begin_time"`
}

type dsMessage struct {
	Header  dsHeader  `json:"header"`
	Payload dsPayload `json:"payload"`
}

// handleASRWebSocket proxies audio between browser and DashScope ASR.
//
// Browser → (PCM audio binary) → this server → DashScope WS
// DashScope WS → (transcription JSON) → this server → Browser
func (s *Server) handleASRWebSocket(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ASRAPIKey == "" {
		http.Error(w, "ASR not configured", http.StatusServiceUnavailable)
		return
	}

	// Upgrade browser connection
	clientConn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[asr] client upgrade failed: %v", err)
		return
	}
	defer clientConn.Close()

	model := s.cfg.ASRModel
	if model == "" {
		model = defaultASRModel
	}
	baseURL := s.cfg.ASRBaseURL
	if baseURL == "" {
		baseURL = dashscopeWSURL
	}

	// Connect to DashScope
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	headers := http.Header{}
	headers.Set("Authorization", "bearer "+s.cfg.ASRAPIKey)
	headers.Set("X-DashScope-DataInspection", "enable")

	dsConn, _, err := dialer.Dial(baseURL, headers)
	if err != nil {
		log.Printf("[asr] dashscope connect failed: %v", err)
		clientConn.WriteJSON(map[string]string{"type": "error", "text": "ASR 服务连接失败"})
		return
	}
	defer dsConn.Close()

	taskID := uuid.New().String()

	// Send run-task to DashScope
	runTask := dsMessage{
		Header: dsHeader{
			Action:    "run-task",
			TaskID:    taskID,
			Streaming: "duplex",
		},
		Payload: dsPayload{
			TaskGroup: "audio",
			Task:      "asr",
			Function:  "recognition",
			Model:     model,
			Parameters: map[string]any{
				"format":      "pcm",
				"sample_rate": defaultSampleRate,
			},
			Input: map[string]any{},
		},
	}
	if err := dsConn.WriteJSON(runTask); err != nil {
		log.Printf("[asr] run-task send failed: %v", err)
		clientConn.WriteJSON(map[string]string{"type": "error", "text": "ASR 初始化失败"})
		return
	}

	// Wait for task-started event
	var startResp dsMessage
	if err := dsConn.ReadJSON(&startResp); err != nil {
		log.Printf("[asr] task-started read failed: %v", err)
		clientConn.WriteJSON(map[string]string{"type": "error", "text": "ASR 启动失败"})
		return
	}
	if startResp.Header.Event != "task-started" {
		log.Printf("[asr] unexpected event: %s (code=%s msg=%s)",
			startResp.Header.Event, startResp.Header.ErrorCode, startResp.Header.ErrorMsg)
		clientConn.WriteJSON(map[string]string{"type": "error", "text": fmt.Sprintf("ASR 启动异常: %s", startResp.Header.ErrorMsg)})
		return
	}

	// Tell browser we're ready
	clientConn.WriteJSON(map[string]string{"type": "ready"})

	var wg sync.WaitGroup
	done := make(chan struct{})

	// Goroutine 1: Read audio from browser → forward to DashScope
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			mt, data, err := clientConn.ReadMessage()
			if err != nil {
				// Client disconnected or error
				break
			}
			if mt == websocket.TextMessage {
				// Control message from client
				var ctrl map[string]string
				json.Unmarshal(data, &ctrl)
				if ctrl["type"] == "stop" {
					// Send finish-task
					finishMsg := dsMessage{
						Header: dsHeader{
							Action:    "finish-task",
							TaskID:    taskID,
						},
						Payload: dsPayload{Input: map[string]any{}},
					}
					dsConn.WriteJSON(finishMsg)
					break
				}
			} else if mt == websocket.BinaryMessage {
				// Audio data → forward to DashScope
				if err := dsConn.WriteMessage(websocket.BinaryMessage, data); err != nil {
					break
				}
			}
		}
	}()

	// Goroutine 2: Read transcription from DashScope → forward to browser
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		for {
			var resp dsMessage
			if err := dsConn.ReadJSON(&resp); err != nil {
				break
			}
			switch resp.Header.Event {
			case "result-generated":
				if resp.Payload.Output != nil && resp.Payload.Output.Sentence != nil {
					clientConn.WriteJSON(map[string]any{
						"type": "partial",
						"text": resp.Payload.Output.Sentence.Text,
					})
				}
			case "task-finished":
				clientConn.WriteJSON(map[string]string{"type": "done"})
				return
			case "task-failed":
				clientConn.WriteJSON(map[string]string{
					"type": "error",
					"text": resp.Header.ErrorMsg,
				})
				return
			}
		}
	}()

	<-done
	wg.Wait()
}
