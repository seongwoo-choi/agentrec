package cli

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
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
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	WarningCount int       `json:"warningCount"`
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
	case "tainted":
		return "warn"
	default:
		return ""
	}
}

func viewRunStatus(exit, verification string, verificationWarnings int) (string, string) {
	if viewStatusClass(exit) == "fail" {
		return "fail", exit
	}
	class := viewStatusClass(verification)
	if class == "pass" && verificationWarnings > 0 {
		class = "warn"
	}
	return class, verification
}

func viewRunListStatus(exit, verification string) (string, string) {
	return viewRunStatus(exit, verification, 0)
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
	StatusClass       string     `json:"statusClass"`
	StatusLabel       string     `json:"statusLabel"`
	WarningCount      int        `json:"warningCount"`
	UnparsedLines     int        `json:"unparsedLines"`
	VersionUnverified bool       `json:"versionUnverified,omitempty"`
	// Mode and SessionID are set for a run recorded from an interactive
	// session's hooks, so the viewer can say which kind of run it shows.
	Mode      string `json:"mode,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

type viewEvidence struct {
	ProviderUsage        []viewField              `json:"providerUsage,omitempty"`
	Supervisor           []viewField              `json:"supervisor"`
	Repository           []viewField              `json:"repository"`
	Verification         []viewField              `json:"verification"`
	PosthocVerification  *viewPosthocVerification `json:"posthocVerification"`
	verificationStatus   string
	verificationWarnings int
}

// viewPosthocVerification is a verification run after the fact: the same
// rows as the run-end one, with when it was measured and whether HEAD moved.
type viewPosthocVerification struct {
	// OwnRan is false when the run itself was never verified, so the page
	// can say so rather than let a later verdict stand for the run's own.
	OwnRan         bool        `json:"ownRan"`
	MeasuredAt     time.Time   `json:"measuredAt,omitzero"`
	HeadMovedSince *bool       `json:"headMovedSince"`
	Status         string      `json:"status"`
	Caveat         string      `json:"caveat"`
	Fields         []viewField `json:"fields"`
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
	StoreBytes    int64            `json:"storeBytes"`
	TrashBytes    int64            `json:"trashBytes"`
	Total         int              `json:"total"`
	Generation    string           `json:"generation"`
	PageIDs       []string         `json:"pageIds"`
	NextCursor    string           `json:"nextCursor,omitempty"`
	Runs          []viewRunSummary `json:"runs"`
}

const viewRunPageSize = 50

func encodeViewRunCursor(generation, position string) string {
	return base64.RawURLEncoding.EncodeToString([]byte("v1\x00" + generation + "\x00" + position))
}

func decodeViewRunCursor(cursor string) (string, string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	parts := strings.Split(string(decoded), "\x00")
	if err != nil || len(parts) != 3 || parts[0] != "v1" || parts[1] == "" {
		return "", "", errors.New("invalid run-list cursor")
	}
	position, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || position <= 0 {
		return "", "", errors.New("invalid run-list cursor")
	}
	return parts[1], strconv.FormatInt(position, 10), nil
}

type viewRunPage struct {
	runs       []runSummary
	ids        []string
	unreadable int
	total      int
	nextCursor string
	generation string
}

type viewRunListCache struct {
	mu          sync.Mutex
	root        string
	stamp       string
	initialized bool
	page        viewRunPage
	scan        func(string) (viewRunPage, error)
}

func newViewRunListCache(root string) *viewRunListCache {
	cache := &viewRunListCache{root: root}
	cache.scan = func(cursor string) (viewRunPage, error) {
		return scanViewRunPage(root, cursor)
	}
	return cache
}

func (c *viewRunListCache) list() (viewRunPage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stamp, err := viewRunsStamp(c.root)
	if err != nil {
		return viewRunPage{}, err
	}
	if !c.initialized || c.stamp != stamp {
		c.page, err = c.scan("")
		if err != nil {
			return viewRunPage{}, err
		}
		c.initialized = true
		c.stamp = stamp
	} else {
		refreshed := make([]runSummary, 0, len(c.page.ids))
		unreadable := 0
		for _, runID := range c.page.ids {
			run, err := readViewRunSummary(c.root, runID)
			if err != nil {
				unreadable++
				continue
			}
			refreshed = append(refreshed, run)
		}
		c.page.runs = refreshed
		c.page.unreadable = unreadable
	}
	page := c.page
	page.runs = slices.Clone(page.runs)
	page.ids = slices.Clone(page.ids)
	return page, nil
}

func (c *viewRunListCache) continuation(cursor string) (viewRunPage, error) {
	return c.scan(cursor)
}

func scanViewRunPage(root, cursor string) (viewRunPage, error) {
	return scanViewRunPageWithRead(root, cursor, readViewRunSummaryFromRoot)
}

func scanViewRunPageWithRead(root, cursor string, read func(*os.Root, string) (runSummary, error)) (viewRunPage, error) {
	var offset int64
	if cursor != "" {
		var err error
		offset, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil || offset <= 0 {
			return viewRunPage{}, errors.New("cursor is outside the run index")
		}
	}
	entries, next, total, generation, err := viewRunIndexPage(root, offset)
	if err != nil {
		return viewRunPage{}, err
	}
	page := viewRunPage{ids: make([]string, 0, len(entries)), total: total, generation: generation}
	if next > 0 {
		page.nextCursor = strconv.FormatInt(next, 10)
	}
	if len(entries) == 0 {
		return page, nil
	}
	runsRoot, err := os.OpenRoot(root)
	if err != nil {
		return viewRunPage{}, fmt.Errorf("cli: open runs directory: %w", err)
	}
	defer runsRoot.Close()
	for _, entry := range entries {
		page.ids = append(page.ids, entry.id)
		run, err := read(runsRoot, entry.id)
		if err != nil {
			page.unreadable++
			continue
		}
		page.runs = append(page.runs, run)
	}
	return page, nil
}

func viewRunsStamp(root string) (string, error) {
	info, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", fmt.Errorf("cli: stat runs directory: %w", err)
	}
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size()), nil
}

func readViewRunSummary(root, runID string) (runSummary, error) {
	runsRoot, err := os.OpenRoot(root)
	if err != nil {
		return runSummary{}, err
	}
	defer runsRoot.Close()
	return readViewRunSummaryFromRoot(runsRoot, runID)
}

func readViewRunSummaryFromRoot(root *os.Root, runID string) (runSummary, error) {
	runRoot, err := openRunRootFromRoot(root, runID)
	if err != nil {
		return runSummary{}, err
	}
	defer runRoot.Close()
	manifest, err := readManifestFromRoot(runRoot)
	if err != nil {
		return runSummary{}, err
	}
	run := runSummary{
		ID: runID, Provider: manifest.Provider, Project: projectName(manifest.CWD),
		StartedAt: manifest.StartedAt, Exit: exitReason(manifest, nil), WarningCount: manifest.WarningCount,
	}
	verification, err := readVerificationFromRoot(runRoot)
	if err != nil {
		return runSummary{}, err
	}
	run.Verification = verificationNotRun
	if verification != nil {
		run.Verification = verdict(verification.Status)
		run.VerificationWarnings = len(verification.Warnings)
	}
	return run, nil
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

	identityToken := takeViewerIdentityToken()
	handler := newViewHandlerWithIdentity(root, runID, allowRun, identityToken)
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		select {
		case <-ctx.Done():
		case <-handler.shutdown:
		}
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	code := 0
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "cli: serve viewer: %v\n", err)
		code = 1
	}
	return closeView(handler, stderr, code)
}

func takeViewerIdentityToken() string {
	token := os.Getenv(viewerIdentityEnv)
	_ = os.Unsetenv(viewerIdentityEnv)
	return token
}

func closeView(closer io.Closer, stderr io.Writer, code int) int {
	if err := closer.Close(); err != nil {
		fmt.Fprintf(stderr, "cli: close viewer: %v\n", err)
		return 1
	}
	return code
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
	runList   *viewRunListCache
	// token is what a request that changes the store must carry, in a
	// header only this page's own script can send: a page on any other
	// origin cannot read it (no CORS) and cannot set the header without a
	// preflight this server refuses.
	token        string
	shutdown     chan struct{}
	shutdownOnce sync.Once
}

func (h *viewHandler) requestShutdown() {
	h.shutdownOnce.Do(func() { close(h.shutdown) })
}

func (h *viewHandler) Close() error {
	h.jobs.Close()
	return h.snapshots.Close()
}

// viewStoreSizes remembers what the store and the trash take on disk for a
// little while, so a list refresh does not walk every run each time.
type viewStoreSizes struct {
	root  string
	mu    sync.Mutex
	at    time.Time
	store int64
	trash int64
}

const viewStoreSizeTTL = 30 * time.Second

func (c *viewStoreSizes) get() (int64, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at.IsZero() || time.Since(c.at) > viewStoreSizeTTL {
		c.store = storeBytes(c.root)
		c.trash = storeBytes(trashRootFor(c.root))
		c.at = time.Now()
	}
	return c.store, c.trash
}

func newViewHandler(root, initialRunID string, allowRun bool) *viewHandler {
	return newViewHandlerWithIdentity(root, initialRunID, allowRun, "")
}

func newViewHandlerWithIdentity(root, initialRunID string, allowRun bool, identityToken string) *viewHandler {
	snapshots := newViewSnapshotStore(root)
	sizes := &viewStoreSizes{root: root}
	token := newViewToken()
	jobs := newShadowJobs(root, allowRun)
	handler := &viewHandler{snapshots: snapshots, jobs: jobs, runList: newViewRunListCache(root), token: token, shutdown: make(chan struct{})}
	mux := http.NewServeMux()
	if identityToken != "" {
		executable, _ := os.Executable()
		executable, _ = filepath.EvalSymlinks(executable)
		identityAllowed := func(r *http.Request) bool {
			got := r.Header.Get(viewerIdentityHeader)
			return len(got) == len(identityToken) && subtle.ConstantTimeCompare([]byte(got), []byte(identityToken)) == 1
		}
		mux.HandleFunc("GET /api/viewer-identity", func(w http.ResponseWriter, r *http.Request) {
			if !identityAllowed(r) {
				writeViewError(w, http.StatusForbidden, errors.New("viewer identity challenge failed"))
				return
			}
			writeViewJSON(w, map[string]any{"pid": os.Getpid(), "executable": executable})
		})
		mux.HandleFunc("POST /api/viewer-stop", func(w http.ResponseWriter, r *http.Request) {
			if !identityAllowed(r) {
				writeViewError(w, http.StatusForbidden, errors.New("viewer identity challenge failed"))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			handler.requestShutdown()
		})
	}
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
	verifying := make(chan struct{}, 1)
	mux.HandleFunc("POST /api/runs/{runID}/verify", func(w http.ResponseWriter, r *http.Request) {
		if !viewMutationAllowed(r, token) {
			writeViewError(w, http.StatusForbidden, errors.New("a request that changes the store must come from this page"))
			return
		}
		if !allowRun {
			writeViewError(w, http.StatusForbidden, errors.New("start the viewer with --allow-run to verify a run from this page"))
			return
		}
		select {
		case verifying <- struct{}{}:
			defer func() { <-verifying }()
		default:
			writeViewError(w, http.StatusConflict, errVerifyBusy)
			return
		}
		result, meta, err := verifyRunLater(r.Context(), root, r.PathValue("runID"))
		switch {
		case err == nil:
			writeViewJSON(w, newViewPosthocVerification(&result, &meta))
		case errors.Is(err, os.ErrNotExist):
			writeViewError(w, http.StatusNotFound, err)
		case errors.Is(err, errRunOpen), errors.Is(err, errRunClosing), errors.Is(err, errVerifyBusy):
			writeViewError(w, http.StatusConflict, err)
		case errors.Is(err, errVerifyNoRepo), errors.Is(err, errVerifyRepoGone), errors.Is(err, errVerifyNoConfig):
			writeViewError(w, http.StatusUnprocessableEntity, err)
		default:
			writeViewError(w, http.StatusInternalServerError, err)
		}
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
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		cursorGeneration := ""
		decodedCursor := ""
		if cursor != "" {
			var err error
			cursorGeneration, decodedCursor, err = decodeViewRunCursor(cursor)
			if err != nil {
				writeViewError(w, http.StatusBadRequest, err)
				return
			}
		}
		var page viewRunPage
		var err error
		if decodedCursor == "" {
			page, err = handler.runList.list()
		} else {
			page, err = handler.runList.continuation(decodedCursor)
		}
		if err != nil {
			if cursor != "" {
				writeViewError(w, http.StatusBadRequest, err)
			} else {
				writeViewError(w, http.StatusInternalServerError, err)
			}
			return
		}
		if cursorGeneration != "" && cursorGeneration != page.generation {
			writeViewError(w, http.StatusBadRequest, errors.New("run-list cursor is stale"))
			return
		}
		out := make([]viewRunSummary, 0, len(page.runs))
		for _, run := range page.runs {
			statusClass, statusLabel := viewRunStatus(run.Exit, run.Verification, run.VerificationWarnings)
			out = append(out, viewRunSummary{
				ID: run.ID, Provider: run.Provider, Project: run.Project,
				StartedAt: run.StartedAt, Exit: run.Exit, Verification: run.Verification,
				StatusClass: statusClass, StatusLabel: statusLabel, WarningCount: run.WarningCount + run.VerificationWarnings,
			})
		}
		initial := initialRunID
		if initial == latestRun {
			initial = ""
		} else if initial != "" {
			runRoot, err := openRunRoot(root, initial)
			if err != nil {
				initial = ""
			} else {
				runRoot.Close()
			}
		}
		store, trash := sizes.get()
		nextCursor := ""
		if page.nextCursor != "" {
			nextCursor = encodeViewRunCursor(page.generation, page.nextCursor)
		}
		writeViewJSON(w, viewRunListResponse{
			SchemaVersion: 1, InitialRunID: initial, Unreadable: page.unreadable,
			StoreBytes: store, TrashBytes: trash, Total: page.total, Generation: page.generation, PageIDs: page.ids, NextCursor: nextCursor, Runs: out,
		})
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
	handler.Handler = viewSecurity(mux)
	return handler
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
