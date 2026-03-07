package store

import "strings"

// GetTaskInfo builds a TaskInfo from the Store interface.
func GetTaskInfo(s Store, id string) (*TaskInfo, error) {
	t, err := s.GetTask(id)
	if err != nil || t == nil {
		return nil, err
	}
	return &TaskInfo{
		ID:          t.ID,
		Title:       t.Title,
		Description: t.Description,
		Status:      t.Status,
		Priority:    t.Priority,
		Assigner:    t.Assigner,
	}, nil
}

// GetAssetInfo builds an AssetInfo from the Store interface.
func GetAssetInfo(s Store, id string) (*AssetInfo, error) {
	a, err := s.GetAsset(id)
	if err != nil || a == nil {
		return nil, err
	}
	return &AssetInfo{
		ID:     a.ID,
		Type:   a.Type,
		Title:  a.Title,
		Status: a.Status,
	}, nil
}

// GetDesignDocSummary returns a truncated summary of the newest design_doc
// asset in the given project. Works with any Store implementation.
func GetDesignDocSummary(s Store, projectID string) string {
	assets, err := s.ListAssetsByType(projectID, "design_doc", 1, 0, "")
	if err != nil || len(assets) == 0 {
		return ""
	}
	content, err := s.GetLatestAssetContent(assets[0].ID)
	if err != nil || content == "" {
		return ""
	}
	content = strings.TrimSpace(content)
	runes := []rune(content)
	if len(runes) > 100 {
		return string(runes[:100]) + "..."
	}
	return content
}
