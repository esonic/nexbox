package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var _ Store = (*RemoteStore)(nil)

// RemoteServerInfo holds metadata returned by the remote data service on Ping.
type RemoteServerInfo struct {
	UserName string `json:"user_name"`
}

// RemoteStore implements the Store interface as an HTTP client
// that proxies all operations to a remote data service.
type RemoteStore struct {
	baseURL            string
	marketplaceBaseURL string // overrides baseURL for /marketplace/* calls; set via NEXBOX_MARKETPLACE_URL
	localOSSDir        string // when set (LOCAL_OSS_DIR), skill tar upload/download uses local FS instead of OSS
	token              string
	httpClient         *http.Client
	Info               RemoteServerInfo
}

// NewRemoteStore creates a RemoteStore that connects to the given data service.
// If the environment variable NEXBOX_MARKETPLACE_URL is set, marketplace API
// calls will be routed there instead of baseURL (useful for local testing).
func NewRemoteStore(baseURL, token string) *RemoteStore {
	mktURL := strings.TrimRight(os.Getenv("NEX_MARKETPLACE_URL"), "/")
	if mktURL != "" {
		log.Printf("[store] marketplace requests → %s (NEXBOX_MARKETPLACE_URL)", mktURL)
	}
	localOSS := strings.TrimRight(os.Getenv("NEX_LOCAL_OSS_DIR"), "/")
	if localOSS != "" {
		if err := os.MkdirAll(localOSS, 0755); err != nil {
			log.Printf("[store] WARNING: cannot create LOCAL_OSS_DIR %q: %v", localOSS, err)
			localOSS = ""
		} else {
			log.Printf("[store] skill OSS mock mode → %s (LOCAL_OSS_DIR)", localOSS)
		}
	}
	return &RemoteStore{
		baseURL:            strings.TrimRight(baseURL, "/"),
		marketplaceBaseURL: mktURL,
		localOSSDir:        localOSS,
		token:              token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// mktBase returns the effective base URL for marketplace endpoints.
func (r *RemoteStore) mktBase() string {
	if r.marketplaceBaseURL != "" {
		return r.marketplaceBaseURL
	}
	return r.baseURL
}

// Ping verifies connectivity and token validity by calling GET /info.
// On success it caches the namespace and user_name returned by the server.
func (r *RemoteStore) Ping() error {
	data, err := r.doRequest("GET", "/info", nil, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &r.Info); err != nil {
		return fmt.Errorf("decode /info response fail: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

// ApiError represents an HTTP error returned by the remote data service.
type ApiError struct {
	StatusCode int
	Message    string
}

func (e *ApiError) Error() string {
	return fmt.Sprintf("remote store: HTTP %d: %s", e.StatusCode, e.Message)
}

// doRequestTo is the shared HTTP helper used by doRequest and doMktRequest.
// base is the full base URL (no trailing slash); path starts with "/".
func (r *RemoteStore) doRequestTo(base, method, path string, query url.Values, body interface{}) ([]byte, error) {
	u := base + path
	if query != nil {
		u += "?" + query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, u, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("remote store request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusConflict {
			return nil, ErrVersionConflict
		}
		msg := string(respBody)
		// Backend may return different error shapes; try each in order of preference.
		var errResp struct {
			Error      string `json:"error"` // simple {"error":"..."}
			Msg        string `json:"msg"`   // {"msg":"...", "detailMsg":"..."}
			DetailMsg  string `json:"detailMsg"`
			ErrMessage string `json:"error_message"` // {"error_message":"..."}
		}
		if json.Unmarshal(respBody, &errResp) == nil {
			switch {
			case errResp.DetailMsg != "":
				msg = errResp.DetailMsg
			case errResp.Error != "":
				msg = errResp.Error
			case errResp.ErrMessage != "":
				msg = errResp.ErrMessage
			case errResp.Msg != "":
				msg = errResp.Msg
			}
		}
		if resp.StatusCode >= 400 {
			log.Printf("[remote] %s %s → %d: %s", method, path, resp.StatusCode, string(respBody))
		}
		return nil, &ApiError{StatusCode: resp.StatusCode, Message: msg}
	}

	return respBody, nil
}

func (r *RemoteStore) doRequest(method, path string, query url.Values, body interface{}) ([]byte, error) {
	return r.doRequestTo(r.baseURL, method, path, query, body)
}

// doMktRequest routes to mktBase() so that NEXBOX_MARKETPLACE_URL can redirect
// marketplace calls to a local server for development/testing.
func (r *RemoteStore) doMktRequest(method, path string, query url.Values, body interface{}) ([]byte, error) {
	return r.doRequestTo(r.mktBase(), method, path, query, body)
}

func decodeJSON[T any](data []byte) (*T, error) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &v, nil
}

func decodeJSONList[T any](data []byte) ([]T, error) {
	var v []T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("decode response list: %w", err)
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// Projects
// ---------------------------------------------------------------------------

func (r *RemoteStore) CreateProject(req CreateProjectRequest) (*Project, error) {
	data, err := r.doRequest("POST", "/projects", nil, req)
	if err != nil {
		return nil, err
	}
	return decodeJSON[Project](data)
}

func (r *RemoteStore) ListProjects() ([]Project, error) {
	data, err := r.doRequest("GET", "/projects", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[Project](data)
}

// FindProjectByGitLabURL queries the remote service for a project whose gitlab_repo_url
// matches the given URL. Unlike ListProjects this searches all projects regardless of
// the caller's membership (the Java endpoint supports ?gitlabUrl= for exactly this).
// Returns nil if no project is found.
func (r *RemoteStore) FindProjectByGitLabURL(gitlabURL string) (*Project, error) {
	q := url.Values{"gitlabUrl": []string{gitlabURL}}
	data, err := r.doRequest("GET", "/projects", q, nil)
	if err != nil {
		return nil, err
	}
	projects, err := decodeJSONList[Project](data)
	if err != nil {
		return nil, err
	}
	if len(projects) == 0 {
		return nil, nil
	}
	return &projects[0], nil
}

func (r *RemoteStore) GetProjectsByIDs(ids []string) ([]Project, error) {
	return r.ListProjects()
}

// ---------------------------------------------------------------------------
// Project Members
// ---------------------------------------------------------------------------

// ProjectMember represents a member of a project.
type ProjectMember struct {
	ProjectID string `json:"project_id"`
	UserName  string `json:"user_name"`
	AddedBy   string `json:"added_by"`
	AddedAt   string `json:"added_at"`
}

// Member represents a namespace member returned by the member search API.
type Member struct {
	UserName    string `json:"user_name"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
}

// SearchMembers performs a fuzzy search for namespace members via the remote service.
func (r *RemoteStore) SearchMembers(query string) ([]Member, error) {
	q := url.Values{"q": {query}}
	data, err := r.doRequest("GET", "/members", q, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[Member](data)
}

func (r *RemoteStore) ListProjectMembers(projectID string) ([]ProjectMember, error) {
	data, err := r.doRequest("GET", "/projects/"+projectID+"/members", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[ProjectMember](data)
}

func (r *RemoteStore) AddProjectMember(projectID, userName string) error {
	_, err := r.doRequest("POST", "/projects/"+projectID+"/members", nil, map[string]string{"user_name": userName})
	return err
}

func (r *RemoteStore) RemoveProjectMember(projectID, userName string) error {
	_, err := r.doRequest("DELETE", "/projects/"+projectID+"/members/"+userName, nil, nil)
	return err
}

func (r *RemoteStore) GetProject(id string) (*Project, error) {
	data, err := r.doRequest("GET", "/projects/"+id, nil, nil)
	if err != nil {
		if ae, ok := err.(*ApiError); ok && ae.StatusCode == 404 {
			return nil, nil
		}
		return nil, err
	}
	return decodeJSON[Project](data)
}

func (r *RemoteStore) UpdateProject(id string, req UpdateProjectRequest) (*Project, error) {
	body := make(map[string]interface{})
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal update request: %w", err)
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("decode update request: %w", err)
	}
	data, err := r.doRequest("PUT", "/projects/"+id, nil, body)
	if err != nil {
		return nil, err
	}
	return decodeJSON[Project](data)
}

func (r *RemoteStore) DeleteProject(id string) error {
	_, err := r.doRequest("DELETE", "/projects/"+id, nil, nil)
	return err
}

// ---------------------------------------------------------------------------
// Tasks
// ---------------------------------------------------------------------------

func (r *RemoteStore) CreateTask(req CreateTaskRequest) (*Task, error) {
	data, err := r.doRequest("POST", "/tasks", nil, req)
	if err != nil {
		return nil, err
	}
	return decodeJSON[Task](data)
}

func (r *RemoteStore) ListTasks(limit, offset int, projectIDs []string, search string) ([]Task, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	if projectIDs != nil {
		q.Set("project_ids", strings.Join(projectIDs, ","))
	}
	if search != "" {
		q.Set("q", search)
	}
	data, err := r.doRequest("GET", "/tasks", q, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[Task](data)
}

func (r *RemoteStore) GetTask(id string) (*Task, error) {
	data, err := r.doRequest("GET", "/tasks/"+id, nil, nil)
	if err != nil {
		if ae, ok := err.(*ApiError); ok && ae.StatusCode == 404 {
			return nil, nil
		}
		return nil, err
	}
	return decodeJSON[Task](data)
}

func (r *RemoteStore) UpdateTask(id string, req UpdateTaskRequest) (*Task, error) {
	data, err := r.doRequest("PUT", "/tasks/"+id, nil, req)
	if err != nil {
		return nil, err
	}
	return decodeJSON[Task](data)
}

func (r *RemoteStore) IncrementTaskRefCount(id string) error {
	_, err := r.doRequest("POST", "/tasks/"+id+"/ref-count", nil, nil)
	return err
}

func (r *RemoteStore) DeleteTask(id string) error {
	_, err := r.doRequest("DELETE", "/tasks/"+id, nil, nil)
	return err
}

func (r *RemoteStore) LinkTaskAsset(taskID, assetID string) (*TaskAsset, error) {
	data, err := r.doRequest("POST", "/tasks/"+taskID+"/assets", nil, map[string]string{"asset_id": assetID})
	if err != nil {
		return nil, err
	}
	// Java server returns 201 with no body; construct result locally.
	if len(data) == 0 || string(data) == "" {
		return &TaskAsset{
			ID:        uuid.New().String(),
			TaskID:    taskID,
			AssetID:   assetID,
			CreatedAt: time.Now(),
		}, nil
	}
	return decodeJSON[TaskAsset](data)
}

func (r *RemoteStore) ListTaskAssets(taskID string) ([]Asset, error) {
	data, err := r.doRequest("GET", "/tasks/"+taskID+"/assets", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[Asset](data)
}

func (r *RemoteStore) UnlinkTaskAsset(taskID, assetID string) error {
	_, err := r.doRequest("DELETE", "/tasks/"+taskID+"/assets/"+assetID, nil, nil)
	return err
}

// ---------------------------------------------------------------------------
// Task Comments
// ---------------------------------------------------------------------------

func (r *RemoteStore) ListTaskComments(taskID string) ([]TaskComment, error) {
	data, err := r.doRequest("GET", "/tasks/"+taskID+"/comments", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[TaskComment](data)
}

func (r *RemoteStore) CreateTaskComment(taskID string, req CreateTaskCommentRequest, author string) (*TaskComment, error) {
	body := map[string]string{"body": req.Body, "author": author}
	data, err := r.doRequest("POST", "/tasks/"+taskID+"/comments", nil, body)
	if err != nil {
		return nil, err
	}
	return decodeJSON[TaskComment](data)
}

func (r *RemoteStore) UpdateTaskComment(id string, req UpdateTaskCommentRequest) (*TaskComment, error) {
	data, err := r.doRequest("PUT", "/task-comments/"+id, nil, req)
	if err != nil {
		return nil, err
	}
	return decodeJSON[TaskComment](data)
}

func (r *RemoteStore) DeleteTaskComment(id string) error {
	_, err := r.doRequest("DELETE", "/task-comments/"+id, nil, nil)
	return err
}

// ---------------------------------------------------------------------------
// Labels
// ---------------------------------------------------------------------------

func (r *RemoteStore) ListLabels(projectID string) ([]Label, error) {
	data, err := r.doRequest("GET", "/projects/"+projectID+"/labels", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[Label](data)
}

func (r *RemoteStore) CreateLabel(projectID string, req CreateLabelRequest) (*Label, error) {
	data, err := r.doRequest("POST", "/projects/"+projectID+"/labels", nil, req)
	if err != nil {
		return nil, err
	}
	return decodeJSON[Label](data)
}

func (r *RemoteStore) DeleteLabel(projectID string, id string) error {
	_, err := r.doRequest("DELETE", "/projects/"+projectID+"/labels/"+id, nil, nil)
	return err
}

func (r *RemoteStore) LeaveProject(projectID string) error {
	_, err := r.doRequest("DELETE", "/projects/"+projectID+"/members/me", nil, nil)
	return err
}

func (r *RemoteStore) AddLabelToTask(taskID, labelName string) error {
	_, err := r.doRequest("POST", "/tasks/"+taskID+"/labels", nil, map[string]string{"name": labelName})
	return err
}

func (r *RemoteStore) RemoveLabelFromTask(taskID, labelName string) error {
	_, err := r.doRequest("DELETE", "/tasks/"+taskID+"/labels/"+labelName, nil, nil)
	return err
}

// ---------------------------------------------------------------------------
// Reactions
// ---------------------------------------------------------------------------

func (r *RemoteStore) GetReactionSummary(targetType, targetID, currentUser string) ([]ReactionSummary, error) {
	q := url.Values{"user": {currentUser}}
	data, err := r.doRequest("GET", "/reactions/"+targetType+"/"+targetID, q, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[ReactionSummary](data)
}

func (r *RemoteStore) AddReaction(targetType, targetID, author, emoji string) error {
	_, err := r.doRequest("POST", "/reactions/"+targetType+"/"+targetID, nil, map[string]string{"author": author, "emoji": emoji})
	return err
}

func (r *RemoteStore) RemoveReaction(targetType, targetID, author, emoji string) error {
	q := url.Values{"author": {author}, "emoji": {emoji}}
	_, err := r.doRequest("DELETE", "/reactions/"+targetType+"/"+targetID, q, nil)
	return err
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func (r *RemoteStore) CreateSession(req CreateSessionRequest) (*Session, error) {
	data, err := r.doRequest("POST", "/sessions", nil, req)
	if err != nil {
		return nil, err
	}
	return decodeJSON[Session](data)
}

func (r *RemoteStore) ListSessions() ([]Session, error) {
	data, err := r.doRequest("GET", "/sessions", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[Session](data)
}

func (r *RemoteStore) GetSession(id string) (*Session, error) {
	data, err := r.doRequest("GET", "/sessions/"+id, nil, nil)
	if err != nil {
		if ae, ok := err.(*ApiError); ok && ae.StatusCode == 404 {
			return nil, nil
		}
		return nil, err
	}
	return decodeJSON[Session](data)
}

func (r *RemoteStore) UpdateSession(id string, req UpdateSessionRequest) (*Session, error) {
	data, err := r.doRequest("PUT", "/sessions/"+id, nil, req)
	if err != nil {
		return nil, err
	}
	return decodeJSON[Session](data)
}

func (r *RemoteStore) UpdateSessionAgentID(id, agentSessionID string) error {
	_, err := r.doRequest("PUT", "/sessions/"+id, nil, map[string]string{"agent_session_id": agentSessionID})
	return err
}

func (r *RemoteStore) UpdateSessionStreamID(id, streamID string) error {
	_, err := r.doRequest("PUT", "/sessions/"+id, nil, map[string]string{"stream_id": streamID})
	return err
}

func (r *RemoteStore) UpdateSessionFeatures(id, features string) error {
	_, err := r.doRequest("PUT", "/sessions/"+id, nil, map[string]string{"features": features})
	return err
}

func (r *RemoteStore) UpdateSessionWorktree(id, mode, path string) error {
	return nil // worktree is local-only
}

func (r *RemoteStore) GetExpiredSessionWorktrees(_ time.Time) ([]string, error) {
	return nil, nil // worktree is local-only
}

func (r *RemoteStore) DeleteSession(id string) error {
	_, err := r.doRequest("DELETE", "/sessions/"+id, nil, nil)
	return err
}

func (r *RemoteStore) ListTrashedSessions() ([]Session, error) {
	return nil, nil
}

func (r *RemoteStore) RestoreSession(id string) error {
	return nil
}

func (r *RemoteStore) PurgeExpiredSessions(_ time.Time) ([]string, error) {
	return nil, nil
}

func (r *RemoteStore) ReorderSessions(sessionIDs []string) error {
	_, err := r.doRequest("PUT", "/sessions/reorder", nil, map[string]interface{}{"session_ids": sessionIDs})
	return err
}

// ---------------------------------------------------------------------------
// Assets
// ---------------------------------------------------------------------------

func (r *RemoteStore) CreateAsset(projectID string, req CreateAssetRequest) (*Asset, error) {
	data, err := r.doRequest("POST", "/projects/"+projectID+"/assets", nil, req)
	if err != nil {
		return nil, err
	}
	return decodeJSON[Asset](data)
}

func (r *RemoteStore) ListAssets(projectID string, limit, offset int, search string) ([]Asset, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	if search != "" {
		q.Set("q", search)
	}
	data, err := r.doRequest("GET", "/projects/"+projectID+"/assets", q, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[Asset](data)
}

func (r *RemoteStore) ListAssetsByType(projectID, assetType string, limit, offset int, search string) ([]Asset, error) {
	q := url.Values{"type": {assetType}}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	if search != "" {
		q.Set("q", search)
	}
	data, err := r.doRequest("GET", "/projects/"+projectID+"/assets", q, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[Asset](data)
}

func (r *RemoteStore) GetAsset(id string) (*Asset, error) {
	data, err := r.doRequest("GET", "/assets/"+id, nil, nil)
	if err != nil {
		if ae, ok := err.(*ApiError); ok && ae.StatusCode == 404 {
			return nil, nil
		}
		return nil, err
	}
	return decodeJSON[Asset](data)
}

func (r *RemoteStore) UpdateAsset(id string, req UpdateAssetRequest) (*Asset, error) {
	data, err := r.doRequest("PUT", "/assets/"+id, nil, req)
	if err != nil {
		return nil, err
	}
	return decodeJSON[Asset](data)
}

func (r *RemoteStore) DeleteAsset(id string) error {
	_, err := r.doRequest("DELETE", "/assets/"+id, nil, nil)
	return err
}

func (r *RemoteStore) GetAssetVersions(assetID string) ([]AssetVersion, error) {
	data, err := r.doRequest("GET", "/assets/"+assetID+"/versions", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[AssetVersion](data)
}

func (r *RemoteStore) GetAssetVersion(assetID string, version int) (*AssetVersion, error) {
	q := url.Values{"version": {strconv.Itoa(version)}}
	data, err := r.doRequest("GET", "/assets/"+assetID+"/versions", q, nil)
	if err != nil {
		if ae, ok := err.(*ApiError); ok && ae.StatusCode == 404 {
			return nil, nil
		}
		return nil, err
	}
	return decodeJSON[AssetVersion](data)
}

func (r *RemoteStore) FindAssetByTitle(projectID, title string) (*Asset, error) {
	q := url.Values{"title": {title}}
	data, err := r.doRequest("GET", "/projects/"+projectID+"/assets", q, nil)
	if err != nil {
		return nil, err
	}
	var assets []Asset
	if err := json.Unmarshal(data, &assets); err != nil {
		return nil, err
	}
	if len(assets) == 0 {
		return nil, nil
	}
	return &assets[0], nil
}

func (r *RemoteStore) GetLatestAssetContent(assetID string) (string, error) {
	data, err := r.doRequest("GET", "/assets/"+assetID+"/content", nil, nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return string(data), nil
	}
	return resp.Content, nil
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

func (r *RemoteStore) CreateMessage(sessionID, role string, parts []MessagePart, metadata MessageMetadata) (*ChatMessage, error) {
	partsJSON, err := json.Marshal(parts)
	if err != nil {
		return nil, fmt.Errorf("marshal parts: %w", err)
	}
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}
	body := map[string]interface{}{
		"session_id": sessionID,
		"role":       role,
		"parts":      string(partsJSON),
		"metadata":   string(metaJSON),
	}
	data, err := r.doRequest("POST", "/sessions/"+sessionID+"/messages", nil, body)
	if err != nil {
		return nil, err
	}
	return decodeChatMessage(data)
}

func (r *RemoteStore) ListMessages(sessionID string) ([]ChatMessage, error) {
	data, err := r.doRequest("GET", "/sessions/"+sessionID+"/messages", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeChatMessageList(data)
}

func (r *RemoteStore) ListMessagesPaginated(sessionID string, limit int, beforeID string) ([]ChatMessage, bool, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if beforeID != "" {
		query.Set("before", beforeID)
	}
	data, err := r.doRequest("GET", "/sessions/"+sessionID+"/messages", query, nil)
	if err != nil {
		return nil, false, err
	}
	var resp struct {
		Messages []json.RawMessage `json:"messages"`
		HasMore  bool              `json:"has_more"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, false, fmt.Errorf("decode response: %w", err)
	}

	messages := make([]ChatMessage, 0, len(resp.Messages))
	for _, raw := range resp.Messages {
		msg, err := decodeChatMessage(raw)
		if err != nil {
			log.Printf("[remote] failed to decode message: %v", err)
			continue
		}
		messages = append(messages, *msg)
	}
	return messages, resp.HasMore, nil
}

// decodeChatMessage handles the Java server's format where parts/metadata are JSON strings.
func decodeChatMessage(data []byte) (*ChatMessage, error) {
	var raw struct {
		ID        string          `json:"id"`
		SessionID string          `json:"session_id"`
		Role      string          `json:"role"`
		Parts     json.RawMessage `json:"parts"`
		Metadata  json.RawMessage `json:"metadata"`
		CreatedAt time.Time       `json:"created_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode chat message: %w", err)
	}
	msg := &ChatMessage{
		ID:        raw.ID,
		SessionID: raw.SessionID,
		Role:      raw.Role,
		CreatedAt: raw.CreatedAt,
	}
	if err := unmarshalStringOrJSON(raw.Parts, &msg.Parts); err != nil {
		return nil, fmt.Errorf("decode parts: %w", err)
	}
	if err := unmarshalStringOrJSON(raw.Metadata, &msg.Metadata); err != nil {
		return nil, fmt.Errorf("decode metadata: %w", err)
	}
	return msg, nil
}

func decodeChatMessageList(data []byte) ([]ChatMessage, error) {
	var raws []struct {
		ID        string          `json:"id"`
		SessionID string          `json:"session_id"`
		Role      string          `json:"role"`
		Parts     json.RawMessage `json:"parts"`
		Metadata  json.RawMessage `json:"metadata"`
		CreatedAt time.Time       `json:"created_at"`
	}
	if err := json.Unmarshal(data, &raws); err != nil {
		return nil, fmt.Errorf("decode chat message list: %w", err)
	}
	msgs := make([]ChatMessage, 0, len(raws))
	for _, raw := range raws {
		msg := ChatMessage{
			ID:        raw.ID,
			SessionID: raw.SessionID,
			Role:      raw.Role,
			CreatedAt: raw.CreatedAt,
		}
		if err := unmarshalStringOrJSON(raw.Parts, &msg.Parts); err != nil {
			log.Printf("[remote] failed to decode message parts (id=%s): %v", raw.ID, err)
		}
		if err := unmarshalStringOrJSON(raw.Metadata, &msg.Metadata); err != nil {
			log.Printf("[remote] failed to decode message metadata (id=%s): %v", raw.ID, err)
		}
		msgs = append(msgs, msg)
	}
	return msgs, nil
}

// unmarshalStringOrJSON handles a field that may be a JSON string (e.g. "\"[...]\"")
// or a direct JSON value (e.g. "[...]").
func unmarshalStringOrJSON(raw json.RawMessage, target interface{}) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		if s == "" {
			return nil
		}
		return json.Unmarshal([]byte(s), target)
	}
	return json.Unmarshal(raw, target)
}

func (r *RemoteStore) DeleteMessagesBySession(sessionID string) error {
	_, err := r.doRequest("DELETE", "/sessions/"+sessionID+"/messages", nil, nil)
	return err
}

func (r *RemoteStore) TrimSessionMessages(sessionID string, keepCount int) (int64, error) {
	// Not supported on remote store; trimming is handled locally.
	return 0, nil
}

func (r *RemoteStore) UpdateMessageParts(messageID string, parts []MessagePart, metadata MessageMetadata) error {
	// Remote Java server does not expose a message-update endpoint.
	// Progressive saves are handled via CreateMessage at the end instead.
	return nil
}

func (r *RemoteStore) CreateUserMessage(sessionID, content string) (*ChatMessage, error) {
	parts := []MessagePart{{Type: "text", Content: content}}
	return r.CreateMessage(sessionID, "user", parts, MessageMetadata{})
}

// ---------------------------------------------------------------------------
// Queued Messages (always local — these stubs satisfy the Store interface)
// ---------------------------------------------------------------------------

func (r *RemoteStore) ListQueuedMessages(sessionID string) ([]QueuedMessage, error) {
	return nil, nil
}

func (r *RemoteStore) CreateQueuedMessage(sessionID string, req CreateQueuedMessageRequest) (*QueuedMessage, error) {
	return nil, fmt.Errorf("queue not supported on remote store")
}

func (r *RemoteStore) UpdateQueuedMessage(sessionID, id, text string) error {
	return fmt.Errorf("queue not supported on remote store")
}

func (r *RemoteStore) DeleteQueuedMessage(sessionID, id string) error {
	return fmt.Errorf("queue not supported on remote store")
}

func (r *RemoteStore) PopQueuedMessage(sessionID string) (*QueuedMessage, error) {
	return nil, nil
}

func (r *RemoteStore) PopQueuedMessageByID(sessionID, id string) (*QueuedMessage, error) {
	return nil, nil
}

func (r *RemoteStore) SessionsWithQueuedMessages() ([]string, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Review Comments
// ---------------------------------------------------------------------------

func (r *RemoteStore) CreateReviewComment(assetID string, req CreateReviewCommentRequest) (*ReviewComment, error) {
	data, err := r.doRequest("POST", "/assets/"+assetID+"/comments", nil, req)
	if err != nil {
		return nil, err
	}
	return decodeJSON[ReviewComment](data)
}

func (r *RemoteStore) ListReviewComments(assetID string) ([]ReviewComment, error) {
	data, err := r.doRequest("GET", "/assets/"+assetID+"/comments", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[ReviewComment](data)
}

func (r *RemoteStore) GetReviewComment(id string) (*ReviewComment, error) {
	data, err := r.doRequest("GET", "/comments/"+id, nil, nil)
	if err != nil {
		if ae, ok := err.(*ApiError); ok && ae.StatusCode == 404 {
			return nil, nil
		}
		return nil, err
	}
	return decodeJSON[ReviewComment](data)
}

func (r *RemoteStore) UpdateReviewComment(id string, req UpdateReviewCommentRequest) (*ReviewComment, error) {
	data, err := r.doRequest("PUT", "/comments/"+id, nil, req)
	if err != nil {
		return nil, err
	}
	return decodeJSON[ReviewComment](data)
}

func (r *RemoteStore) DeleteReviewComment(id string) error {
	_, err := r.doRequest("DELETE", "/comments/"+id, nil, nil)
	return err
}

func (r *RemoteStore) ResolveAllReviewComments(assetID string) (int, error) {
	data, err := r.doRequest("POST", "/assets/"+assetID+"/comments/resolve-all", nil, nil)
	if err != nil {
		return 0, err
	}
	var resp struct {
		Count         int `json:"count"`
		ResolvedCount int `json:"resolved_count"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, err
	}
	if resp.ResolvedCount > 0 {
		return resp.ResolvedCount, nil
	}
	return resp.Count, nil
}

// ---------------------------------------------------------------------------
// Changes
// ---------------------------------------------------------------------------

// InsertChange writes a change record to the remote store via POST /changes.
// Used by Go handlers to record notification-bearing changes (with encoded detail).
func (r *RemoteStore) InsertChange(projectID, entityType, entityID, action, summary, detail, userName string, entityVersion *int) error {
	body := map[string]interface{}{
		"projectId":   projectID,
		"entityType":  entityType,
		"entityId":    entityID,
		"action":      action,
		"summary":     summary,
		"detail":      detail,
		"userName":    userName,
	}
	if entityVersion != nil {
		body["entityVersion"] = *entityVersion
	}
	_, err := r.doRequest("POST", "/changes", nil, body)
	return err
}

// parseChangeDetail parses the JSON detail field for notification changes and
// populates the extra fields (Recipient, TaskID, AssetID, LineNumber, CommentID).
func parseChangeDetail(ch *Change) {
	if ch.EntityType != "notification" || ch.Detail == "" {
		return
	}
	var d struct {
		Recipient      string `json:"recipient"`
		TaskID         string `json:"task_id"`
		AssetID        string `json:"asset_id"`
		LineNumber     string `json:"line_number"`
		CommentID      string `json:"comment_id"`
		CommentPreview string `json:"comment_preview"`
	}
	// detail may be a plain string (legacy) rather than JSON
	if err := json.Unmarshal([]byte(ch.Detail), &d); err != nil {
		return
	}
	ch.Recipient = d.Recipient
	ch.TaskID = d.TaskID
	ch.AssetID = d.AssetID
	ch.CommentID = d.CommentID
	ch.CommentPreview = d.CommentPreview
	if d.LineNumber != "" {
		if n, err := strconv.Atoi(d.LineNumber); err == nil {
			ch.LineNumber = &n
		}
	}
}

func (r *RemoteStore) ListChanges(sinceID int64, limit int, order string) ([]Change, error) {
	q := url.Values{
		"since": {strconv.FormatInt(sinceID, 10)},
		"limit": {strconv.Itoa(limit)},
	}
	if order != "" {
		q.Set("order", order)
	}
	data, err := r.doRequest("GET", "/changes", q, nil)
	if err != nil {
		return nil, err
	}
	changes, err := decodeJSONList[Change](data)
	if err != nil {
		return nil, err
	}
	for i := range changes {
		parseChangeDetail(&changes[i])
	}
	return changes, nil
}

func (r *RemoteStore) ListLarkChat(sinceID int64, limit int) ([]Change, error) {
	q := url.Values{
		"since": {strconv.FormatInt(sinceID, 10)},
		"limit": {strconv.Itoa(limit)},
	}
	data, err := r.doRequest("GET", "/changes/lark-chat", q, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[Change](data)
}

func (r *RemoteStore) SendLarkReply(targetUser, content string) error {
	body := map[string]interface{}{
		"targetUser": targetUser,
		"content":    content,
		"timestamp":  time.Now().Unix(),
	}
	_, err := r.doRequest("POST", "/lark-reply", nil, body)
	return err
}

// ========================================================================
// Workflow Store (definitions stored remotely; executions/logs stay local)
// ========================================================================

func (r *RemoteStore) CreateWorkflow(projectID, name, description, createdBy string, definition []byte) (*Workflow, error) {
	body := map[string]interface{}{
		"name":        name,
		"description": description,
		"created_by":  createdBy,
		"definition":  json.RawMessage(definition),
	}
	data, err := r.doRequest("POST", "/projects/"+projectID+"/workflows", nil, body)
	if err != nil {
		return nil, err
	}
	return decodeJSON[Workflow](data)
}

func (r *RemoteStore) GetWorkflow(id string) (*Workflow, error) {
	data, err := r.doRequest("GET", "/workflows/"+id, nil, nil)
	if err != nil {
		if ae, ok := err.(*ApiError); ok && ae.StatusCode == 404 {
			return nil, nil
		}
		return nil, err
	}
	return decodeJSON[Workflow](data)
}

func (r *RemoteStore) ListWorkflows(projectID string) ([]Workflow, error) {
	data, err := r.doRequest("GET", "/projects/"+projectID+"/workflows", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[Workflow](data)
}

func (r *RemoteStore) UpdateWorkflow(id string, req UpdateWorkflowRequest) (*Workflow, error) {
	data, err := r.doRequest("PUT", "/workflows/"+id, nil, req)
	if err != nil {
		return nil, err
	}
	return decodeJSON[Workflow](data)
}

func (r *RemoteStore) DeleteWorkflow(id string) error {
	_, err := r.doRequest("DELETE", "/workflows/"+id, nil, nil)
	return err
}

func (r *RemoteStore) CreateExecution(req CreateExecutionRequest) (*WorkflowExecution, error) {
	return nil, fmt.Errorf("workflows not supported in remote store")
}

func (r *RemoteStore) GetExecution(id string) (*WorkflowExecution, error) {
	return nil, fmt.Errorf("workflows not supported in remote store")
}

func (r *RemoteStore) ListExecutions(workflowID string, limit int, offset int) ([]WorkflowExecution, error) {
	return nil, fmt.Errorf("workflows not supported in remote store")
}

func (r *RemoteStore) UpdateExecutionStatus(id string, status string, errorMsg string, durationMs *int64) error {
	return fmt.Errorf("workflows not supported in remote store")
}

func (r *RemoteStore) CreateNodeLog(log *WorkflowNodeLog) error {
	return fmt.Errorf("workflows not supported in remote store")
}

func (r *RemoteStore) GetNodeLogs(executionID string) ([]WorkflowNodeLog, error) {
	return nil, fmt.Errorf("workflows not supported in remote store")
}

func (r *RemoteStore) UpdateNodeLog(id string, status string, input string, output string, errorMsg string, durationMs int64) error {
	return fmt.Errorf("workflows not supported in remote store")
}

func (r *RemoteStore) SetNodeHookInfo(id string, retries int, result string) error {
	return fmt.Errorf("workflows not supported in remote store")
}

func (r *RemoteStore) FailStaleNodeLogs(executionID, status string) error {
	return fmt.Errorf("workflows not supported in remote store")
}

// ListAllWorkflows returns all workflows across all projects the user belongs to.
// Used by the local scheduler to find workflows with schedules.
func (r *RemoteStore) ListAllWorkflows() ([]Workflow, error) {
	projects, err := r.ListProjects()
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	var all []Workflow
	for _, p := range projects {
		wfs, err := r.ListWorkflows(p.ID)
		if err != nil {
			// Non-fatal: skip this project and continue
			continue
		}
		all = append(all, wfs...)
	}
	return all, nil
}

func (r *RemoteStore) ResetStaleExecutions() error {
	return fmt.Errorf("workflows not supported in remote store")
}

// ---------------------------------------------------------------------------
// Marketplace
// ---------------------------------------------------------------------------

// MarketplacePackage is the remote representation of a marketplace package.
type MarketplacePackage struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	AuthorID      string   `json:"author_id"`
	LatestVersion string   `json:"latest_version"`
	Tags          []string `json:"tags"`          // category tags; currently at most one element
	InstallCount  int64    `json:"install_count"` // cumulative installs; updated by Java on each install
	StarCount     int64    `json:"star_count"`    // cumulative stars; updated by Java on star/unstar
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

// PackageStarStatus is returned by the star endpoints.
type PackageStarStatus struct {
	Starred   bool  `json:"starred"`
	StarCount int64 `json:"star_count"`
}

// PackageReview is a user comment on a marketplace package.
type PackageReview struct {
	ID        string `json:"id"`
	PackageID string `json:"package_id"`
	UserName  string `json:"user_name"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// MarketplacePackageVersion is the remote representation of a package version.
type MarketplacePackageVersion struct {
	ID          string `json:"id"`
	PackageID   string `json:"package_id"`
	Version     string `json:"version"`
	Manifest    string `json:"manifest"`
	OssKey      string `json:"oss_key"`
	Changelog   string `json:"changelog"`
	PublishedBy string `json:"published_by"`
	CreatedAt   string `json:"created_at"`
}

// MarketplacePackageDetail combines a package and its versions.
type MarketplacePackageDetail struct {
	Package  MarketplacePackage          `json:"package"`
	Versions []MarketplacePackageVersion `json:"versions"`
}

// MarketplaceStatsReport holds aggregated usage stats to report to the remote.
type MarketplaceStatsReport struct {
	User     string      `json:"user"`
	Installs []NameCount `json:"installs"`
	Calls    []NameCount `json:"calls"`
	Sessions int64       `json:"sessions"`
	Messages int64       `json:"messages"`
}

// NameCount is a name+count pair used in stats reports.
type NameCount struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Count int64  `json:"count"`
}

func (r *RemoteStore) ListMarketplacePackages(pkgType, q, tag, sortBy string, limit, offset int) ([]MarketplacePackage, error) {
	qv := url.Values{}
	if pkgType != "" {
		qv.Set("type", pkgType)
	}
	if q != "" {
		qv.Set("q", q)
	}
	if tag != "" {
		qv.Set("tag", tag)
	}
	if sortBy != "" {
		qv.Set("sort", sortBy)
	}
	qv.Set("limit", strconv.Itoa(limit))
	qv.Set("offset", strconv.Itoa(offset))
	data, err := r.doMktRequest("GET", "/marketplace/packages", qv, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[MarketplacePackage](data)
}

func (r *RemoteStore) GetPackageStarStatus(packageID string) (*PackageStarStatus, error) {
	data, err := r.doMktRequest("GET", "/marketplace/packages/"+packageID+"/star", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSON[PackageStarStatus](data)
}

func (r *RemoteStore) StarPackage(packageID string) (*PackageStarStatus, error) {
	data, err := r.doMktRequest("POST", "/marketplace/packages/"+packageID+"/star", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSON[PackageStarStatus](data)
}

func (r *RemoteStore) UnstarPackage(packageID string) (*PackageStarStatus, error) {
	data, err := r.doMktRequest("DELETE", "/marketplace/packages/"+packageID+"/star", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSON[PackageStarStatus](data)
}

func (r *RemoteStore) ListPackageReviews(packageID string) ([]PackageReview, error) {
	data, err := r.doMktRequest("GET", "/marketplace/packages/"+packageID+"/reviews", nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSONList[PackageReview](data)
}

func (r *RemoteStore) AddPackageReview(packageID string, body map[string]interface{}) (*PackageReview, error) {
	data, err := r.doMktRequest("POST", "/marketplace/packages/"+packageID+"/reviews", nil, body)
	if err != nil {
		return nil, err
	}
	return decodeJSON[PackageReview](data)
}

func (r *RemoteStore) DeletePackageReview(packageID, reviewID string) error {
	_, err := r.doMktRequest("DELETE", "/marketplace/packages/"+packageID+"/reviews/"+reviewID, nil, nil)
	return err
}

func (r *RemoteStore) DeletePackage(packageID string) error {
	_, err := r.doMktRequest("DELETE", "/marketplace/packages/"+packageID, nil, nil)
	return err
}

// RecordPackageInstall increments the install_count on the remote package record.
// Called non-blocking after a successful local install.
func (r *RemoteStore) RecordPackageInstall(packageID string) error {
	_, err := r.doMktRequest("POST", "/marketplace/packages/"+packageID+"/record-install", nil, nil)
	return err
}

func (r *RemoteStore) GetMarketplacePackage(id string) (*MarketplacePackageDetail, error) {
	data, err := r.doMktRequest("GET", "/marketplace/packages/"+id, nil, nil)
	if err != nil {
		return nil, err
	}
	return decodeJSON[MarketplacePackageDetail](data)
}

func (r *RemoteStore) UpdateMarketplacePackage(id string, body map[string]interface{}) (*MarketplacePackage, error) {
	data, err := r.doMktRequest("PATCH", "/marketplace/packages/"+id, nil, body)
	if err != nil {
		return nil, err
	}
	return decodeJSON[MarketplacePackage](data)
}

func (r *RemoteStore) PublishPackage(body map[string]interface{}) (*MarketplacePackageDetail, error) {
	data, err := r.doMktRequest("POST", "/marketplace/packages", nil, body)
	if err != nil {
		return nil, err
	}
	return decodeJSON[MarketplacePackageDetail](data)
}

func (r *RemoteStore) PublishPackageVersion(packageID string, body map[string]interface{}) (*MarketplacePackageVersion, error) {
	data, err := r.doMktRequest("POST", "/marketplace/packages/"+packageID+"/versions", nil, body)
	if err != nil {
		return nil, err
	}
	return decodeJSON[MarketplacePackageVersion](data)
}

// DownloadSkillTar streams the skill tar.gz and writes it to w.
// Returns the total bytes written.
func (r *RemoteStore) DownloadSkillTar(packageID, version string, w io.Writer) (int64, error) {
	if r.localOSSDir != "" {
		detail, err := r.GetMarketplacePackage(packageID)
		if err != nil {
			return 0, fmt.Errorf("local OSS: get package: %w", err)
		}
		var ossKey string
		for _, v := range detail.Versions {
			if v.Version == version {
				ossKey = v.OssKey
				break
			}
		}
		if ossKey == "" {
			return 0, fmt.Errorf("local OSS: oss_key not found for %s@%s", packageID, version)
		}
		f, err := os.Open(filepath.Join(r.localOSSDir, ossKey))
		if err != nil {
			return 0, fmt.Errorf("local OSS open %s: %w", ossKey, err)
		}
		defer f.Close()
		log.Printf("[store] local OSS download: %s@%s → %s", packageID, version, ossKey)
		return io.Copy(w, f)
	}
	u := r.mktBase() + "/marketplace/packages/" + packageID + "/versions/" + version + "/download"
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download skill tar: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, &ApiError{StatusCode: resp.StatusCode, Message: "download failed"}
	}
	return io.Copy(w, resp.Body)
}

// UploadSkillTar sends a multipart file upload to the remote marketplace.
// Returns the oss_key assigned by the server.
func (r *RemoteStore) UploadSkillTar(packageID string, filename string, content io.Reader, size int64) (string, error) {
	if r.localOSSDir != "" {
		dest := filepath.Join(r.localOSSDir, filename)
		f, err := os.Create(dest)
		if err != nil {
			return "", fmt.Errorf("local OSS create %s: %w", filename, err)
		}
		defer f.Close()
		if _, err := io.Copy(f, content); err != nil {
			return "", fmt.Errorf("local OSS write %s: %w", filename, err)
		}
		log.Printf("[store] local OSS upload: %s → %s", filename, dest)
		return filename, nil
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(fw, content); err != nil {
		return "", fmt.Errorf("copy file content: %w", err)
	}
	mw.Close()

	u := r.mktBase() + "/marketplace/packages/" + packageID + "/skill-upload"
	req, err := http.NewRequest("POST", u, &buf)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload skill tar: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", &ApiError{StatusCode: resp.StatusCode, Message: string(body)}
	}
	var result struct {
		OssKey string `json:"oss_key"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("decode upload response: %w", err)
	}
	return result.OssKey, nil
}

func (r *RemoteStore) ReportMarketplaceStats(report MarketplaceStatsReport) error {
	_, err := r.doMktRequest("POST", "/marketplace/stats/report", nil, report)
	return err
}
