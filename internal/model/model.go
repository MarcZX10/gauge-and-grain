// Package model defines the provider-neutral usage record every parser emits.
package model

import "time"

// Field flags record which optional token fields the source actually
// reported, so "0" and "not reported" are never confused.
const (
	HasCacheRead uint8 = 1 << iota
	HasCacheWrite
	HasReasoning
)

// Event is one billed model call (one API response).
//
// Token semantics are normalised across providers:
//
//	Input      uncached input tokens only
//	CacheRead  input tokens served from cache
//	CacheWrite input tokens written to cache (CacheWrite1h is the 1-hour subset)
//	Output     all generated tokens, including reasoning
//	Reasoning  informational subset of Output (not added again to totals)
type Event struct {
	Provider     string
	Model        string
	Session      string
	Project      string
	Time         time.Time
	Key          string // globally unique id used to drop duplicate records
	Input        int64
	CacheRead    int64
	CacheWrite   int64
	CacheWrite1h int64
	Output       int64
	Reasoning    int64
	Has          uint8
}

// Total is the provider-neutral token total: every token billed or served.
func (e *Event) Total() int64 { return e.Input + e.CacheRead + e.CacheWrite + e.Output }

// Merge folds a duplicate record of the same call into e, keeping the
// largest value of each counter (streamed chunks only ever grow).
func (e *Event) Merge(o *Event) {
	e.Input = max(e.Input, o.Input)
	e.CacheRead = max(e.CacheRead, o.CacheRead)
	e.CacheWrite = max(e.CacheWrite, o.CacheWrite)
	e.CacheWrite1h = max(e.CacheWrite1h, o.CacheWrite1h)
	e.Output = max(e.Output, o.Output)
	e.Reasoning = max(e.Reasoning, o.Reasoning)
	e.Has |= o.Has
	if e.Project == "" {
		e.Project = o.Project
	}
	if e.Model == "" || e.Model == "unknown" {
		e.Model = o.Model
	}
}

// ParseStats counts what a parser saw but could not use.
type ParseStats struct {
	Lines     int // candidate lines (matched the cheap pre-filter)
	Malformed int // candidate lines that were not valid JSON / wrong shape
	Anomalies int // negative or absurd counters that were clamped
	Oversized int // lines too large to read
}

// Add accumulates o into s.
func (s *ParseStats) Add(o ParseStats) {
	s.Lines += o.Lines
	s.Malformed += o.Malformed
	s.Anomalies += o.Anomalies
	s.Oversized += o.Oversized
}
