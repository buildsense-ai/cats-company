package server

import (
	"context"
	"log"
	"time"
)

const artifactRuntimeRunReaperBatch = 100

func (h *Hub) runArtifactRuntimeRunReaper() {
	if h == nil || h.artifactTasks == nil || h.artifactTasks.runtimeRuns == nil {
		return
	}
	interval := h.taskReaperInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	reap := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := h.artifactTasks.runtimeRuns.ConvergeArtifactRuntimeRuns(
			ctx, time.Now().UTC(), h.artifactTasks.agentGrace, artifactRuntimeRunReaperBatch,
		)
		cancel()
		if err != nil {
			log.Printf("artifact runtime run reaper: %v", err)
		}
	}
	reap()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		reap()
	}
}
