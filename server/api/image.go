package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
)

const maxImageSize = 5 << 20 // 5 MB

var allowedImageTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

func (s *Server) sessionImageDir(sessionID string) string {
	return filepath.Join(s.DataDir, "sessions", sessionID, "images")
}

// UploadImage handles multipart image uploads for a session.
// POST /api/sessions/{sessionId}/upload-image
func (s *Server) UploadImage(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")

	r.Body = http.MaxBytesReader(w, r.Body, maxImageSize)
	if err := r.ParseMultipartForm(maxImageSize); err != nil {
		writeError(w, http.StatusBadRequest, "file too large (max 5MB)")
		return
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing image field")
		return
	}
	defer file.Close()

	ct := header.Header.Get("Content-Type")
	ext, ok := allowedImageTypes[ct]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported image type: %s", ct))
		return
	}

	imgDir := s.sessionImageDir(sessionID)
	if err := os.MkdirAll(imgDir, 0755); err != nil {
		slog.Error("failed to create image directory", "session", sessionID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create image directory")
		return
	}

	randBytes := make([]byte, 12)
	rand.Read(randBytes)
	filename := hex.EncodeToString(randBytes) + ext

	dst, err := os.Create(filepath.Join(imgDir, filename))
	if err != nil {
		slog.Error("failed to create image file", "session", sessionID, "file", filename, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create file")
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		slog.Error("failed to save image file", "session", sessionID, "file", filename, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to save file")
		return
	}

	slog.Info("image uploaded", "session", sessionID, "file", filename)

	writeJSON(w, http.StatusOK, map[string]string{
		"filename": filename,
		"url":      fmt.Sprintf("/api/sessions/%s/image/%s", sessionID, filename),
	})
}

// ServeImage serves a previously uploaded image.
// GET /api/sessions/{sessionId}/image/{filename}
func (s *Server) ServeImage(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	filename := chi.URLParam(r, "filename")

	if strings.Contains(filename, "..") || strings.ContainsAny(filename, "/\\") {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	imgPath := filepath.Join(s.sessionImageDir(sessionID), filename)
	http.ServeFile(w, r, imgPath)
}
