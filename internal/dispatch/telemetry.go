package dispatch

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// LoopTelemetry captures per-turn metrics for a single agent loop iteration.
// It is serialised to JSONL by TelemetryWriter.
type LoopTelemetry struct {
	Timestamp    time.Time     `json:"timestamp"`
	TaskID       string        `json:"task_id"`
	Provider     string        `json:"provider"`
	Model        string        `json:"model"`
	Turn         int           `json:"turn"`
	PromptTokens int           `json:"prompt_tokens"`
	OutputTokens int           `json:"output_tokens"`
	Cost         float64       `json:"cost"`
	ToolCalls    int           `json:"tool_calls"`
	ToolErrors   int           `json:"tool_errors"`
	StopReason   string        `json:"stop_reason"`
	Duration     time.Duration `json:"duration_ns"`
	Escalated    bool          `json:"escalated"`
}

// TelemetryWriter serialises LoopTelemetry entries as newline-delimited JSON
// (JSONL) to an underlying io.Writer.  It is safe for concurrent use.
type TelemetryWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// NewTelemetryWriter creates a TelemetryWriter that writes to w.
func NewTelemetryWriter(w io.Writer) *TelemetryWriter {
	return &TelemetryWriter{w: w}
}

// Write serialises entry as a single JSON object followed by a newline.
// Concurrent calls are serialised with an internal mutex.
func (tw *TelemetryWriter) Write(entry LoopTelemetry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tw.mu.Lock()
	defer tw.mu.Unlock()
	_, err = tw.w.Write(data)
	return err
}
