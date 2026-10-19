package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

type optimizeRequest struct {
	RequestID string `json:"request_id"`
	Input     string `json:"input"`
	// SchemeID names the scheme to use. The run snapshots that scheme's name and
	// body at execution time, but the run identifies it by this ID, so a later
	// edit or deletion of the scheme does not change what a replay returns.
	SchemeID int64 `json:"scheme_id"`
}

// apiFailure is a client-visible failure: an HTTP status plus the same code and
// message the JSON error envelope carries. MCP tools report the code and message
// as an IsError result instead of a status; both surfaces share one decision
// about what went wrong.
type apiFailure struct {
	status  int
	code    string
	message string
}

func badRequest(message string) *apiFailure {
	return &apiFailure{http.StatusBadRequest, "bad_request", message}
}

// internalFailure logs the underlying error and reports a generic failure. The
// detail stays in the log: it may quote request text, a file path or an upstream
// body.
func (c *Client) internalFailure(context string, err error) *apiFailure {
	c.log.Printf("error: %s: %v", context, err)
	return &apiFailure{http.StatusInternalServerError, "internal", "internal error"}
}

func (c *Client) handleOptimize(w http.ResponseWriter, r *http.Request) {
	var req optimizeRequest
	if !c.decodeJSON(w, r, &req) {
		return
	}
	run, failure := c.executeOptimization(r.Context(), req)
	if failure != nil {
		fail(w, failure.status, failure.code, failure.message)
		return
	}
	// A run still marked running belongs to another request that won the claim;
	// reporting 200 would claim a result this response does not carry.
	status := http.StatusOK
	if run.Status == RunRunning {
		status = http.StatusAccepted
	}
	writeJSON(w, status, run)
}

// executeOptimization performs one optimization request: validation, replay of a
// recorded request ID, the single upstream call and settling the run. It writes
// no HTTP response, so the REST handler and the MCP tool call it directly rather
// than forwarding to each other. ctx carries the client's lifetime; the upstream
// deadline is applied here.
func (c *Client) executeOptimization(ctx context.Context, req optimizeRequest) (Run, *apiFailure) {
	// A UUID satisfies this. Restricting the charset keeps the ID usable as a URL
	// path segment on the way back in through GET /api/optimizations/{requestId}.
	if !validRequestID(req.RequestID) {
		return Run{}, badRequest("request_id must be 1-128 characters of [A-Za-z0-9._-]")
	}
	if strings.TrimSpace(req.Input) == "" {
		return Run{}, badRequest("input must not be empty")
	}
	if len([]rune(req.Input)) > maxFieldRunes {
		return Run{}, badRequest("input is too long")
	}
	if req.SchemeID <= 0 {
		return Run{}, badRequest("scheme_id must be a positive integer")
	}

	// A repeated request ID is decided by the stored record alone: same submitted
	// input and same submitted scheme_id replays the stored status and result,
	// anything else is a conflict. The current scheme row is never consulted, so
	// editing or deleting the scheme leaves replay intact. This runs before the
	// model check so an already-recorded run stays readable and replayable even
	// while the endpoint is unconfigured.
	if existing, err := c.store.Run(req.RequestID); err == nil {
		if conflict := replayConflict(existing, req); conflict != nil {
			return Run{}, conflict
		}
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Run{}, c.internalFailure("load existing run", err)
	}

	// A new run needs an upstream. Refuse without inserting a running row: an
	// unconfigured endpoint is a server-side condition, not a failed attempt.
	if !c.cfg.modelConfigured() {
		return Run{}, &apiFailure{http.StatusServiceUnavailable, "model_not_configured", "model endpoint is not configured"}
	}

	scheme, err := c.store.Scheme(req.SchemeID)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, &apiFailure{http.StatusBadRequest, "bad_scheme", "scheme does not exist"}
	}
	if err != nil {
		return Run{}, c.internalFailure("load scheme for optimization", err)
	}

	// Render the upstream body before claiming the ID so a misconfigured model
	// endpoint costs no run record.
	body, params := c.buildRequest(req.Input, scheme.Body)

	run := Run{
		RequestID:      req.RequestID,
		Input:          req.Input,
		SchemeID:       scheme.ID,
		SchemeName:     scheme.Name,
		SchemeSnapshot: scheme.Body,
		Model:          c.cfg.Model,
		Params:         params,
	}

	// Claiming the ID and calling upstream are deliberately separate: no
	// transaction may span the model call, and only the claim winner is allowed
	// to spend an upstream request. A concurrent duplicate loses here and
	// compares its raw payload against the recorded one.
	claimed, err := c.store.ClaimRun(run)
	if err != nil {
		return Run{}, c.internalFailure("claim run", err)
	}
	if !claimed {
		existing, err := c.store.Run(req.RequestID)
		if err != nil {
			return Run{}, c.internalFailure("load claimed run", err)
		}
		if conflict := replayConflict(existing, req); conflict != nil {
			return Run{}, conflict
		}
		return existing, nil
	}

	// The timeout belongs to this one upstream call; a client disconnect
	// cancels it through ctx.
	callCtx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	defer cancel()
	result, kind := c.optimize(callCtx, body)

	status := RunSucceeded
	if kind != "" {
		status = RunFailed
	}
	// The run must be settled before it is reported. If this write fails the row
	// is still "running", and reporting success would claim a classification the
	// database does not hold. Report the storage failure and keep the row for a
	// later GET instead. The model is not called again.
	if err := c.store.FinishRun(req.RequestID, status, result, kind); err != nil {
		return Run{}, c.internalFailure("finish run", err)
	}

	stored, err := c.store.Run(req.RequestID)
	if err != nil {
		return Run{}, c.internalFailure("reload run", err)
	}
	return stored, nil
}

// validRequestID accepts an opaque client-generated token of URL-safe characters.
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// replayConflict decides whether an existing run for the same ID describes the
// same submission. A settled run replays its stored result, a run still executing
// is reported as such by the caller, and a different payload is a conflict: one
// ID cannot stand for two runs.
func replayConflict(existing Run, req optimizeRequest) *apiFailure {
	if existing.Input != req.Input || existing.SchemeID != req.SchemeID {
		return &apiFailure{http.StatusConflict, "request_id_conflict", "request_id was already used with a different input or scheme"}
	}
	return nil
}

func (c *Client) handleRunGet(w http.ResponseWriter, r *http.Request) {
	run, err := c.store.Run(r.PathValue("requestId"))
	if err != nil {
		notFoundOrInternal(c, w, "get run", err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}
