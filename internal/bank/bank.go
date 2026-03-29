// Package bank implements the .mqb question-bank file format (MQB2).
//
// File layout (same as Python bank.py):
//
//	4 bytes  magic  "MQB2"
//	4 bytes  meta_len  (big-endian uint32)
//	N bytes  meta JSON  {count, created, encrypted, compressed, salt_hex}
//	M bytes  payload   JSON → zlib → optional Fernet
package bank

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/zyh001/med-exam-kit/internal/models"
)

var (
	magicV2 = []byte("MQB2")
	magicV1 = []byte("MQB1")
)

type bankMeta struct {
	Count      int     `json:"count"`
	Created    float64 `json:"created"`
	Encrypted  bool    `json:"encrypted"`
	Compressed bool    `json:"compressed"`
	SaltHex    string  `json:"salt_hex"`
}

// SaveBank serialises questions to a .mqb file.
// password == "" disables encryption.
// compress == true enables zlib (default: true).
func SaveBank(questions []*models.Question, path, password string, compress bool, compressLevel int) (string, error) {
	outPath := replaceExt(path, ".mqb")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return "", err
	}

	// 1. JSON serialise (strip Raw field)
	type subqJSON struct {
		Text         string   `json:"text"`
		Options      []string `json:"options"`
		Answer       string   `json:"answer"`
		Rate         string   `json:"rate"`
		ErrorProne   string   `json:"error_prone"`
		Discuss      string   `json:"discuss"`
		Point        string   `json:"point"`
		AIAnswer     string   `json:"ai_answer"`
		AIDiscuss    string   `json:"ai_discuss"`
		AIConfidence float64  `json:"ai_confidence"`
		AIModel      string   `json:"ai_model"`
		AIStatus     string   `json:"ai_status"`
	}
	type qJSON struct {
		Fingerprint   string         `json:"fingerprint"`
		Name          string         `json:"name"`
		Pkg           string         `json:"pkg"`
		Cls           string         `json:"cls"`
		Unit          string         `json:"unit"`
		Mode          string         `json:"mode"`
		Stem          string         `json:"stem"`
		SharedOptions []string       `json:"shared_options"`
		SubQuestions  []subqJSON     `json:"sub_questions"`
		Discuss       string         `json:"discuss"`
		SourceFile    string         `json:"source_file"`
		Raw           map[string]any `json:"raw"` // always empty in .mqb
	}

	records := make([]qJSON, len(questions))
	for i, q := range questions {
		sqs := make([]subqJSON, len(q.SubQuestions))
		for j, sq := range q.SubQuestions {
			sqs[j] = subqJSON{
				Text: sq.Text, Options: sq.Options, Answer: sq.Answer,
				Rate: sq.Rate, ErrorProne: sq.ErrorProne, Discuss: sq.Discuss,
				Point: sq.Point, AIAnswer: sq.AIAnswer, AIDiscuss: sq.AIDiscuss,
				AIConfidence: sq.AIConfidence, AIModel: sq.AIModel, AIStatus: sq.AIStatus,
			}
		}
		records[i] = qJSON{
			Fingerprint: q.Fingerprint, Name: q.Name, Pkg: q.Pkg,
			Cls: q.Cls, Unit: q.Unit, Mode: q.Mode, Stem: q.Stem,
			SharedOptions: q.SharedOptions, SubQuestions: sqs,
			Discuss: q.Discuss, SourceFile: q.SourceFile, Raw: map[string]any{},
		}
	}

	payload, err := json.Marshal(records)
	if err != nil {
		return "", fmt.Errorf("bank: json marshal: %w", err)
	}

	// 2. zlib compress
	if compress {
		var buf bytes.Buffer
		w, err := zlib.NewWriterLevel(&buf, compressLevel)
		if err != nil {
			return "", err
		}
		if _, err = w.Write(payload); err != nil {
			return "", err
		}
		w.Close()
		payload = buf.Bytes()
	}

	// 3. Fernet encrypt (optional)
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	if password != "" {
		key := DeriveKey(password, salt)
		enc, err := FernetEncrypt(key, payload)
		if err != nil {
			return "", fmt.Errorf("bank: fernet encrypt: %w", err)
		}
		payload = enc
	}

	// 4. Build meta
	meta := bankMeta{
		Count:      len(questions),
		Created:    float64(time.Now().UnixNano()) / 1e9,
		Encrypted:  password != "",
		Compressed: compress,
		SaltHex:    hex.EncodeToString(salt),
	}
	metaBytes, _ := json.Marshal(meta)

	// 5. Write file
	f, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err = f.Write(magicV2); err != nil {
		return "", err
	}
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(metaBytes)))
	if _, err = f.Write(lenBuf); err != nil {
		return "", err
	}
	if _, err = f.Write(metaBytes); err != nil {
		return "", err
	}
	if _, err = f.Write(payload); err != nil {
		return "", err
	}

	return outPath, nil
}

// LoadBank reads a .mqb file and returns the question list.
func LoadBank(path, password string) ([]*models.Question, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	magic := make([]byte, 4)
	if _, err = io.ReadFull(f, magic); err != nil {
		return nil, err
	}

	if bytes.Equal(magic, magicV1) {
		return nil, fmt.Errorf(
			"检测到旧版 MQB1 格式 (pickle)。\n"+
				"请先用原 Python 版本迁移：\n"+
				"  med-exam migrate --bank %s\n"+
				"迁移完成后再用 Go 版本读取", path)
	}
	if !bytes.Equal(magic, magicV2) {
		return nil, errors.New("不是有效的 .mqb 文件")
	}

	lenBuf := make([]byte, 4)
	if _, err = io.ReadFull(f, lenBuf); err != nil {
		return nil, err
	}
	metaLen := binary.BigEndian.Uint32(lenBuf)

	metaBytes := make([]byte, metaLen)
	if _, err = io.ReadFull(f, metaBytes); err != nil {
		return nil, err
	}
	var meta bankMeta
	if err = json.Unmarshal(metaBytes, &meta); err != nil {
		return nil, err
	}

	payload, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	// 1. Decrypt
	if meta.Encrypted {
		if password == "" {
			return nil, errors.New("该题库已加密，请提供 --password")
		}
		salt, err := hex.DecodeString(meta.SaltHex)
		if err != nil {
			return nil, err
		}
		key := DeriveKey(password, salt)
		payload, err = FernetDecrypt(key, payload)
		if err != nil {
			return nil, errors.New("密码错误或文件损坏")
		}
	}

	// 2. Decompress
	if meta.Compressed {
		r, err := zlib.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		payload, err = io.ReadAll(r)
		r.Close()
		if err != nil {
			return nil, err
		}
	}

	// 3. Unmarshal
	var raw []map[string]any
	if err = json.Unmarshal(payload, &raw); err != nil {
		return nil, err
	}
	return questionsFromRaw(raw), nil
}

// questionsFromRaw converts raw JSON maps to Question structs (forward-compatible).
func questionsFromRaw(raw []map[string]any) []*models.Question {
	questions := make([]*models.Question, 0, len(raw))
	for _, r := range raw {
		q := &models.Question{
			Fingerprint:   str(r["fingerprint"]),
			Name:          str(r["name"]),
			Pkg:           str(r["pkg"]),
			Cls:           str(r["cls"]),
			Unit:          str(r["unit"]),
			Mode:          str(r["mode"]),
			Stem:          str(r["stem"]),
			SharedOptions: strSlice(r["shared_options"]),
			Discuss:       str(r["discuss"]),
			SourceFile:    str(r["source_file"]),
		}
		if sqs, ok := r["sub_questions"].([]any); ok {
			for _, sqRaw := range sqs {
				if sqMap, ok := sqRaw.(map[string]any); ok {
					sq := models.SubQuestion{
						Text:       str(sqMap["text"]),
						Options:    strSlice(sqMap["options"]),
						Answer:     str(sqMap["answer"]),
						Rate:       str(sqMap["rate"]),
						ErrorProne: str(sqMap["error_prone"]),
						Discuss:    str(sqMap["discuss"]),
						Point:      str(sqMap["point"]),
						AIAnswer:   str(sqMap["ai_answer"]),
						AIDiscuss:  str(sqMap["ai_discuss"]),
						AIModel:    str(sqMap["ai_model"]),
						AIStatus:   str(sqMap["ai_status"]),
					}
					if v, ok := sqMap["ai_confidence"].(float64); ok {
						sq.AIConfidence = v
					}
					q.SubQuestions = append(q.SubQuestions, sq)
				}
			}
		}
		questions = append(questions, q)
	}
	return questions
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func strSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func replaceExt(path, ext string) string {
	base := path[:len(path)-len(filepath.Ext(path))]
	return base + ext
}
