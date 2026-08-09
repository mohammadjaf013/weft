package core

// wireNames maps an internal EventKind (PascalCase Go constant) to the
// dot.lowercase wire name external webhook consumers subscribe to. Keep this
// mapping in ONE place — don't let either naming style leak into the other
// layer. Lives in core (not runtime/webhook) so runtime/store/sqlite can also
// use it when enqueueing outbox rows in the same transaction as the event.
var wireNames = map[EventKind]string{
	EvtJobCreated:       "job.created",
	EvtJobStarted:       "job.started",
	EvtJobProgress:      "job.progress",
	EvtJobPaused:        "job.paused",
	EvtJobResumed:       "job.resumed",
	EvtJobCancelled:     "job.cancelled",
	EvtJobFinished:      "job.completed",
	EvtJobFailed:        "job.failed",
	EvtTaskProgress:     "task.progress",
	EvtStorageUploaded:  "storage.uploaded",
	EvtStorageFailed:    "storage.failed",
	EvtPipelineStarted:  "pipeline.started",
	EvtPipelineFinished: "pipeline.finished",
	EvtPluginStarted:    "plugin.started",
	EvtPluginFinished:   "plugin.finished",
	EvtNodeJoined:       "node.joined",
	EvtNodeLeft:         "node.left",
	EvtNodeHealth:       "node.health",
}

// WireName returns the wire name for k, or k itself (unchanged) when there's
// no dedicated mapping.
func WireName(k EventKind) string {
	if n, ok := wireNames[k]; ok {
		return n
	}
	return string(k)
}
