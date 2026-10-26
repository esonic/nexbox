package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// DetailCache stores full message parts locally for on-demand loading.
// The main message table only stores a lightweight summary (tool_group + thinking markers),
// while this cache holds the complete original parts for lazy loading when users expand.
type DetailCache struct {
	db *DB
}

func NewDetailCache(db *DB) *DetailCache {
	return &DetailCache{db: db}
}

func (c *DetailCache) Save(sessionID, messageID string, parts []MessagePart) error {
	partsJSON, err := json.Marshal(parts)
	if err != nil {
		return fmt.Errorf("marshal parts: %w", err)
	}
	_, err = c.db.Exec(
		`INSERT OR REPLACE INTO message_part_details (message_id, session_id, parts, created_at) VALUES (?, ?, ?, ?)`,
		messageID, sessionID, string(partsJSON), time.Now(),
	)
	return err
}

func (c *DetailCache) Get(messageID string) ([]MessagePart, error) {
	var partsJSON string
	err := c.db.QueryRow(
		`SELECT parts FROM message_part_details WHERE message_id = ?`, messageID,
	).Scan(&partsJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	var parts []MessagePart
	if err := json.Unmarshal([]byte(partsJSON), &parts); err != nil {
		return nil, fmt.Errorf("unmarshal parts: %w", err)
	}
	return parts, nil
}

func (c *DetailCache) Cleanup(keepSessionIDs []string) error {
	if len(keepSessionIDs) == 0 {
		_, err := c.db.Exec(`DELETE FROM message_part_details`)
		return err
	}
	placeholders := make([]string, len(keepSessionIDs))
	args := make([]interface{}, len(keepSessionIDs))
	for i, id := range keepSessionIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		`DELETE FROM message_part_details WHERE session_id NOT IN (%s)`,
		strings.Join(placeholders, ","),
	)
	result, err := c.db.Exec(query, args...)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n > 0 {
		log.Printf("[detail-cache] Cleaned up %d stale detail entries", n)
	}
	return nil
}

// SummarizeParts replaces tool_use/tool_result sequences with tool_group markers
// and strips thinking content, producing a lightweight summary for the main message table.
func SummarizeParts(parts []MessagePart) []MessagePart {
	var summary []MessagePart
	toolCount := 0

	flush := func() {
		if toolCount > 0 {
			summary = append(summary, MessagePart{
				Type:  "tool_group",
				Count: toolCount,
			})
			toolCount = 0
		}
	}

	for _, p := range parts {
		switch p.Type {
		case "tool_use":
			toolCount++
		case "tool_result":
			// counted via tool_use
		case "thinking":
			flush()
			summary = append(summary, MessagePart{Type: "thinking"})
		default:
			flush()
			summary = append(summary, p)
		}
	}
	flush()
	return summary
}
