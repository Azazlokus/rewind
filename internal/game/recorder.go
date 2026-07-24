package game

import "time"

// Recorder receives telemetry from a room. Implementations must be safe for
// concurrent use; internal/metrics has the Prometheus one.
//
// The interface lives here, next to its only caller, so that the simulation
// keeps zero dependencies on the metrics stack.
type Recorder interface {
	// TickDuration observes how long one simulation step took.
	TickDuration(d time.Duration)
	// SnapshotBytes counts bytes queued towards clients.
	SnapshotBytes(n int)
	// ConnectedPlayers reports the current player count of the room.
	ConnectedPlayers(n int)
	// InboxDepth reports how many events were still queued after a tick.
	InboxDepth(n int)
}

// NopRecorder discards telemetry. It is the default for rooms in tests.
type NopRecorder struct{}

func (NopRecorder) TickDuration(time.Duration) {}
func (NopRecorder) SnapshotBytes(int)          {}
func (NopRecorder) ConnectedPlayers(int)       {}
func (NopRecorder) InboxDepth(int)             {}
