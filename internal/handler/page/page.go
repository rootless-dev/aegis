// Package page serves the HTML surface: the pages a browser reaches directly,
// as opposed to the JSON endpoints a client calls.
package page

import (
	"errors"
	"net/http"

	"github.com/phuslu/log"
	"github.com/rootless-dev/aegis/internal/http/render"
)

const (
	landingTemplate = "landing"
	errorTemplate   = "error"

	notFoundMessage = "That page does not exist."
	// Says nothing about the failure: a panic message or a wrapped database
	// error on a public page is an information leak, the same distinction
	// health draws with RevealErrors.
	serverErrorMessage      = "Something went wrong. Please try again."
	methodNotAllowedMessage = "That method is not allowed here."
)

type Handler struct {
	renderer *render.Renderer
	logger   *log.Logger
}

func New(renderer *render.Renderer, logger *log.Logger) *Handler {
	return &Handler{renderer: renderer, logger: logger}
}

type landingModel struct {
	Title   string
	Tagline string
}

type errorModel struct {
	Title   string
	Status  int
	Message string
}

func (h *Handler) Landing(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, landingTemplate, landingModel{
		Title:   "Aegis",
		Tagline: "Identity for every realm.",
	})
}

func (h *Handler) NotFound(w http.ResponseWriter, r *http.Request) {
	h.renderError(w, r, http.StatusNotFound, notFoundMessage)
}

func (h *Handler) MethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	h.renderError(w, r, http.StatusMethodNotAllowed, methodNotAllowedMessage)
}

// ServerError satisfies middleware.ErrorWriter, which is how the page surface
// answers a panic in HTML while the API surface answers JSON.
func (h *Handler) ServerError(w http.ResponseWriter, r *http.Request) {
	h.renderError(w, r, http.StatusInternalServerError, serverErrorMessage)
}

func (h *Handler) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	h.render(w, r, status, errorTemplate, errorModel{
		Title:   http.StatusText(status),
		Status:  status,
		Message: message,
	})
}

// render carries the floor: the handler discovering a failed render may already
// be answering an error, so the failure path writes a fixed document instead of
// recursing through the renderer.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	err := h.renderer.Page(w, status, name, data)
	if err == nil {
		return
	}

	h.logger.Error().
		Err(err).
		Str("template", name).
		Str("path", r.URL.Path).
		Msg("rendering a page failed")

	// The status and part of the document are already on the wire: answering
	// again would set headers on a committed response and append a second
	// document under the first.
	if errors.Is(err, render.ErrResponseWritten) {
		return
	}

	// A page that failed to render is not a success: answering 200 with the
	// failure document would read as a healthy page to a monitor or a crawler.
	if status < http.StatusBadRequest {
		status = http.StatusInternalServerError
	}

	render.Fallback(w, status)
}
