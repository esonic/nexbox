package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/esonic/nexbox/store"
)

func (s *Server) ListAssets(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	assetType := r.URL.Query().Get("type")
	search := r.URL.Query().Get("q")

	var assets []Asset
	var err error
	if assetType != "" {
		assets, err = s.Store.ListAssetsByType(projectID, assetType, limit, offset, search)
	} else {
		assets, err = s.Store.ListAssets(projectID, limit, offset, search)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if assets == nil {
		assets = []Asset{}
	}
	writeJSON(w, http.StatusOK, assets)
}

func (s *Server) CreateAsset(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	var req CreateAssetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Title == "" || req.Type == "" {
		writeError(w, http.StatusBadRequest, "title and type are required")
		return
	}

	asset, err := s.Store.CreateAsset(projectID, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Hub.NotifyChange("asset", asset.ID, "created")
	writeJSON(w, http.StatusCreated, asset)
}

func (s *Server) GetAsset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "assetId")
	asset, err := s.Store.GetAsset(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if asset == nil {
		writeError(w, http.StatusNotFound, "asset not found")
		return
	}
	content, _ := s.Store.GetLatestAssetContent(id)
	writeJSON(w, http.StatusOK, store.AssetWithContent{Asset: *asset, Content: content})
}

func (s *Server) UpdateAsset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "assetId")
	var req UpdateAssetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	asset, err := s.Store.UpdateAsset(id, req)
	if err != nil {
		if errors.Is(err, store.ErrVersionConflict) {
			writeError(w, http.StatusConflict, "version conflict: asset was modified by another client")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if asset == nil {
		writeError(w, http.StatusNotFound, "asset not found")
		return
	}
	s.Hub.NotifyChange("asset", id, "updated")
	writeJSON(w, http.StatusOK, asset)
}

func (s *Server) DeleteAsset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "assetId")
	if err := s.Store.DeleteAsset(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Hub.NotifyChange("asset", id, "deleted")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ListAssetVersions(w http.ResponseWriter, r *http.Request) {
	assetID := chi.URLParam(r, "assetId")
	versions, err := s.Store.GetAssetVersions(assetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if versions == nil {
		versions = []AssetVersion{}
	}
	writeJSON(w, http.StatusOK, versions)
}

type diffLine struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

func (s *Server) DiffAssetVersions(w http.ResponseWriter, r *http.Request) {
	assetID := chi.URLParam(r, "assetId")
	v1Str := r.URL.Query().Get("v1")
	v2Str := r.URL.Query().Get("v2")

	if v1Str == "" || v2Str == "" {
		writeError(w, http.StatusBadRequest, "v1 and v2 query params are required")
		return
	}

	v1, err := strconv.Atoi(v1Str)
	if err != nil {
		writeError(w, http.StatusBadRequest, "v1 must be an integer")
		return
	}
	v2, err := strconv.Atoi(v2Str)
	if err != nil {
		writeError(w, http.StatusBadRequest, "v2 must be an integer")
		return
	}

	ver1, err := s.Store.GetAssetVersion(assetID, v1)
	if err != nil || ver1 == nil {
		writeError(w, http.StatusNotFound, "version v1 not found")
		return
	}
	ver2, err := s.Store.GetAssetVersion(assetID, v2)
	if err != nil || ver2 == nil {
		writeError(w, http.StatusNotFound, "version v2 not found")
		return
	}

	dmp := diffmatchpatch.New()
	chars1, chars2, lineArray := dmp.DiffLinesToChars(ver1.Content, ver2.Content)
	diffs := dmp.DiffMain(chars1, chars2, false)
	diffs = dmp.DiffCharsToLines(diffs, lineArray)

	var lines []diffLine
	for _, d := range diffs {
		var dtype string
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			dtype = "equal"
		case diffmatchpatch.DiffInsert:
			dtype = "insert"
		case diffmatchpatch.DiffDelete:
			dtype = "delete"
		}
		lines = append(lines, diffLine{Type: dtype, Content: d.Text})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"asset_id": assetID,
		"version1": v1,
		"version2": v2,
		"diffs":    lines,
		"old_text": ver1.Content,
		"new_text": ver2.Content,
	})
}
