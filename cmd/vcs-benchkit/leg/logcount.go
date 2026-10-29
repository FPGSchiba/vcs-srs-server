package leg

import "strings"

// LogCounts are the breach-relevant server log lines (design 6.4.4).
type LogCounts struct {
	ShardQueueFull     uint64 // "Voice shard queue full"
	UpdateStreamBehind uint64 // "Update stream fell behind"
	NodeFeedBehind     uint64 // "Voice node feed fell behind"
	NodeDeregistered   uint64 // "Voice node deregistered"
	HelloAccepted      uint64 // "Accepted voice HELLO"
	HelloRejected      uint64 // "Rejected voice HELLO"
}

// CountLog counts the breach messages and the two voice HELLO outcomes in a server log read from its file.
func CountLog(log string) LogCounts {
	return LogCounts{
		ShardQueueFull:     uint64(strings.Count(log, "Voice shard queue full")),
		UpdateStreamBehind: uint64(strings.Count(log, "Update stream fell behind")),
		NodeFeedBehind:     uint64(strings.Count(log, "Voice node feed fell behind")),
		NodeDeregistered:   uint64(strings.Count(log, "Voice node deregistered")),
		HelloAccepted:      uint64(strings.Count(log, "Accepted voice HELLO")),
		HelloRejected:      uint64(strings.Count(log, "Rejected voice HELLO")),
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
		HelloAccepted:      d(a.HelloAccepted, b.HelloAccepted),
		HelloRejected:      d(a.HelloRejected, b.HelloRejected),
	}
}

// FailedAcked counts the failed bots whose GUID appears on an "Accepted voice
// HELLO" line of the log text added between before and after: the server
// accepted a HELLO the bot gave up on. It returns -1 when no failed entry
// carries a GUID (v1 bot, or a control-stage failure), as then nothing can be
// matched.
func FailedAcked(before, after string, failed []JoinFailure) int {
	delta := after
	if strings.HasPrefix(after, before) {
		delta = after[len(before):]
	}
	var accepted []string
	for _, line := range strings.Split(delta, "\n") {
		if strings.Contains(line, "Accepted voice HELLO") {
			accepted = append(accepted, line)
		}
	}
	known, n := false, 0
	for _, f := range failed {
		if f.GUID == "" {
			continue
		}
		known = true
		for _, line := range accepted {
			if strings.Contains(line, f.GUID) {
				n++
				break
			}
		}
	}
	if !known {
		return -1
	}
	return n
}
