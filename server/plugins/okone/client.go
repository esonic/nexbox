package okone

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// Config holds OKOne project-level configuration.
type Config struct {
	BaseURL        string
	ApplicationID  int
	ServiceID      int
	Service        string // used as both "service" and "serviceName"
	ServiceEnvID   int
	ServiceEnvName string
	Owner          string
}

// Artifact represents a build artifact from OKOne.
type Artifact struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	CommitID  string    `json:"commitId"`
	Branch    string    `json:"branch"`
	CreatedAt time.Time `json:"createdAt"`
	Meta      struct {
		Tag string `json:"tag"`
	} `json:"meta"`
}

// ErrUnauthorized indicates the OKOne token is invalid or expired.
var ErrUnauthorized = fmt.Errorf("OKOne token is invalid or expired. Please open test-okone.okg.com in Chrome and login, then retry")

// FetchLatestArtifact calls OKOne API to get the latest build artifact.
func FetchLatestArtifact(cfg Config, token string) (*Artifact, error) {
	url := fmt.Sprintf("%s/okone/priapi/v1/artifacts/service-environment?serviceEnvironmentId=%d&page=1&limit=10",
		cfg.BaseURL, cfg.ServiceEnvID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	setHeaders(req, cfg, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch artifacts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, ErrUnauthorized
	}

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("OKOne artifacts API returned %d: %s", resp.StatusCode, string(body[:min(200, len(body))]))
	}

	var result struct {
		Code int `json:"code"`
		Data struct {
			Rows []Artifact `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse artifacts response: %w", err)
	}
	if result.Code != 0 {
		return nil, fmt.Errorf("OKOne artifacts API error code: %d", result.Code)
	}
	if len(result.Data.Rows) == 0 {
		return nil, fmt.Errorf("no artifacts found for service environment %d", cfg.ServiceEnvID)
	}
	return &result.Data.Rows[0], nil
}

// CreateOrderParams holds the AI-generated and dynamic fields for a change order.
type CreateOrderParams struct {
	Title              string
	Description        string
	DesignDocument     string
	TestQualityReport  string
	OperationManual    string
	VerificationManual string
	ChangeRisk         string
	RollbackManual     string
}

// CreateChangeOrder calls OKOne API to create a change order.
func CreateChangeOrder(cfg Config, token string, artifact *Artifact, params CreateOrderParams) (int, error) {
	now := time.Now()
	nowMs := now.UnixMilli()
	startTime := now.Format("2006-01-02 15:04")
	artifactIDStr := strconv.Itoa(artifact.ID)

	// Build artifactDisplayName: "name:branch-commitShort (date)"
	commitShort := artifact.CommitID
	if len(commitShort) > 6 {
		commitShort = commitShort[:6]
	}
	artifactDisplay := fmt.Sprintf("%s:%s-%s (%s)",
		artifact.Name, artifact.Branch, commitShort,
		artifact.CreatedAt.Format("2006-01-02 15:04:05"))

	// Build itemDetailJson
	itemDetail := map[string]interface{}{
		"simulateType":           "publish",
		"itemType":               "Service",
		"devTaskConfigs":         []interface{}{},
		"createdAt":              nowMs,
		"serviceId":              cfg.ServiceID,
		"service":                cfg.Service,
		"serviceName":            cfg.Service,
		"serviceModuleId":        cfg.ApplicationID,
		"serviceGroupId":         cfg.ApplicationID,
		"serviceEnvironmentId":   cfg.ServiceEnvID,
		"serviceEnvironmentName": cfg.ServiceEnvName,
		"containers": []map[string]interface{}{
			{
				"containerName":       cfg.ServiceEnvName,
				"artifactId":          artifact.ID,
				"artifactDisplayName": artifactDisplay,
			},
		},
	}
	itemDetailJSON, _ := json.Marshal(itemDetail)

	payload := map[string]interface{}{
		"serviceGroupId": cfg.ApplicationID,
		"changeType":     "Standard",
		"changeServiceItemList": []map[string]interface{}{
			{
				"serviceId":           cfg.ServiceID,
				"serviceEnvironment":  cfg.ServiceEnvID,
				"securityCheckStatus": "true",
				"securityCheckFailedReason": map[string]string{
					artifactIDStr: "(PASSED) SAST: Service passed security checkpoints. Detail Link: -.",
				},
				"securityCheckFailedStage": map[string]string{
					artifactIDStr: "SAST",
				},
				"artifactId":     []int{artifact.ID},
				"configType":     "Service",
				"itemType":       "Service",
				"createdAt":      nowMs,
				"itemDetailJson": string(itemDetailJSON),
				"id":             rand.Float64(),
				"changeSequence": 1,
			},
		},
		"title":              params.Title,
		"owner":              cfg.Owner,
		"startTime":          startTime,
		"description":        params.Description,
		"designDocument":     params.DesignDocument,
		"testQualityReport":  params.TestQualityReport,
		"operationManual":    params.OperationManual,
		"verificationManual": params.VerificationManual,
		"changeRisk":         params.ChangeRisk,
		"rollbackManual":     params.RollbackManual,
		"fireApproval":       true,
		"hitGray":            false,
	}

	payloadJSON, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", cfg.BaseURL+"/okone/priapi/v1/change/management/order/insertOrUpdate", bytes.NewReader(payloadJSON))
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	setHeaders(req, cfg, token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("create change order: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return 0, ErrUnauthorized
	}

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("OKOne create order API returned %d: %s", resp.StatusCode, string(body[:min(200, len(body))]))
	}

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			ID int `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("parse create order response: %w", err)
	}
	if result.Code != 0 {
		return 0, fmt.Errorf("OKOne create order error: code=%d msg=%s", result.Code, result.Msg)
	}
	return result.Data.ID, nil
}

func setHeaders(req *http.Request, cfg Config, token string) {
	appIDStr := strconv.Itoa(cfg.ApplicationID)
	req.Header.Set("authorization", token)
	req.Header.Set("applicationid", appIDStr)
	req.Header.Set("entityid", appIDStr)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("scope", "APPLICATION_NEW")
	req.Header.Set("servicename", "okone")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
