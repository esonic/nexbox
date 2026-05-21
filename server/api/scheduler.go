package api

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/esonic/nexbox/store"
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

		if !s.isDue(wf.ID, sched, wf.ScheduleUpdatedAt, now) {
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
// scheduleUpdatedAt is when the schedule was last saved by the user; we use it
// to avoid firing retroactively for a slot that had already passed when the
// schedule was set (e.g. saving "Wed 09:00" on Wed at 14:00 must not fire today).
func (s *Server) isDue(workflowID string, sched store.WorkflowSchedule, scheduleUpdatedAt time.Time, now time.Time) bool {
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
			// First run: wait `hours` from when the schedule was set, so that
			// saving "every 4h" does not fire within the next minute.
			if scheduleUpdatedAt.IsZero() {
				return true // legacy schedule from before updated_at was tracked
			}
			return now.Sub(scheduleUpdatedAt) >= time.Duration(hours)*time.Hour
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
		// Don't fire retroactively: the schedule must have existed before
		// today's scheduled slot. (Zero means a legacy row from before this
		// column existed — fall through to preserve prior behaviour.)
		if !scheduleUpdatedAt.IsZero() && scheduleUpdatedAt.After(scheduledTime) {
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
