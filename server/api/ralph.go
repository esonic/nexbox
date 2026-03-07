package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gitlab.okg.com/sre_ai/nexbox/store"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type RalphWorkerState struct {
	Index       int    `json:"index"`
	SessionID   string `json:"session_id,omitempty"`
	CurrentTask *struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"current_task"`
	Completed int `json:"completed"`
}

type RalphPool struct {
	ID         string
	ProjectID  string
	WorkflowID string
	Workers    int
	Prompt     string

	ctx    context.Context
	cancel context.CancelFunc

	mu             sync.Mutex
	workerState    []RalphWorkerState
	activeSessions map[string]bool // session IDs currently executing
	completed      int
	failed         int
	totalTasks     int
	status         string // running | idle | stopped
}

// In-memory pool registry (only active pools)
var (
	ralphMu    sync.RWMutex
	ralphPools = make(map[string]*RalphPool)
)

// ---------------------------------------------------------------------------
// Pool lifecycle
// ---------------------------------------------------------------------------

func (s *Server) startRalphPool(projectID, workflowID, prompt string, workers int) (*RalphPool, error) {
	ctx, cancel := context.WithCancel(context.Background())

	pool := &RalphPool{
		ID:             uuid.New().String(),
		ProjectID:      projectID,
		WorkflowID:     workflowID,
		Workers:        workers,
		Prompt:         prompt,
		ctx:            ctx,
		cancel:         cancel,
		workerState:    make([]RalphWorkerState, workers),
		activeSessions: make(map[string]bool),
		status:         "running",
	}
	for i := range pool.workerState {
		pool.workerState[i] = RalphWorkerState{Index: i}
	}

	// Count open tasks with ralph label
	tasks, _ := s.Store.ListTasks(1000, 0, []string{projectID}, "")
	for _, t := range tasks {
		if t.Status == "open" && hasRalphLabel(t.Labels) {
			pool.totalTasks++
		}
	}

	// Persist run to DB
	s.execSQL(`INSERT INTO ralph_runs (id, project_id, workflow_id, prompt, workers, status, total_tasks) VALUES (?, ?, ?, ?, ?, 'running', ?)`,
		pool.ID, projectID, workflowID, prompt, workers, pool.totalTasks)

	ralphMu.Lock()
	ralphPools[pool.ID] = pool
	ralphMu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			s.runRalphWorker(pool, idx)
		}(i)
	}

	go func() {
		wg.Wait()

		pool.mu.Lock()
		if pool.status == "running" {
			pool.status = "idle"
		}
		finalStatus := pool.status
		completed := pool.completed
		failed := pool.failed
		pool.mu.Unlock()

		dbStatus := finalStatus
		if dbStatus == "idle" {
			dbStatus = "completed"
		}
		s.execSQL(`UPDATE ralph_runs SET status = ?, total_completed = ?, total_failed = ?, completed_at = ? WHERE id = ?`,
			dbStatus, completed, failed, time.Now(), pool.ID)

		s.emitRalphProgress(pool.ID, map[string]interface{}{"event": "pool_done", "status": dbStatus})
		log.Printf("[ralph] Pool %s finished: status=%s completed=%d failed=%d", pool.ID, dbStatus, completed, failed)

		ralphMu.Lock()
		delete(ralphPools, pool.ID)
		ralphMu.Unlock()
	}()

	log.Printf("[ralph] Started pool %s: %d workers, %d open tasks, project=%s", pool.ID, workers, pool.totalTasks, projectID)
	return pool, nil
}

func (s *Server) stopRalphPool(poolID string) error {
	ralphMu.RLock()
	pool, ok := ralphPools[poolID]
	ralphMu.RUnlock()
	if !ok {
		return fmt.Errorf("pool %s not found or already finished", poolID)
	}

	pool.mu.Lock()
	pool.status = "stopped"
	sessionIDs := make([]string, 0, len(pool.activeSessions))
	for sid := range pool.activeSessions {
		sessionIDs = append(sessionIDs, sid)
	}
	pool.mu.Unlock()

	// Cancel the pool context (prevents new task picks)
	pool.cancel()

	// Immediately cancel all running AI sessions
	for _, sid := range sessionIDs {
		s.SessionManager.Cancel(sid)
	}
	if len(sessionIDs) > 0 {
		log.Printf("[ralph] Stopped pool %s: cancelled %d active sessions", poolID, len(sessionIDs))
	}

	return nil
}

// ---------------------------------------------------------------------------
// Worker loop
// ---------------------------------------------------------------------------

func (s *Server) runRalphWorker(pool *RalphPool, workerIdx int) {
	for {
		if pool.ctx.Err() != nil {
			return
		}

		task := s.ralphPickTask(pool)
		if task == nil {
			return
		}

		// Log: worker picked
		logID := uuid.New().String()
		s.execSQL(`INSERT INTO ralph_worker_logs (id, run_id, worker_index, task_id, task_title, status) VALUES (?, ?, ?, ?, ?, 'running')`,
			logID, pool.ID, workerIdx, task.ID, task.Title)

		pool.mu.Lock()
		pool.workerState[workerIdx].CurrentTask = &struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}{ID: task.ID, Title: task.Title}
		pool.mu.Unlock()

		s.emitRalphProgress(pool.ID, map[string]interface{}{
			"event": "worker_picked", "worker": workerIdx,
			"task_id": task.ID, "task_title": task.Title,
		})

		err := s.ralphExecuteTask(pool, workerIdx, task, logID)

		pool.mu.Lock()
		pool.workerState[workerIdx].CurrentTask = nil
		pool.mu.Unlock()

		stopped := pool.ctx.Err() != nil

		if err != nil {
			// Reset task to open so it can be retried
			openStatus := "open"
			s.Store.UpdateTask(task.ID, store.UpdateTaskRequest{Status: &openStatus})
			s.Hub.NotifyChange("task", task.ID, "updated")

			if stopped {
				log.Printf("[ralph] Worker %d stopped mid-task %s", workerIdx, task.ID)
				s.execSQL(`UPDATE ralph_worker_logs SET status = 'stopped', completed_at = ? WHERE id = ?`, time.Now(), logID)
				s.emitRalphProgress(pool.ID, map[string]interface{}{
					"event": "worker_stopped", "worker": workerIdx, "task_id": task.ID,
				})
				return
			}

			log.Printf("[ralph] Worker %d failed task %s: %v", workerIdx, task.ID, err)
			s.execSQL(`UPDATE ralph_worker_logs SET status = 'failed', completed_at = ? WHERE id = ?`, time.Now(), logID)

			pool.mu.Lock()
			pool.failed++
			pool.mu.Unlock()

			s.emitRalphProgress(pool.ID, map[string]interface{}{
				"event": "worker_failed", "worker": workerIdx,
				"task_id": task.ID, "error": err.Error(),
			})
		} else {
			doneStatus := "done"
			s.Store.UpdateTask(task.ID, store.UpdateTaskRequest{Status: &doneStatus})
			s.Hub.NotifyChange("task", task.ID, "updated")

			s.execSQL(`UPDATE ralph_worker_logs SET status = 'completed', completed_at = ? WHERE id = ?`, time.Now(), logID)

			pool.mu.Lock()
			pool.workerState[workerIdx].Completed++
			pool.completed++
			pool.mu.Unlock()

			s.emitRalphProgress(pool.ID, map[string]interface{}{
				"event": "worker_completed", "worker": workerIdx, "task_id": task.ID,
			})
		}

		// Periodic DB sync
		pool.mu.Lock()
		c, f := pool.completed, pool.failed
		pool.mu.Unlock()
		s.execSQL(`UPDATE ralph_runs SET total_completed = ?, total_failed = ? WHERE id = ?`, c, f, pool.ID)

		if pool.ctx.Err() != nil {
			return
		}
	}
}

const ralphLabel = "ralph"

func hasRalphLabel(labels []string) bool {
	for _, l := range labels {
		if l == ralphLabel {
			return true
		}
	}
	return false
}

func (s *Server) ralphPickTask(pool *RalphPool) *store.Task {
	tasks, err := s.Store.ListTasks(200, 0, []string{pool.ProjectID}, "")
	if err != nil {
		log.Printf("[ralph] Failed to list tasks: %v", err)
		return nil
	}

	for _, t := range tasks {
		if t.Status != "open" || !hasRalphLabel(t.Labels) {
			continue
		}
		if pool.ctx.Err() != nil {
			return nil
		}

		inProgress := "in_progress"
		assigner := fmt.Sprintf("ralph:%s", pool.ID)
		_, err := s.Store.UpdateTask(t.ID, store.UpdateTaskRequest{
			Status:          &inProgress,
			Assigner:        &assigner,
			ExpectedVersion: &t.Version,
		})
		if err != nil {
			continue // CAS失败，被其他worker抢走
		}
		s.Hub.NotifyChange("task", t.ID, "updated")
		return &t
	}
	return nil
}

func (s *Server) ralphExecuteTask(pool *RalphPool, workerIdx int, task *store.Task, logID string) error {
	prompt := fmt.Sprintf("%s\n\n---\n\n[Task]\nTitle: %s\nDescription:\n%s", pool.Prompt, task.Title, task.Description)
	parts := []MessagePart{
		{Type: "text", Content: pool.Prompt},
		{Type: "task_ref", TaskID: task.ID},
	}

	sessionName := fmt.Sprintf("RALPH: W%d — %s", workerIdx+1, task.Title)
	if len(sessionName) > 60 {
		sessionName = sessionName[:57] + "..."
	}

	agent := s.getDefaultAgent()
	session, err := s.Store.CreateSession(store.CreateSessionRequest{
		Name:      sessionName,
		ProjectID: pool.ProjectID,
		Agent:     agent,
	})
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	pool.mu.Lock()
	pool.workerState[workerIdx].SessionID = session.ID
	pool.activeSessions[session.ID] = true
	pool.mu.Unlock()

	s.execSQL(`UPDATE ralph_worker_logs SET session_id = ? WHERE id = ?`, session.ID, logID)
	s.Hub.NotifyChange("session", session.ID, "created")

	s.handleUserMessage(session.ID, prompt, parts, nil, nil)
	_, err = s.waitSessionDone(pool.ctx, session.ID)

	pool.mu.Lock()
	delete(pool.activeSessions, session.ID)
	pool.mu.Unlock()

	return err
}

// ---------------------------------------------------------------------------
// WebSocket events
// ---------------------------------------------------------------------------

func (s *Server) emitRalphProgress(poolID string, payload map[string]interface{}) {
	payload["type"] = "ralph_progress"
	payload["ralph_id"] = poolID
	data, _ := json.Marshal(payload)
	s.Hub.BroadcastRaw(data)
}

// ---------------------------------------------------------------------------
// DB helper
// ---------------------------------------------------------------------------

func (s *Server) execSQL(query string, args ...interface{}) {
	if localStore := s.LocalStore; localStore != nil {
		if _, err := localStore.DB().Exec(query, args...); err != nil {
			log.Printf("[ralph] DB exec error: %v", err)
		}
	}
}

func (s *Server) queryRalphRuns(projectID string, limit int) ([]map[string]interface{}, error) {
	db := s.LocalStore
	if db == nil {
		return nil, fmt.Errorf("no local store")
	}
	if limit <= 0 {
		limit = 20
	}

	q := `SELECT id, project_id, workflow_id, workers, status, total_tasks, total_completed, total_failed, created_at, completed_at FROM ralph_runs`
	var args []interface{}
	if projectID != "" {
		q += ` WHERE project_id = ?`
		args = append(args, projectID)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.DB().Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var id, pid, wid, status string
		var workers, totalTasks, totalCompleted, totalFailed int
		var createdAt time.Time
		var completedAt sql.NullTime

		if err := rows.Scan(&id, &pid, &wid, &workers, &status, &totalTasks, &totalCompleted, &totalFailed, &createdAt, &completedAt); err != nil {
			continue
		}

		// Check if this run is still active in memory
		ralphMu.RLock()
		if pool, ok := ralphPools[id]; ok {
			pool.mu.Lock()
			status = pool.status
			totalCompleted = pool.completed
			totalFailed = pool.failed
			pool.mu.Unlock()
		}
		ralphMu.RUnlock()

		item := map[string]interface{}{
			"id":              id,
			"project_id":      pid,
			"workflow_id":     wid,
			"workers":         workers,
			"status":          status,
			"total_tasks":     totalTasks,
			"total_completed": totalCompleted,
			"total_failed":    totalFailed,
			"created_at":      createdAt.Format(time.RFC3339),
		}
		if completedAt.Valid {
			item["completed_at"] = completedAt.Time.Format(time.RFC3339)
		}
		results = append(results, item)
	}
	return results, nil
}

func (s *Server) queryRalphWorkerLogs(runID string) ([]map[string]interface{}, error) {
	db := s.LocalStore
	if db == nil {
		return nil, fmt.Errorf("no local store")
	}

	rows, err := db.DB().Query(
		`SELECT id, worker_index, task_id, task_title, session_id, status, started_at, completed_at
		 FROM ralph_worker_logs WHERE run_id = ? ORDER BY started_at ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var id, taskID, taskTitle, sessionID, status string
		var workerIdx int
		var startedAt time.Time
		var completedAt sql.NullTime

		if err := rows.Scan(&id, &workerIdx, &taskID, &taskTitle, &sessionID, &status, &startedAt, &completedAt); err != nil {
			continue
		}
		item := map[string]interface{}{
			"id":           id,
			"worker_index": workerIdx,
			"task_id":      taskID,
			"task_title":   taskTitle,
			"session_id":   sessionID,
			"status":       status,
			"started_at":   startedAt.Format(time.RFC3339),
		}
		if completedAt.Valid {
			item["completed_at"] = completedAt.Time.Format(time.RFC3339)
		}
		results = append(results, item)
	}
	return results, nil
}

// ---------------------------------------------------------------------------
// API Handlers
// ---------------------------------------------------------------------------

func (s *Server) HandleStartRalph(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID  string `json:"project_id"`
		WorkflowID string `json:"workflow_id"`
		Workers    int    `json:"workers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.ProjectID == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	if req.Workers <= 0 {
		req.Workers = 1
	}
	if req.Workers > 16 {
		req.Workers = 16
	}

	// Extract prompt template from workflow's first AI node
	prompt := "Complete the following task."
	if req.WorkflowID != "" {
		wf, err := s.Store.GetWorkflow(req.WorkflowID)
		if err != nil || wf == nil {
			writeError(w, http.StatusNotFound, "workflow not found")
			return
		}
		var def store.WorkflowDefinition
		if err := json.Unmarshal(wf.Definition, &def); err == nil {
			for _, n := range def.Nodes {
				if n.Type == "ai" {
					if cfg, ok := n.Data["config"].(map[string]interface{}); ok {
						if p, ok := cfg["prompt"].(string); ok && p != "" {
							prompt = p
							break
						}
					}
				}
			}
		}
	}

	pool, err := s.startRalphPool(req.ProjectID, req.WorkflowID, prompt, req.Workers)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":          pool.ID,
		"status":      "running",
		"workers":     pool.Workers,
		"total_tasks": pool.totalTasks,
	})
}

func (s *Server) HandleStopRalph(w http.ResponseWriter, r *http.Request) {
	poolID := chi.URLParam(r, "id")
	if err := s.stopRalphPool(poolID); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) HandleGetRalphRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")

	// Try in-memory first for live data
	ralphMu.RLock()
	pool, active := ralphPools[runID]
	ralphMu.RUnlock()

	if active {
		pool.mu.Lock()
		workers := make([]RalphWorkerState, len(pool.workerState))
		copy(workers, pool.workerState)
		status := pool.status
		completed := pool.completed
		failed := pool.failed
		total := pool.totalTasks
		pool.mu.Unlock()

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"id":              runID,
			"status":          status,
			"workers":         workers,
			"total_tasks":     total,
			"total_completed": completed,
			"total_failed":    failed,
		})
		return
	}

	// Fall back to DB for historical runs
	logs, err := s.queryRalphWorkerLogs(runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":          runID,
		"worker_logs": logs,
	})
}

func (s *Server) HandleListRalphRuns(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	runs, err := s.queryRalphRuns(projectID, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runs == nil {
		runs = []map[string]interface{}{}
	}
	writeJSON(w, http.StatusOK, runs)
}

// ---------------------------------------------------------------------------
// Routes
// ---------------------------------------------------------------------------

func (s *Server) RegisterRalphRoutes(r chi.Router) {
	r.Route("/ralph", func(r chi.Router) {
		r.Post("/start", s.HandleStartRalph)
		r.Get("/runs", s.HandleListRalphRuns)
		r.Get("/runs/{id}", s.HandleGetRalphRun)
		r.Post("/runs/{id}/stop", s.HandleStopRalph)
	})
}
