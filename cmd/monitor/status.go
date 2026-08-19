package main

import (
	"encoding/json"
	"html/template"
	"net/http"
	"sort"
	"sync"
	"time"
)

// targetStatus is the latest known state of one monitored target, as shown
// on the dashboard and JSON API.
type targetStatus struct {
	URL                 string    `json:"url"`
	Up                  bool      `json:"up"`
	Down                bool      `json:"down"` // true once fail-threshold consecutive failures have been reached
	StatusCode          int       `json:"status_code"`
	LatencyMS           int64     `json:"latency_ms"`
	Err                 string    `json:"error,omitempty"`
	CheckedAt           time.Time `json:"checked_at"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
}

// statusStore holds the latest status per target, safe for concurrent use
// by the check loop (writer) and HTTP handlers (readers).
type statusStore struct {
	mu      sync.RWMutex
	targets map[string]targetStatus
}

func newStatusStore() *statusStore {
	return &statusStore{targets: make(map[string]targetStatus)}
}

func (s *statusStore) update(status targetStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets[status.URL] = status
}

// snapshot returns all target statuses sorted by URL, for stable output.
func (s *statusStore) snapshot() []targetStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]targetStatus, 0, len(s.targets))
	for _, t := range s.targets {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

func (s *statusStore) handleJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.snapshot())
}

var dashboardTemplate = template.Must(template.New("dashboard").Parse(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="5">
<title>uptime-monitor</title>
<style>
	body { font-family: system-ui, sans-serif; margin: 2rem; color: #1a1a1a; }
	table { border-collapse: collapse; width: 100%; max-width: 900px; }
	th, td { text-align: left; padding: 0.5rem 1rem; border-bottom: 1px solid #ddd; }
	.up { color: #0a7d2c; font-weight: 600; }
	.down { color: #c62828; font-weight: 600; }
</style>
</head>
<body>
<h1>uptime-monitor</h1>
<table>
<tr><th>Target</th><th>Status</th><th>Code</th><th>Latency</th><th>Last checked</th><th>Consecutive failures</th></tr>
{{range .}}
<tr>
	<td>{{.URL}}</td>
	<td class="{{if .Down}}down{{else}}up{{end}}">{{if .Down}}DOWN{{else}}UP{{end}}</td>
	<td>{{.StatusCode}}</td>
	<td>{{.LatencyMS}}ms</td>
	<td>{{.CheckedAt.Format "2006-01-02 15:04:05"}}</td>
	<td>{{.ConsecutiveFailures}}</td>
</tr>
{{end}}
</table>
</body>
</html>
`))

func (s *statusStore) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	dashboardTemplate.Execute(w, s.snapshot())
}

// newStatusServer builds the status HTTP server. Callers are expected to run
// ListenAndServe in its own goroutine and call Shutdown for a graceful stop.
func newStatusServer(addr string, s *statusStore) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/api/status", s.handleJSON)
	return &http.Server{Addr: addr, Handler: mux}
}
