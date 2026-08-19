package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// headerFlags accumulates repeated -header "Name: Value" flags into a
// name->value map, applied to every target checked in this run.
type headerFlags map[string]string

func (h headerFlags) String() string {
	return fmt.Sprintf("%v", map[string]string(h))
}

func (h headerFlags) Set(value string) error {
	name, val, ok := strings.Cut(value, ":")
	if !ok {
		return fmt.Errorf("invalid -header %q, expected \"Name: Value\"", value)
	}
	h[strings.TrimSpace(name)] = strings.TrimSpace(val)
	return nil
}

func main() {
	urlList := flag.String("urls", "https://example.com", "comma-separated list of target URLs to check")
	timeout := flag.Duration("timeout", 5*time.Second, "request timeout per check")
	interval := flag.Duration("interval", 0, "if set, repeat checks on this interval instead of running once")
	failThreshold := flag.Int("fail-threshold", 3, "consecutive failures required before a target is marked DOWN")
	dbPath := flag.String("db", "monitor.db", "path to SQLite database file")
	webhookURL := flag.String("webhook", "", "if set, POST a JSON payload here on every DOWN/RECOVERED transition")
	listenAddr := flag.String("listen", "", "if set, serve a status dashboard and JSON API on this address (e.g. :8080)")
	configPath := flag.String("config", "", "path to a JSON config file; explicit CLI flags override its values")
	headers := make(headerFlags)
	flag.Var(headers, "header", `extra header sent with every check, as "Name: Value" (repeatable)`)
	flag.Parse()

	explicit := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	var cfg fileConfig
	if *configPath != "" {
		loaded, err := loadConfig(*configPath)
		if err != nil {
			slog.Error("loading config", "path", *configPath, "err", err)
			os.Exit(2)
		}
		cfg = loaded
	}

	var urls []string
	if len(cfg.URLs) > 0 && !explicit["urls"] {
		urls = cfg.URLs
	} else {
		for _, u := range strings.Split(*urlList, ",") {
			if u = strings.TrimSpace(u); u != "" {
				urls = append(urls, u)
			}
		}
	}
	if len(urls) == 0 {
		fmt.Fprintln(os.Stderr, "no target URLs provided")
		os.Exit(2)
	}

	if cfg.Timeout != 0 && !explicit["timeout"] {
		*timeout = time.Duration(cfg.Timeout)
	}
	if cfg.Interval != 0 && !explicit["interval"] {
		*interval = time.Duration(cfg.Interval)
	}
	if cfg.FailThreshold != 0 && !explicit["fail-threshold"] {
		*failThreshold = cfg.FailThreshold
	}
	if cfg.DB != "" && !explicit["db"] {
		*dbPath = cfg.DB
	}
	if cfg.Webhook != "" && !explicit["webhook"] {
		*webhookURL = cfg.Webhook
	}
	if cfg.Listen != "" && !explicit["listen"] {
		*listenAddr = cfg.Listen
	}
	requestHeaders := map[string]string(headers)
	if len(cfg.Headers) > 0 && !explicit["header"] {
		requestHeaders = cfg.Headers
	}

	// The same headers are sent with every target's requests. That's a
	// deliberate simplification (not per-target headers): every current
	// target is our own infrastructure, so there's no risk in one target
	// seeing a header meant for another.
	var targets []target
	for _, u := range urls {
		targets = append(targets, target{URL: u, Headers: requestHeaders})
	}

	db, err := openStore(*dbPath)
	if err != nil {
		slog.Error("opening store", "err", err)
		os.Exit(1)
	}

	initialFailures, initialDown, err := db.loadFailureState()
	if err != nil {
		slog.Error("loading failure state", "err", err)
		os.Exit(1)
	}

	client := &http.Client{Timeout: *timeout}
	track := newTracker(*failThreshold, initialFailures, initialDown)
	notify := newNotifier(*webhookURL)
	status := newStatusStore()

	// Ctrl+C (SIGINT) and SIGTERM (e.g. from a process supervisor stopping
	// the service) both cancel ctx, triggering the graceful shutdown below
	// instead of an abrupt kill.
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	var srv *http.Server
	if *listenAddr != "" {
		srv = newStatusServer(*listenAddr, status)
		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("status server", "err", err)
			}
		}()
		fmt.Printf("serving status dashboard on http://%s (Ctrl+C to stop)\n", *listenAddr)
	}

	shutdown := func() {
		fmt.Println("shutting down...")
		if srv != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				slog.Error("status server shutdown", "err", err)
			}
		}
		db.Close()
	}

	if *interval <= 0 {
		anyDown := runRound(client, db, track, notify, status, targets)
		if *listenAddr != "" {
			fmt.Println("holding status dashboard open; press Ctrl+C to stop")
			<-ctx.Done()
			shutdown()
		} else {
			db.Close()
		}
		if anyDown {
			os.Exit(1)
		}
		return
	}

	fmt.Printf("monitoring %d target(s) every %s, alerting after %d consecutive failures (Ctrl+C to stop)\n",
		len(targets), *interval, *failThreshold)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	runRound(client, db, track, notify, status, targets)

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-ticker.C:
			runRound(client, db, track, notify, status, targets)
		}
	}

	shutdown()
}

// runRound checks every target once, persists each result and the updated
// failure state, fires a webhook on any state transition, and prints
// per-check status plus any transitions. It reports whether any target was
// down on this round.
func runRound(client *http.Client, db *store, track *tracker, notify *notifier, status *statusStore, targets []target) (anyDown bool) {
	results := runChecks(client, targets)

	for range targets {
		result := <-results
		up := result.Err == nil && result.StatusCode < 400

		if err := db.record(result, up); err != nil {
			slog.Warn("recording result failed", "target", result.URL, "err", err)
		}

		printResult(result, up)

		transition := track.observe(result.URL, up)

		if err := db.saveFailureState(result.URL, track.failures[result.URL], track.down[result.URL]); err != nil {
			slog.Warn("saving failure state failed", "target", result.URL, "err", err)
		}

		status.update(targetStatus{
			URL:                 result.URL,
			Up:                  up,
			Down:                track.down[result.URL],
			StatusCode:          result.StatusCode,
			LatencyMS:           result.Latency.Milliseconds(),
			Err:                 errString(result.Err),
			CheckedAt:           time.Now().UTC(),
			ConsecutiveFailures: track.failures[result.URL],
		})

		switch transition {
		case wentDown:
			message := fmt.Sprintf("%d consecutive failures, last: %s", track.threshold, failureDetail(result))
			fmt.Printf("ALERT  %s is DOWN (%s)\n", result.URL, message)
			if err := notify.notify(result.URL, true, message); err != nil {
				slog.Warn("webhook notify failed", "target", result.URL, "err", err)
			}
		case wentUp:
			fmt.Printf("ALERT  %s has RECOVERED\n", result.URL)
			if err := notify.notify(result.URL, false, "recovered"); err != nil {
				slog.Warn("webhook notify failed", "target", result.URL, "err", err)
			}
		}

		if !up {
			anyDown = true
		}
	}

	return anyDown
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// failureDetail distinguishes "couldn't even reach it" (a network-level
// error: the instance/network is down) from "reached it, and it says
// something's wrong" (a non-2xx/3xx response, with its body if one was
// captured) so alerts say which one actually happened instead of a generic
// "down".
func failureDetail(result checkResult) string {
	if result.Err != nil {
		return fmt.Sprintf("unreachable (%v)", result.Err)
	}
	if result.Body != "" {
		return fmt.Sprintf("status %d: %s", result.StatusCode, result.Body)
	}
	return fmt.Sprintf("status %d", result.StatusCode)
}

func printResult(result checkResult, up bool) {
	status := "UP"
	if !up {
		status = "DOWN"
	}
	if result.Err != nil {
		fmt.Printf("%s  %s  error=%v  latency=%s\n", status, result.URL, result.Err, result.Latency)
		return
	}
	if result.Body != "" {
		fmt.Printf("%s  %s  status=%d  latency=%s  body=%s\n", status, result.URL, result.StatusCode, result.Latency, result.Body)
		return
	}
	fmt.Printf("%s  %s  status=%d  latency=%s\n", status, result.URL, result.StatusCode, result.Latency)
}
