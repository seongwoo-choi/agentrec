package cli

import (
	"context"
	"embed"
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
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/seongwoo-choi/agentrec/internal/report"
)

const (
	viewUsage         = "usage: agentrec view [<run-id>|latest] [--listen <loopback-address>] [--no-open]\n"
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

type viewRunResponse struct {
	SchemaVersion  int                `json:"schemaVersion"`
	SnapshotID     string             `json:"snapshotId"`
	ActionCount    int                `json:"actionCount"`
	EventCount     int                `json:"eventCount"`
	Run            viewRunInfo        `json:"run"`
	ProviderEvents viewProviderEvents `json:"providerEvents"`
	Evidence       viewEvidence       `json:"evidence"`
}

type viewRunListResponse struct {
	SchemaVersion int              `json:"schemaVersion"`
	InitialRunID  string           `json:"initialRunId"`
	Unreadable    int              `json:"unreadable"`
	Runs          []viewRunSummary `json:"runs"`
}

func runView(args []string, stdout, stderr io.Writer) int {
	runID, listen, open, ok := parseViewArgs(args)
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
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	runRoot, err := openRunRoot(root, runID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := runRoot.Close(); err != nil {
		fmt.Fprintf(stderr, "cli: close run %s: %v\n", runID, err)
		return 1
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

	handler := newViewHandler(root, runID)
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

func parseViewArgs(args []string) (runID, listen string, open, ok bool) {
	runID, listen, open, ok = latestRun, defaultViewListen, true, true
	runSet, listenSet := false, false
	for len(args) > 0 {
		switch args[0] {
		case "--no-open":
			if !open {
				return "", "", false, false
			}
			open = false
			args = args[1:]
		case "--listen":
			if listenSet || len(args) < 2 || !isLoopbackListen(args[1]) {
				return "", "", false, false
			}
			listenSet, listen = true, args[1]
			args = args[2:]
		default:
			if runSet || (args[0] != latestRun && validateRunID(args[0]) != nil) {
				return "", "", false, false
			}
			runSet, runID = true, args[0]
			args = args[1:]
		}
	}
	return runID, listen, open, true
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
}

func (h *viewHandler) Close() error { return h.snapshots.Close() }

func newViewHandler(root, initialRunID string) *viewHandler {
	snapshots := newViewSnapshotStore(root)
	mux := http.NewServeMux()
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
			out = append(out, viewRunSummary{
				ID: run.ID, Provider: run.Provider, Project: run.Project,
				StartedAt: run.StartedAt, Exit: run.Exit, Verification: run.Verification,
			})
		}
		writeViewJSON(w, viewRunListResponse{1, initialRunID, unreadable, out})
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
			out, err = snapshots.create(runID)
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
	return &viewHandler{Handler: viewSecurity(mux), snapshots: snapshots}
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
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}
