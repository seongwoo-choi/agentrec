package cli

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/seongwoo-choi/agentrec/internal/report"
)

const (
	viewUsage         = "usage: agentrec view [<run-id>|latest] [--listen <loopback-address>] [--no-open] [--allow-run]\n"
	defaultViewListen = "127.0.0.1:0"
	promptFile        = "prompt.txt"
)

//go:embed ui_assets/index.html ui_assets/app.css ui_assets/app.js
var viewAssets embed.FS

type viewField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type viewRunSummary struct {
	ID           string    `json:"id"`
	Provider     string    `json:"provider"`
	Project      string    `json:"project"`
	StartedAt    time.Time `json:"startedAt"`
	Exit         string    `json:"exit"`
	Verification string    `json:"verification"`
	StatusClass  string    `json:"statusClass"`
	StatusLabel  string    `json:"statusLabel"`
}

func viewStatusClass(value string) string {
	switch strings.ToLower(value) {
	case "pass", "passed", "completed", "success":
		return "pass"
	// session_lost is the session analogue of interrupted: the recording ended
	// without the session saying so. session_ended stays neutral, as completed
	// would if the process result were unknown.
	case "fail", "failed", "error", "timeout", "nonzero", "interrupted", "parse_error", "storage_error", "start_error", reasonSessionLost:
		return "fail"
	default:
		return ""
	}
}

func viewRunListStatus(exit, verification string) (string, string) {
	if viewStatusClass(exit) == "fail" {
		return "fail", exit
	}
	return viewStatusClass(verification), verification
}

type viewRunInfo struct {
	ID                string     `json:"id"`
	Provider          string     `json:"provider"`
	ProviderVersion   string     `json:"providerVersion,omitempty"`
	Project           string     `json:"project"`
	CWD               string     `json:"cwd"`
	Prompt            string     `json:"prompt,omitempty"`
	StartedAt         time.Time  `json:"startedAt"`
	EndedAt           *time.Time `json:"endedAt,omitempty"`
	ExitReason        string     `json:"exitReason,omitempty"`
	WarningCount      int        `json:"warningCount"`
	UnparsedLines     int        `json:"unparsedLines"`
	VersionUnverified bool       `json:"versionUnverified,omitempty"`
	// Mode and SessionID are set for a run recorded from an interactive
	// session's hooks, so the viewer can say which kind of run it shows.
	Mode      string `json:"mode,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

type viewEvidence struct {
	ProviderUsage []viewField `json:"providerUsage,omitempty"`
	Supervisor    []viewField `json:"supervisor"`
	Repository    []viewField `json:"repository"`
	Verification  []viewField `json:"verification"`
}

type viewProviderEvents struct {
	Attribution string `json:"attribution"`
	Present     bool   `json:"present"`
}

type viewChangeSummary struct {
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Attribution string `json:"attribution,omitempty"`
	Baseline    string `json:"baseline,omitempty"`
	Total       int    `json:"total"`
	Tracked     int    `json:"tracked"`
	Untracked   int    `json:"untracked"`
	Additions   int    `json:"additions"`
	Deletions   int    `json:"deletions"`
	Binary      int    `json:"binary"`
}

type viewRunResponse struct {
	SchemaVersion  int                `json:"schemaVersion"`
	SnapshotID     string             `json:"snapshotId"`
	ActionCount    int                `json:"actionCount"`
	EventCount     int                `json:"eventCount"`
	Run            viewRunInfo        `json:"run"`
	ProviderEvents viewProviderEvents `json:"providerEvents"`
	Changes        viewChangeSummary  `json:"changes"`
	Evidence       viewEvidence       `json:"evidence"`
}

type viewRunListResponse struct {
	SchemaVersion int              `json:"schemaVersion"`
	InitialRunID  string           `json:"initialRunId"`
	Unreadable    int              `json:"unreadable"`
	Runs          []viewRunSummary `json:"runs"`
}

func runView(args []string, stdout, stderr io.Writer) int {
	runID, listen, open, allowRun, ok := parseViewArgs(args)
	if !ok {
		fmt.Fprint(stderr, viewUsage)
		return 2
	}
	root, err := runsRoot()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if runID == latestRun {
		runID, err = newestRunID(root)
		switch {
		case errors.Is(err, errNoRuns):
			// Nothing recorded yet is a state the viewer shows, not a reason
			// to refuse: the list is empty until the first session ends.
			runID = ""
		case err != nil:
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if runID != "" {
		runRoot, err := openRunRoot(root, runID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := runRoot.Close(); err != nil {
			fmt.Fprintf(stderr, "cli: close run %s: %v\n", runID, err)
			return 1
		}
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		fmt.Fprintf(stderr, "cli: start viewer: %v\n", err)
		return 1
	}
	defer listener.Close()
	viewerURL := "http://" + listener.Addr().String() + "/"
	fmt.Fprintf(stdout, "agentrec viewer: %s\n", viewerURL)
	if open {
		if err := openViewBrowser(viewerURL); err != nil {
			fmt.Fprintf(stderr, "Warning: browser did not open: %v\n", err)
		}
	}

	handler := newViewHandler(root, runID, allowRun)
	defer handler.Close()
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "cli: serve viewer: %v\n", err)
		return 1
	}
	return 0
}

func parseViewArgs(args []string) (runID, listen string, open, allowRun, ok bool) {
	runID, listen, open, ok = latestRun, defaultViewListen, true, true
	runSet, listenSet := false, false
	for len(args) > 0 {
		switch args[0] {
		case "--no-open":
			if !open {
				return "", "", false, false, false
			}
			open = false
			args = args[1:]
		case "--allow-run":
			if allowRun {
				return "", "", false, false, false
			}
			allowRun = true
			args = args[1:]
		case "--listen":
			if listenSet || len(args) < 2 || !isLoopbackListen(args[1]) {
				return "", "", false, false, false
			}
			listenSet, listen = true, args[1]
			args = args[2:]
		default:
			if runSet || (args[0] != latestRun && validateRunID(args[0]) != nil) {
				return "", "", false, false, false
			}
			runSet, runID = true, args[0]
			args = args[1:]
		}
	}
	return runID, listen, open, allowRun, true
}

func isLoopbackListen(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func openViewBrowser(target string) error {
	if _, err := url.ParseRequestURI(target); err != nil {
		return err
	}
	var command string
	switch runtime.GOOS {
	case "darwin":
		command = "open"
	case "linux":
		command = "xdg-open"
	default:
		return fmt.Errorf("browser opening is unsupported on %s", runtime.GOOS)
	}
	return exec.Command(command, target).Start()
}

type viewHandler struct {
	http.Handler
	snapshots *viewSnapshotStore
	jobs      *shadowJobs
	// token is what a request that changes the store must carry, in a
	// header only this page's own script can send: a page on any other
	// origin cannot read it (no CORS) and cannot set the header without a
	// preflight this server refuses.
	token string
}

func (h *viewHandler) Close() error {
	h.jobs.Close()
	return h.snapshots.Close()
}

func newViewHandler(root, initialRunID string, allowRun bool) *viewHandler {
	snapshots := newViewSnapshotStore(root)
	token := newViewToken()
	jobs := newShadowJobs(root, allowRun)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/shadow", func(w http.ResponseWriter, _ *http.Request) {
		writeViewJSON(w, jobs.overview())
	})
	mux.HandleFunc("POST /api/shadow/jobs", func(w http.ResponseWriter, r *http.Request) {
		if !viewMutationAllowed(r, token) {
			writeViewError(w, http.StatusForbidden, errors.New("a request that changes the store must come from this page"))
			return
		}
		var req shadowJobRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 6*shadowTaskLimit+4096)).Decode(&req); err != nil {
			writeViewError(w, http.StatusBadRequest, errors.New("invalid comparison request"))
			return
		}
		id, err := jobs.start(req)
		switch {
		case errors.Is(err, errShadowDisabled):
			writeViewError(w, http.StatusForbidden, err)
		case errors.Is(err, errShadowBusy), errors.Is(err, errShadowClosing):
			writeViewError(w, http.StatusConflict, err)
		case err != nil:
			writeViewError(w, http.StatusBadRequest, err)
		default:
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
		}
	})
	mux.HandleFunc("GET /api/shadow/jobs/{jobID}", func(w http.ResponseWriter, r *http.Request) {
		since := int64(0)
		if raw := r.URL.Query().Get("since"); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || parsed < 0 {
				writeViewError(w, http.StatusBadRequest, errors.New("invalid offset"))
				return
			}
			since = parsed
		}
		view, err := jobs.get(r.PathValue("jobID"), since)
		if err != nil {
			writeViewError(w, http.StatusNotFound, err)
			return
		}
		writeViewJSON(w, view)
	})
	mux.HandleFunc("POST /api/shadow/jobs/{jobID}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !viewMutationAllowed(r, token) {
			writeViewError(w, http.StatusForbidden, errors.New("a request that changes the store must come from this page"))
			return
		}
		switch err := jobs.cancel(r.PathValue("jobID")); {
		case errors.Is(err, errShadowNotFound):
			writeViewError(w, http.StatusNotFound, err)
		case errors.Is(err, errShadowNotOpen):
			writeViewError(w, http.StatusConflict, err)
		case err != nil:
			writeViewError(w, http.StatusInternalServerError, err)
		default:
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusAccepted)
		}
	})
	mux.HandleFunc("GET /api/token", func(w http.ResponseWriter, _ *http.Request) {
		writeViewJSON(w, map[string]string{"token": token})
	})
	mux.HandleFunc("DELETE /api/runs/{runID}", func(w http.ResponseWriter, r *http.Request) {
		if !viewMutationAllowed(r, token) {
			writeViewError(w, http.StatusForbidden, errors.New("a request that changes the store must come from this page"))
			return
		}
		writeViewMutation(w, trashRun(root, r.PathValue("runID")))
	})
	mux.HandleFunc("POST /api/runs/{runID}/restore", func(w http.ResponseWriter, r *http.Request) {
		if !viewMutationAllowed(r, token) {
			writeViewError(w, http.StatusForbidden, errors.New("a request that changes the store must come from this page"))
			return
		}
		writeViewMutation(w, restoreRun(root, r.PathValue("runID")))
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		serveViewAsset(w, "ui_assets/index.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("GET /assets/app.css", func(w http.ResponseWriter, _ *http.Request) {
		serveViewAsset(w, "ui_assets/app.css", "text/css; charset=utf-8")
	})
	mux.HandleFunc("GET /assets/app.js", func(w http.ResponseWriter, _ *http.Request) {
		serveViewAsset(w, "ui_assets/app.js", "text/javascript; charset=utf-8")
	})
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, _ *http.Request) {
		runs, unreadable, err := listRunsForTable(root, "", false, "")
		if err != nil {
			writeViewError(w, http.StatusInternalServerError, err)
			return
		}
		out := make([]viewRunSummary, 0, len(runs))
		for _, run := range runs {
			statusClass, statusLabel := viewRunListStatus(run.Exit, run.Verification)
			out = append(out, viewRunSummary{
				ID: run.ID, Provider: run.Provider, Project: run.Project,
				StartedAt: run.StartedAt, Exit: run.Exit, Verification: run.Verification,
				StatusClass: statusClass, StatusLabel: statusLabel,
			})
		}
		initial := initialRunID
		if initial != "" && initial != latestRun {
			initial = ""
			for _, run := range out {
				if run.ID == initialRunID {
					initial = initialRunID
					break
				}
			}
		}
		writeViewJSON(w, viewRunListResponse{1, initial, unreadable, out})
	})
	mux.HandleFunc("GET /api/runs/{runID}/live", func(w http.ResponseWriter, r *http.Request) {
		live, err := readLiveChanges(root, r.PathValue("runID"))
		switch {
		case errors.Is(err, errRunNotRunning):
			writeViewError(w, http.StatusConflict, err)
		case errors.Is(err, os.ErrNotExist):
			writeViewError(w, http.StatusNotFound, err)
		case err != nil:
			writeViewError(w, http.StatusBadRequest, err)
		default:
			writeViewJSON(w, live)
		}
	})
	mux.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		limit := 0
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil {
				limit = parsed
			}
		}
		result, err := searchRuns(root, r.URL.Query().Get("q"), limit)
		if err != nil {
			writeViewError(w, http.StatusBadRequest, err)
			return
		}
		writeViewJSON(w, result)
	})
	mux.HandleFunc("GET /api/runs/{runID}", func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("runID")
		if runID == "" || validateRunID(runID) != nil || path.Base(runID) != runID {
			writeViewError(w, http.StatusBadRequest, errors.New("invalid run id"))
			return
		}
		var out viewRunResponse
		err := snapshots.withSlot(r, func() error {
			var err error
			out, err = snapshots.createContext(r.Context(), runID)
			return err
		})
		if err != nil {
			writeViewError(w, http.StatusNotFound, err)
			return
		}
		writeViewJSON(w, out)
	})
	mux.HandleFunc("GET /api/snapshots/{snapshotID}/{stream}", func(w http.ResponseWriter, r *http.Request) {
		cursor, err := parseViewCursor(r)
		if err != nil {
			writeViewError(w, http.StatusBadRequest, err)
			return
		}
		found := false
		err = snapshots.withSlot(r, func() error {
			var innerErr error
			found, innerErr = snapshots.withSnapshot(r.PathValue("snapshotID"), func(snapshot *viewSnapshot) error {
				switch r.PathValue("stream") {
				case "actions":
					page, err := readViewActionPage(snapshot, cursor)
					if err == nil {
						writeViewJSON(w, page)
					}
					return err
				case "events":
					page, err := readViewEventPage(snapshot, cursor)
					if err == nil {
						writeViewJSON(w, page)
					}
					return err
				case "changes":
					page, err := readViewChangePage(snapshot, cursor)
					if err == nil {
						writeViewJSON(w, page)
					}
					return err
				case "patch":
					changePath := r.URL.Query().Get("path")
					if changePath == "" || !utf8.ValidString(changePath) {
						return errors.New("invalid change path")
					}
					page, err := readViewPatchPage(snapshot, changePath, cursor)
					if err == nil {
						writeViewJSON(w, page)
					}
					return err
				default:
					return errors.New("unknown viewer stream")
				}
			})
			return innerErr
		})
		if !found {
			writeViewError(w, http.StatusNotFound, errors.New("viewer snapshot expired"))
			return
		}
		if err != nil {
			writeViewError(w, http.StatusBadRequest, err)
		}
	})
	return &viewHandler{Handler: viewSecurity(mux), snapshots: snapshots, jobs: jobs, token: token}
}

func newViewToken() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic("cli: viewer token: " + err.Error())
	}
	return hex.EncodeToString(raw[:])
}

// viewMutationAllowed accepts a request that changes the store only from
// this page: the token in the header only same-origin script can read and
// send, and browser-provided fetch metadata that says the same.
func viewMutationAllowed(r *http.Request, token string) bool {
	got := r.Header.Get("X-Agentrec-Token")
	if len(got) != len(token) || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
		return false
	}
	return true
}

// writeViewMutation answers a delete or restore: nothing on success, and on
// failure the status that says why.
func writeViewMutation(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, os.ErrNotExist), errors.Is(err, errNotInTrash):
		writeViewError(w, http.StatusNotFound, err)
	case errors.Is(err, errRunOpen), errors.Is(err, errRunClosing), errors.Is(err, errRunExists):
		writeViewError(w, http.StatusConflict, err)
	default:
		writeViewError(w, http.StatusBadRequest, err)
	}
}

func readViewPrompt(root *os.Root) (string, error) {
	raw, err := readDocumentFromRoot(root, promptFile)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("cli: %s is not valid UTF-8", promptFile)
	}
	return strings.TrimSuffix(string(raw), "\n"), nil
}

func viewFields(fields []report.Field) []viewField {
	out := make([]viewField, 0, len(fields))
	for _, field := range fields {
		out = append(out, viewField{Name: field.Name, Value: field.Value})
	}
	return out
}

func serveViewAsset(w http.ResponseWriter, name, contentType string) {
	raw, err := viewAssets.ReadFile(name)
	if err != nil {
		http.Error(w, "viewer asset unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

func writeViewJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

func writeViewError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func viewSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if split, _, err := net.SplitHostPort(host); err == nil {
			host = split
		}
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		switch r.Method {
		case http.MethodGet, http.MethodDelete, http.MethodPost:
		default:
			w.Header().Set("Allow", "GET, POST, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}
