package leg

import "strings"

// LogCounts are the breach-relevant server log lines (design 6.4.4).
type LogCounts struct {
	ShardQueueFull     uint64 // "Voice shard queue full"
	UpdateStreamBehind uint64 // "Update stream fell behind"
	NodeFeedBehind     uint64 // "Voice node feed fell behind"
	NodeDeregistered   uint64 // "Voice node deregistered"
}

// CountLog counts the four messages in a server log read from its file.
func CountLog(log string) LogCounts {
	return LogCounts{
		ShardQueueFull:     uint64(strings.Count(log, "Voice shard queue full")),
		UpdateStreamBehind: uint64(strings.Count(log, "Update stream fell behind")),
		NodeFeedBehind:     uint64(strings.Count(log, "Voice node feed fell behind")),
		NodeDeregistered:   uint64(strings.Count(log, "Voice node deregistered")),
	}
}

// Sub is the per-window increase a - b. A count that went down (a log that
// was truncated) yields 0, never an underflow that would fake a breach.
func (a LogCounts) Sub(b LogCounts) LogCounts {
	d := func(x, y uint64) uint64 {
		if x < y {
			return 0
		}
		return x - y
	}
	return LogCounts{
		ShardQueueFull:     d(a.ShardQueueFull, b.ShardQueueFull),
		UpdateStreamBehind: d(a.UpdateStreamBehind, b.UpdateStreamBehind),
		NodeFeedBehind:     d(a.NodeFeedBehind, b.NodeFeedBehind),
		NodeDeregistered:   d(a.NodeDeregistered, b.NodeDeregistered),
	}
}
