package main

// transition describes how a target's reported state changed after
// observing a check result.
type transition int

const (
	noChange transition = iota
	wentDown
	wentUp
)

// tracker keeps a per-target consecutive-failure count so a target is only
// reported DOWN after failing threshold times in a row, instead of on every
// transient blip.
type tracker struct {
	threshold int
	failures  map[string]int
	down      map[string]bool
}

// newTracker builds a tracker starting from persisted failure/down state
// (as loaded by store.loadFailureState), so consecutive-failure counts
// survive a process restart instead of resetting to zero on every run. Pass
// nil maps to start completely fresh.
func newTracker(threshold int, failures map[string]int, down map[string]bool) *tracker {
	if failures == nil {
		failures = make(map[string]int)
	}
	if down == nil {
		down = make(map[string]bool)
	}
	return &tracker{threshold: threshold, failures: failures, down: down}
}

// observe records whether the latest check for url succeeded and reports
// any resulting state transition.
func (t *tracker) observe(url string, up bool) transition {
	if up {
		t.failures[url] = 0
		if t.down[url] {
			t.down[url] = false
			return wentUp
		}
		return noChange
	}

	t.failures[url]++
	if !t.down[url] && t.failures[url] >= t.threshold {
		t.down[url] = true
		return wentDown
	}
	return noChange
}
