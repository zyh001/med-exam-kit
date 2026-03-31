package ai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Checkpoint manages resume state for AI enrichment.
type Checkpoint struct {
	Tag string
	Dir string

	mu        sync.Mutex
	completed map[string]json.RawMessage // taskID → result JSON (null = failed)
}

// NewCheckpoint creates a checkpoint manager.
func NewCheckpoint(tag, dir string) *Checkpoint {
	return &Checkpoint{
		Tag:       tag,
		Dir:       dir,
		completed: make(map[string]json.RawMessage),
	}
}

func (c *Checkpoint) path() string {
	return filepath.Join(c.Dir, c.Tag+".ckpt.json")
}

// Load reads the checkpoint file. Returns the number of completed entries.
func (c *Checkpoint) Load() int {
	data, err := os.ReadFile(c.path())
	if err != nil {
		return 0
	}
	var raw struct {
		Completed map[string]json.RawMessage `json:"completed"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return 0
	}
	c.mu.Lock()
	c.completed = raw.Completed
	if c.completed == nil {
		c.completed = make(map[string]json.RawMessage)
	}
	c.mu.Unlock()
	return len(c.completed)
}

// Done marks a task as completed with the given result.
func (c *Checkpoint) Done(taskID string, result map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if result == nil {
		c.completed[taskID] = json.RawMessage("null")
	} else {
		data, _ := json.Marshal(result)
		c.completed[taskID] = data
	}
	c.flush()
}

// IsDone returns true if the task has already been completed.
func (c *Checkpoint) IsDone(taskID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.completed[taskID]
	return ok
}

// Results returns all completed results. nil values indicate failures.
func (c *Checkpoint) Results() map[string]map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]map[string]any, len(c.completed))
	for k, v := range c.completed {
		if string(v) == "null" {
			out[k] = nil
		} else {
			var m map[string]any
			if json.Unmarshal(v, &m) == nil {
				out[k] = m
			}
		}
	}
	return out
}

// Clear removes the checkpoint file.
func (c *Checkpoint) Clear() {
	os.Remove(c.path())
	c.mu.Lock()
	c.completed = make(map[string]json.RawMessage)
	c.mu.Unlock()
}

func (c *Checkpoint) flush() {
	os.MkdirAll(c.Dir, 0o755)
	payload := map[string]any{
		"meta": map[string]any{
			"tag":      c.Tag,
			"saved_at": time.Now().UTC().Format(time.RFC3339),
		},
		"completed": c.completed,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	// Atomic write via temp file + rename
	tmp := c.path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	os.Rename(tmp, c.path())
}
