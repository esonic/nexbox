package api

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"gitlab.okg.com/sre_ai/boxnex/store"
)

// StartWorkflowScheduler checks every minute if any scheduled workflow is due to run.
func (s *Server) StartWorkflowScheduler(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	// Run once immediately on startup
	s.runScheduledWorkflows()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runScheduledWorkflows()
		}
	}
}

func (s *Server) runScheduledWorkflows() {
	workflows, err := s.Store.ListAllWorkflows()
	if err != nil {
		log.Printf("[scheduler] failed to list workflows: %v", err)
		return
	}

	now := time.Now()
	for _, wf := range workflows {
		if wf.Schedule == "" {
			continue
		}
		if s.isWorkflowRunning(wf.ID) {
			continue
		}

		var sched store.WorkflowSchedule
		if err := json.Unmarshal([]byte(wf.Schedule), &sched); err != nil {
			log.Printf("[scheduler] workflow %s has invalid schedule JSON: %v", wf.ID, err)
			continue
		}

		if !s.isDue(wf.ID, sched, now) {
			continue
		}

		log.Printf("[scheduler] triggering workflow %q (id=%s)", wf.Name, wf.ID)
		if _, err := s.Execute(ExecuteWorkflowRequest{
			WorkflowID:  wf.ID,
			ProjectID:   wf.ProjectID,
			TriggeredBy: "scheduler",
		}); err != nil {
			log.Printf("[scheduler] failed to execute workflow %s: %v", wf.ID, err)
		}
	}
}

// isWorkflowRunning returns true if there's already an active execution for this workflow.
func (s *Server) isWorkflowRunning(workflowID string) bool {
	return isWorkflowRunningByID(workflowID)
}

// isDue returns true if the workflow should be triggered now according to its schedule.
func (s *Server) isDue(workflowID string, sched store.WorkflowSchedule, now time.Time) bool {
	// Fetch the last completed execution to determine elapsed time / last run day
	executions, err := s.Store.ListExecutions(workflowID, 1, 0)
	if err != nil {
		return false
	}

	switch sched.Type {
	case "interval":
		hours := sched.IntervalHours
		if hours < 1 {
			hours = 1
		}
		if hours > 24 {
			hours = 24
		}
		if len(executions) == 0 {
			return true // never run yet
		}
		last := executions[0]
		if last.CompletedAt == nil {
			return false // still running
		}
		return now.Sub(*last.CompletedAt) >= time.Duration(hours)*time.Hour

	case "weekly":
		// Check if today (weekday 0=Sun) is in the Days list
		weekday := int(now.Weekday()) // 0=Sun, 1=Mon, ... 6=Sat
		dayMatches := false
		for _, d := range sched.Days {
			if d == weekday {
				dayMatches = true
				break
			}
		}
		if !dayMatches {
			return false
		}
		// Check if current time is past the scheduled hour:minute
		scheduledTime := time.Date(now.Year(), now.Month(), now.Day(), sched.Hour, sched.Minute, 0, 0, now.Location())
		if now.Before(scheduledTime) {
			return false
		}
		// Check that we haven't already run it today (use local timezone for day boundary)
		if len(executions) == 0 {
			return true
		}
		last := executions[0]
		if last.CompletedAt == nil {
			return false // still running
		}
		loc := now.Location()
		lastLocal := last.CompletedAt.In(loc)
		lastDay := time.Date(lastLocal.Year(), lastLocal.Month(), lastLocal.Day(), 0, 0, 0, 0, loc)
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		return lastDay.Before(today)
	}

	return false
}
