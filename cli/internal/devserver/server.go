package devserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	devScriptPath  = "/__blocks_embed_dev.js"
	devSSEPath     = "/__blocks_dev_sse"
	webDir         = "web"
	indexHTML      = "index.html"
	embedDevScript = "/__blocks_embed_dev.js"
)

// Server is the local dev HTTP server for blocks dev.
type Server struct {
	port           int
	backendBaseURL string
	agents         []string // sorted bare agent names

	sseClients map[chan struct{}]struct{}
	sseMu      sync.Mutex
}

// Config holds the parameters required to start a dev server.
type Config struct {
	Port           int
	BackendBaseURL string
	Agents         []string // bare agent names (will be sorted internally)
}

// New creates a new Server from Config.
func New(cfg Config) *Server {
	agents := make([]string, len(cfg.Agents))
	copy(agents, cfg.Agents)
	sort.Strings(agents)

	return &Server{
		port:           cfg.Port,
		backendBaseURL: cfg.BackendBaseURL,
		agents:         agents,
		sseClients:     make(map[chan struct{}]struct{}),
	}
}

// Run starts the dev server and blocks until Ctrl-C or context cancellation.
func (s *Server) Run(ctx context.Context) error {
	return s.run(ctx, nil)
}

// RunWithAddrChan starts the dev server and sends the bound address on addrCh once listening.
// Used by tests to determine the actual port when port == 0.
func (s *Server) RunWithAddrChan(ctx context.Context, addrCh chan<- string) error {
	return s.run(ctx, addrCh)
}

func (s *Server) run(ctx context.Context, addrCh chan<- string) error {
	requestedPort := s.port
	ln, boundPort, err := listenWithRetry(s.port, portRetryAttempts)
	if err != nil {
		return err
	}
	s.port = boundPort
	if requestedPort != 0 && boundPort != requestedPort {
		fmt.Fprintf(os.Stderr, "Port %d in use; bound to %d instead.\n", requestedPort, boundPort)
	}

	origin := fmt.Sprintf("http://localhost:%d", s.port)

	// Notify callers of the bound address (used by tests).
	if addrCh != nil {
		addrCh <- ln.Addr().String()
	}

	s.printBanner(origin)
	s.warnIfDevScriptMissing()

	mux := http.NewServeMux()
	mux.HandleFunc(devScriptPath, s.serveDevScript)
	mux.HandleFunc(devSSEPath, s.serveSSE)
	mux.Handle("/", s.serveStatic())

	srv := &http.Server{Handler: mux}

	stopWatch := s.watchWebDir()
	defer stopWatch()

	serverErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case err := <-serverErr:
		return fmt.Errorf("devserver: %w", err)
	case <-sigCh:
		fmt.Println("\nShutting down dev server...")
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	return nil
}

// serveDevScript serves /__blocks_embed_dev.js with Cache-Control: no-store.
// The injected script exposes backendBaseUrl, cdmUrl, origin, and agents to
// the embed-auth widget for local-stack overrides.
func (s *Server) serveDevScript(w http.ResponseWriter, r *http.Request) {
	agentsJSON, _ := json.Marshal(s.agents)

	// `cdmUrl` plumbs the local backend's CDM endpoint to the widget so
	// `TaskClient.create` resolves PubNub keysets and `api.baseUrl` from
	// the local stack instead of the production CDM.
	cdmURL := strings.TrimRight(s.backendBaseURL, "/") + "/api/v1/cdm"

	payload := fmt.Sprintf(
		"window.__BLOCKS_EMBED_DEV__ = {\n"+
			"  backendBaseUrl: %q,\n"+
			"  cdmUrl: %q,\n"+
			"  origin: %q,\n"+
			"  agents: %s\n"+
			"};\n",
		s.backendBaseURL,
		cdmURL,
		fmt.Sprintf("http://localhost:%d", s.port),
		agentsJSON,
	)

	// Hot-reload SSE bridge — server emits `reload` on file change.
	payload += fmt.Sprintf(
		"(function () {\n"+
			"  if (typeof EventSource === 'undefined') return;\n"+
			"  try {\n"+
			"    var es = new EventSource(%q);\n"+
			"    es.addEventListener('reload', function () { window.location.reload(); });\n"+
			"  } catch (e) { /* hot reload is best-effort */ }\n"+
			"})();\n",
		devSSEPath,
	)

	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, payload)
}

// serveSSE handles /__blocks_dev_sse for hot-reload events.
func (s *Server) serveSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher.Flush()

	ch := make(chan struct{}, 1)
	s.sseMu.Lock()
	s.sseClients[ch] = struct{}{}
	s.sseMu.Unlock()

	defer func() {
		s.sseMu.Lock()
		delete(s.sseClients, ch)
		s.sseMu.Unlock()
	}()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			fmt.Fprintf(w, "event: reload\ndata: {}\n\n")
			flusher.Flush()
		}
	}
}

// broadcastReload sends a reload event to all SSE clients.
func (s *Server) broadcastReload() {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	for ch := range s.sseClients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// serveStatic returns an http.Handler that serves ./web/ with Cache-Control: no-cache.
func (s *Server) serveStatic() http.Handler {
	fileServer := http.FileServer(http.Dir(webDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip the special-cased paths (handled by their own mux entries).
		if r.URL.Path == devScriptPath || r.URL.Path == devSSEPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})
}

// watchWebDir starts a polling watcher on the ./web/ directory.
// Returns a stop function. Uses polling to avoid external dependencies.
func (s *Server) watchWebDir() func() {
	stop := make(chan struct{})
	go func() {
		snapshots := map[string]time.Time{}
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				changed := false
				_ = filepath.WalkDir(webDir, func(path string, d fs.DirEntry, err error) error {
					if err != nil || d.IsDir() {
						return nil
					}
					info, err := d.Info()
					if err != nil {
						return nil
					}
					prev, seen := snapshots[path]
					snapshots[path] = info.ModTime()
					if seen && !prev.Equal(info.ModTime()) {
						changed = true
					}
					if !seen {
						changed = true
					}
					return nil
				})
				if changed {
					s.broadcastReload()
				}
			}
		}
	}()
	return func() { close(stop) }
}

// printBanner prints startup info.
func (s *Server) printBanner(origin string) {
	fmt.Printf("blocks dev — local embed dev server\n")
	fmt.Printf("  Origin:  %s\n", origin)
	fmt.Printf("  Agents:  %s\n", strings.Join(s.agents, ", "))
	fmt.Printf("  Hot reload:    %s%s\n", origin, devSSEPath)
	fmt.Println()
	fmt.Printf("Listening on %s\n", origin)
}

// portRetryAttempts is the number of sequential ports to try (base, base+1, …)
// before giving up. Mirrors Vite's port-fallback behavior.
const portRetryAttempts = 5

// listenWithRetry tries to bind 127.0.0.1:port; on EADDRINUSE it increments
// the port and retries, up to `attempts` total. Port 0 ("any available") is
// not retried — the OS picks the port. Returns the listener and the port that
// was actually bound.
func listenWithRetry(port, attempts int) (net.Listener, int, error) {
	if port == 0 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, 0, fmt.Errorf("devserver: listen on 127.0.0.1:0: %w", err)
		}
		bound := port
		if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok {
			bound = tcpAddr.Port
		}
		return ln, bound, nil
	}
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		try := port + i
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", try))
		if err == nil {
			return ln, try, nil
		}
		lastErr = err
		if !isAddrInUse(err) {
			return nil, 0, fmt.Errorf("devserver: listen on 127.0.0.1:%d: %w", try, err)
		}
	}
	return nil, 0, fmt.Errorf(
		"ports %d–%d are all in use — stop a conflicting process or run: blocks dev --port <other> (last error: %w)",
		port, port+attempts-1, lastErr,
	)
}

// isAddrInUse returns true when err is an EADDRINUSE listen failure.
func isAddrInUse(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if se, ok := opErr.Err.(*os.SyscallError); ok {
			return se.Err == syscall.EADDRINUSE
		}
	}
	return false
}

// warnIfDevScriptMissing prints a warning when web/index.html exists but
// does not include a reference to /__blocks_embed_dev.js.
func (s *Server) warnIfDevScriptMissing() {
	idxPath := filepath.Join(webDir, indexHTML)
	data, err := os.ReadFile(idxPath)
	if err != nil {
		// No index.html — nothing to warn about.
		return
	}
	if !strings.Contains(string(data), embedDevScript) {
		fmt.Fprintf(os.Stderr,
			"Warning: %s does not reference %s — the page will not load the dev script.\n",
			idxPath, embedDevScript,
		)
	}
}
