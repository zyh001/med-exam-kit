package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zyh001/med-exam-kit/internal/logger"
)

// examSessionDisk is the JSON-serialisable form of an in-memory examSession.
type examSessionDisk struct {
	Answers    map[string]examAnswer `json:"answers"`
	Ts         int64                 `json:"ts"`
	StartedAt  int64                 `json:"started_at"`
	TimeLimit  int                   `json:"time_limit"`
	RevealedAt int64                 `json:"revealed_at"`
}

// examSessionsFile returns the path of the persistence file.
// It lives next to the config file, or in the working directory if configPath is empty.
func (s *Server) examSessionsFile() string {
	base := "exam-sessions.json"
	if s.configPath != "" {
		return filepath.Join(filepath.Dir(s.configPath), base)
	}
	return base
}

// persistExamSessions writes current active exam sessions to disk (best-effort).
// Call with examMu held or from a path that already serialises access.
func (s *Server) persistExamSessions() {
	s.examMu.Lock()
	m := make(map[string]examSessionDisk, len(s.examSessions))
	for id, sess := range s.examSessions {
		m[id] = examSessionDisk{
			Answers:    sess.answers,
			Ts:         sess.ts,
			StartedAt:  sess.startedAt,
			TimeLimit:  sess.timeLimit,
			RevealedAt: sess.revealedAt,
		}
	}
	s.examMu.Unlock()

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		logger.Errorf("[exam-persist] 序列化失败: %v", err)
		return
	}
	path := s.examSessionsFile()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		logger.Errorf("[exam-persist] 写入临时文件失败: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logger.Errorf("[exam-persist] rename 失败: %v", err)
		return
	}
}

// loadExamSessions reads persisted sessions back into memory on startup,
// discarding stale sessions (>24h or already revealed+past grace window).
func (s *Server) loadExamSessions() {
	path := s.examSessionsFile()
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warnf("[exam-persist] 读取持久化文件失败: %v", err)
		}
		return
	}

	var m map[string]examSessionDisk
	if err := json.Unmarshal(data, &m); err != nil {
		logger.Warnf("[exam-persist] 解析持久化文件失败: %v", err)
		return
	}

	nowS := time.Now().Unix()
	loaded := 0
	s.examMu.Lock()
	for id, d := range m {
		// 过期或已超出宽限窗口的 session 直接跳过
		if nowS-d.Ts > 86400 {
			continue
		}
		if d.RevealedAt != 0 && nowS-d.RevealedAt > int64(revealGraceWindow) {
			continue
		}
		s.examSessions[id] = &examSession{
			answers:    d.Answers,
			ts:         d.Ts,
			startedAt:  d.StartedAt,
			timeLimit:  d.TimeLimit,
			revealedAt: d.RevealedAt,
		}
		loaded++
	}
	s.examMu.Unlock()

	if loaded > 0 {
		logger.Infof("[exam-persist] 从磁盘恢复 %d 个考试会话", loaded)
	}
}

// activeExamCount returns the number of in-progress (not yet revealed) sessions.
func (s *Server) activeExamCount() int {
	s.examMu.Lock()
	defer s.examMu.Unlock()
	n := 0
	for _, sess := range s.examSessions {
		if sess.revealedAt == 0 {
			n++
		}
	}
	return n
}

// examSessionsSummary returns a human-readable summary for the shutdown warning.
func (s *Server) examSessionsSummary() string {
	s.examMu.Lock()
	defer s.examMu.Unlock()
	active := 0
	for _, sess := range s.examSessions {
		if sess.revealedAt == 0 {
			active++
		}
	}
	return fmt.Sprintf("%d 场考试正在进行中（答案已持久化到磁盘，重启后仍可恢复）", active)
}
