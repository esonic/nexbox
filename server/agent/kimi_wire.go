package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Kimi persists every agent's event log as JSON lines under
// $KIMI_CODE_HOME/sessions/<workspace>/<session>/agents/<agent>/wire.jsonl.
// The ACP stream omits two things NexBox needs — why a turn failed, and the
// per-turn token usage — so the adapter reads them back from the main
// agent's log, read-only. Each record line starts with its "type" field.
var (
	kimiWireTurnPrompt = []byte(`{"type":"turn.prompt",`)
	kimiWireTurnEnded  = []byte(`{"type":"turn.ended",`)
	kimiWireUsage      = []byte(`{"type":"usage.record",`)
)

// kimiSessionDir locates a session's directory, or "" when it does not exist.
func kimiSessionDir(sessionID string) string {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\*?[`) {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(kimiHome(), "sessions", "*", sessionID))
	if len(matches) != 1 {
		return ""
	}
	return matches[0]
}

func kimiWirePath(sessionID string) string {
	dir := kimiSessionDir(sessionID)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "agents", "main", "wire.jsonl")
}

type kimiUsage struct {
	InputOther         int `json:"inputOther"`
	Output             int `json:"output"`
	InputCacheRead     int `json:"inputCacheRead"`
	InputCacheCreation int `json:"inputCacheCreation"`
}

type kimiWireScan struct {
	ok         bool  // the log was readable
	size       int64 // bytes consumed, the offset for the next scan
	turnsEnded int
	// openTurn: the last turn.prompt has no turn.ended after it, i.e. a turn
	// is running (or its process died mid-turn).
	openTurn bool
	usage    kimiUsage
	failure  string // the last failed turn's error, "" if none failed
}

// scanKimiWire reads the log from offset. A partially written final line is
// left for the next scan.
func scanKimiWire(path string, offset int64) kimiWireScan {
	scan := kimiWireScan{size: offset}
	if path == "" {
		return scan
	}
	file, err := os.Open(path)
	if err != nil {
		return scan
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return scan
	}
	scan.ok = true
	reader := bufio.NewReaderSize(file, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			scan.ok = errors.Is(err, io.EOF)
			return scan
		}
		scan.size += int64(len(line))
		switch {
		case bytes.HasPrefix(line, kimiWireTurnPrompt):
			scan.openTurn = true
		case bytes.HasPrefix(line, kimiWireTurnEnded):
			var record struct {
				Reason string `json:"reason"`
				Error  struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(line, &record) != nil {
				continue
			}
			scan.turnsEnded++
			scan.openTurn = false
			scan.failure = ""
			if record.Reason == "failed" {
				scan.failure = strings.TrimSpace("[" + record.Error.Code + "] " + record.Error.Message)
			}
		case bytes.HasPrefix(line, kimiWireUsage):
			var record struct {
				AgentID    string    `json:"agentId"`
				UsageScope string    `json:"usageScope"`
				Usage      kimiUsage `json:"usage"`
			}
			if json.Unmarshal(line, &record) != nil || record.AgentID != "main" || record.UsageScope != "turn" {
				continue
			}
			scan.usage.InputOther += record.Usage.InputOther
			scan.usage.Output += record.Usage.Output
			scan.usage.InputCacheRead += record.Usage.InputCacheRead
			scan.usage.InputCacheCreation += record.Usage.InputCacheCreation
		}
	}
}
