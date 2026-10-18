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

func (c *Client) handleOptimize(w http.ResponseWriter, r *http.Request) {
	var req optimizeRequest
	if !c.decodeJSON(w, r, &req) {
		return
	}
	requestID := req.RequestID
	// A UUID satisfies this. Restricting the charset keeps the ID usable as a URL
	// path segment on the way back in through GET /api/optimizations/{requestId}.
	if !validRequestID(requestID) {
		fail(w, http.StatusBadRequest, "bad_request", "request_id must be 1-128 characters of [A-Za-z0-9._-]")
		return
	}
	if strings.TrimSpace(req.Input) == "" {
		fail(w, http.StatusBadRequest, "bad_request", "input must not be empty")
		return
	}
	if len([]rune(req.Input)) > maxFieldRunes {
		fail(w, http.StatusBadRequest, "bad_request", "input is too long")
		return
	}
	if req.SchemeID <= 0 {
		fail(w, http.StatusBadRequest, "bad_request", "scheme_id must be a positive integer")
		return
	}

	// A repeated request ID is decided by the stored record alone: same submitted
	// input and same submitted scheme_id replays the stored status and result,
	// anything else is a conflict. The current scheme row is never consulted, so
	// editing or deleting the scheme leaves replay intact. This runs before the
	// model check so an already-recorded run stays readable and replayable even
	// while the endpoint is unconfigured.
	if existing, err := c.store.Run(requestID); err == nil {
		c.replayRun(w, existing, req)
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		c.internal(w, "load existing run", err)
		return
	}

	// A new run needs an upstream. Refuse without inserting a running row: an
	// unconfigured endpoint is a server-side condition, not a failed attempt.
	if !c.cfg.modelConfigured() {
		fail(w, http.StatusServiceUnavailable, "model_not_configured", "model endpoint is not configured")
		return
	}

	scheme, err := c.store.Scheme(req.SchemeID)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, http.StatusBadRequest, "bad_scheme", "scheme does not exist")
		return
	}
	if err != nil {
		c.internal(w, "load scheme for optimization", err)
		return
	}

	// Render the upstream body before claiming the ID so a misconfigured model
	// endpoint costs no run record.
	body, params := c.buildRequest(req.Input, scheme.Body)

	run := Run{
		RequestID:      requestID,
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
		c.internal(w, "claim run", err)
		return
	}
	if !claimed {
		existing, err := c.store.Run(requestID)
		if err != nil {
			c.internal(w, "load claimed run", err)
			return
		}
		c.replayRun(w, existing, req)
		return
	}

	// The timeout belongs to this one upstream call; a client disconnect
	// cancels it through r.Context().
	ctx, cancel := context.WithTimeout(r.Context(), c.cfg.RequestTimeout)
	defer cancel()
	result, kind := c.optimize(ctx, body)

	status := RunSucceeded
	if kind != "" {
		status = RunFailed
	}
	// The run must be settled before it is reported. If this write fails the row
	// is still "running", and returning 200 with that row would claim a success or
	// a classification the database does not hold. Report the storage failure and
	// keep the row for a later GET instead. The model is not called again.
	if err := c.store.FinishRun(requestID, status, result, kind); err != nil {
		c.internal(w, "finish run", err)
		return
	}

	stored, err := c.store.Run(requestID)
	if err != nil {
		c.internal(w, "reload run", err)
		return
	}
	writeJSON(w, http.StatusOK, stored)
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

// replayRun answers a request whose ID is already recorded. A settled run
// replays its stored result; a run still executing is reported as such rather
// than waited on, so a duplicate never starts a second upstream call and never
// blocks.
func (c *Client) replayRun(w http.ResponseWriter, existing Run, req optimizeRequest) {
	if existing.Input != req.Input || existing.SchemeID != req.SchemeID {
		fail(w, http.StatusConflict, "request_id_conflict", "request_id was already used with a different input or scheme")
		return
	}
	status := http.StatusOK
	if existing.Status == RunRunning {
		status = http.StatusAccepted
	}
	writeJSON(w, status, existing)
}

func (c *Client) handleRunGet(w http.ResponseWriter, r *http.Request) {
	run, err := c.store.Run(r.PathValue("requestId"))
	if err != nil {
		notFoundOrInternal(c, w, "get run", err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}
