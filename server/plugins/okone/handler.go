package okone

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"gitlab.okg.com/sre_ai/nexbox/config"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

// Handler holds dependencies for OKOne HTTP endpoints.
type Handler struct {
	GetStore    func() store.Store
	Config      *config.Manager
	GetUserName func() string
}

// NewRouter creates the Chi sub-router for OKOne endpoints.
// getStore is called on each request so the handler always uses the current store
// (important: store switches on cloud login/logout).
func NewRouter(getStore func() store.Store, cfgMgr *config.Manager, getUserName func() string) chi.Router {
	h := &Handler{GetStore: getStore, Config: cfgMgr, GetUserName: getUserName}
	r := chi.NewRouter()
	r.Post("/{projectId}/change-order", h.CreateChangeOrder)
	return r
}

// CreateChangeOrder creates an OKOne change order for a project.
func (h *Handler) CreateChangeOrder(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")

	project, err := h.GetStore().GetProject(projectID)
	if err != nil || project == nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	var okoneCfg *config.OKOneConfig
	for _, m := range h.Config.Get().Projects {
		if m.RemoteID == projectID {
			okoneCfg = m.OKOne
			break
		}
	}
	if okoneCfg == nil || okoneCfg.ApplicationID == 0 || okoneCfg.ServiceID == 0 || okoneCfg.ServiceEnvID == 0 {
		writeError(w, http.StatusBadRequest, "OKOne integration not configured for this project. Add an \"okone\" entry for this project in config.json under \"projects\".")
		return
	}

	owner := okoneCfg.Owner
	if owner == "" && h.GetUserName != nil {
		if u := h.GetUserName(); u != "" {
			owner = u + "@okg.com"
		}
	}
	if owner == "" {
		writeError(w, http.StatusBadRequest, "OKOne owner not configured. Set \"owner\" in config.json under \"project_okone\".<project_id>, or use cloud mode.")
		return
	}

	baseURL := okoneCfg.BaseURL
	if baseURL == "" {
		baseURL = "https://test-okone.okg.com"
	}

	apicfg := Config{
		BaseURL:        baseURL,
		ApplicationID:  okoneCfg.ApplicationID,
		ServiceID:      okoneCfg.ServiceID,
		Service:        okoneCfg.Service,
		ServiceEnvID:   okoneCfg.ServiceEnvID,
		ServiceEnvName: okoneCfg.ServiceEnvName,
		Owner:          owner,
	}

	token, err := ReadToken()
	if err != nil {
		if errors.Is(err, ErrTokenExpired) || errors.Is(err, ErrTokenNotFound) {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "Failed to read OKOne token: "+err.Error())
		return
	}

	log.Printf("[okone] Fetching latest artifact for service env %d...", apicfg.ServiceEnvID)
	artifact, err := FetchLatestArtifact(apicfg, token)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "Failed to fetch artifact: "+err.Error())
		return
	}
	log.Printf("[okone] Using artifact: %d (%s)", artifact.ID, artifact.Name)

	params := CreateOrderParams{
		Title:              project.Name + " update",
		Description:        "Service update: " + project.Name,
		DesignDocument:     "Standard deployment following existing architecture.",
		TestQualityReport:  "Verified in development environment.",
		OperationManual:    "Standard deployment via OKOne.",
		VerificationManual: "Check service metrics and monitor logs.",
		ChangeRisk:         "Standard deployment procedure.",
		RollbackManual:     "Rollback to previous version via OKOne.",
	}

	log.Printf("[okone] Creating change order: %s", params.Title)
	orderID, err := CreateChangeOrder(apicfg, token, artifact, params)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "Failed to create change order: "+err.Error())
		return
	}

	orderURL := fmt.Sprintf("%s/application/%d/change/order/%d", baseURL, okoneCfg.ApplicationID, orderID)
	log.Printf("[okone] Change order created: %s", orderURL)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"order_id":  orderID,
		"order_url": orderURL,
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
