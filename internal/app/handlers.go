package app

import (
	"errors"
	"net/http"
	"strings"
)

// maxFieldRunes bounds one stored text field. It keeps a single row from
// dominating the database and the JSON responses.
const maxFieldRunes = 200_000

// Search term bounds, shared by the REST list and the MCP search tool.
const (
	maxSearchQueryRunes = 500
	maxSearchTagRunes   = 200
)

type promptPayload struct {
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Tags        []string `json:"tags"`
	SourceRunID *string  `json:"source_run_id"`
}

type schemePayload struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

func (c *Client) handlePromptList(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	tag := r.URL.Query().Get("tag")
	if len([]rune(query)) > maxSearchQueryRunes || len([]rune(tag)) > maxSearchTagRunes {
		fail(w, http.StatusBadRequest, "bad_request", "search terms are too long")
		return
	}
	prompts, err := c.store.Prompts(query, tag)
	if err != nil {
		c.internal(w, "list prompts", err)
		return
	}
	writeJSON(w, http.StatusOK, prompts)
}

func (c *Client) handlePromptGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	prompt, err := c.store.Prompt(id)
	if err != nil {
		notFoundOrInternal(c, w, "get prompt", err)
		return
	}
	writeJSON(w, http.StatusOK, prompt)
}

// normalizePrompt checks a prompt payload and rewrites it in normalized form
// (trimmed title, deduplicated tags). The returned message is client-safe: it is
// what the API reports as 400 bad_request and what an MCP tool reports as an
// IsError result, so the REST handler and the MCP tool accept exactly the same
// payloads.
func normalizePrompt(p *promptPayload) error {
	title, ok := trimmed(p.Title)
	if !ok {
		return errors.New("title must not be empty")
	}
	if strings.TrimSpace(p.Body) == "" {
		return errors.New("body must not be empty")
	}
	if len([]rune(title)) > maxFieldRunes || len([]rune(p.Body)) > maxFieldRunes {
		return errors.New("title or body is too long")
	}
	tags := make([]string, 0, len(p.Tags))
	seen := map[string]bool{}
	for _, raw := range p.Tags {
		tag, ok := trimmed(raw)
		if !ok || seen[tag] {
			continue
		}
		if len([]rune(tag)) > 64 {
			return errors.New("tag is too long")
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	if len(tags) > 32 {
		return errors.New("too many tags")
	}
	p.Title = title
	p.Tags = tags
	if p.SourceRunID != nil {
		id, ok := trimmed(*p.SourceRunID)
		if !ok {
			p.SourceRunID = nil
		} else {
			p.SourceRunID = &id
		}
	}
	return nil
}

// validatePrompt writes the 400 response itself and reports whether the payload
// was accepted.
func (c *Client) validatePrompt(w http.ResponseWriter, p *promptPayload) bool {
	if err := normalizePrompt(p); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return false
	}
	return true
}

func (c *Client) handlePromptCreate(w http.ResponseWriter, r *http.Request) {
	var payload promptPayload
	if !c.decodeJSON(w, r, &payload) || !c.validatePrompt(w, &payload) {
		return
	}
	prompt, err := c.store.CreatePrompt(Prompt{
		Title:       payload.Title,
		Body:        payload.Body,
		Tags:        payload.Tags,
		SourceRunID: payload.SourceRunID,
	})
	if errors.Is(err, ErrSourceRun) {
		fail(w, http.StatusConflict, "bad_source_run", "source run does not exist or did not succeed")
		return
	}
	if err != nil {
		c.internal(w, "create prompt", err)
		return
	}
	writeJSON(w, http.StatusCreated, prompt)
}

func (c *Client) handlePromptUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var payload promptPayload
	if !c.decodeJSON(w, r, &payload) || !c.validatePrompt(w, &payload) {
		return
	}
	// Update never touches source_run_id: provenance stays as recorded.
	prompt, err := c.store.UpdatePrompt(Prompt{ID: id, Title: payload.Title, Body: payload.Body, Tags: payload.Tags})
	if err != nil {
		notFoundOrInternal(c, w, "update prompt", err)
		return
	}
	writeJSON(w, http.StatusOK, prompt)
}

func (c *Client) handlePromptDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := c.store.DeletePrompt(id); err != nil {
		notFoundOrInternal(c, w, "delete prompt", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *Client) handleSchemeList(w http.ResponseWriter, r *http.Request) {
	schemes, err := c.store.Schemes()
	if err != nil {
		c.internal(w, "list schemes", err)
		return
	}
	writeJSON(w, http.StatusOK, schemes)
}

func (c *Client) handleSchemeGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	scheme, err := c.store.Scheme(id)
	if err != nil {
		notFoundOrInternal(c, w, "get scheme", err)
		return
	}
	writeJSON(w, http.StatusOK, scheme)
}

func (c *Client) validateScheme(w http.ResponseWriter, p *schemePayload) bool {
	name, ok := trimmed(p.Name)
	if !ok {
		fail(w, http.StatusBadRequest, "bad_request", "name must not be empty")
		return false
	}
	if strings.TrimSpace(p.Body) == "" {
		fail(w, http.StatusBadRequest, "bad_request", "body must not be empty")
		return false
	}
	if len([]rune(name)) > 200 || len([]rune(p.Body)) > maxFieldRunes {
		fail(w, http.StatusBadRequest, "bad_request", "name or body is too long")
		return false
	}
	p.Name = name
	return true
}

func (c *Client) handleSchemeCreate(w http.ResponseWriter, r *http.Request) {
	var payload schemePayload
	if !c.decodeJSON(w, r, &payload) || !c.validateScheme(w, &payload) {
		return
	}
	scheme, err := c.store.CreateScheme(Scheme{Name: payload.Name, Body: payload.Body})
	if err != nil {
		c.internal(w, "create scheme", err)
		return
	}
	writeJSON(w, http.StatusCreated, scheme)
}

func (c *Client) handleSchemeUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var payload schemePayload
	if !c.decodeJSON(w, r, &payload) || !c.validateScheme(w, &payload) {
		return
	}
	scheme, err := c.store.UpdateScheme(Scheme{ID: id, Name: payload.Name, Body: payload.Body})
	if err != nil {
		notFoundOrInternal(c, w, "update scheme", err)
		return
	}
	writeJSON(w, http.StatusOK, scheme)
}

func (c *Client) handleSchemeDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := c.store.DeleteScheme(id); err != nil {
		notFoundOrInternal(c, w, "delete scheme", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
