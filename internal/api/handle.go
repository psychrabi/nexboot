package api

// handle.go defines adapters for JSON endpoints. Register simple routes using an adapter matching the service method signature.
// Decode, error mapping and encoding are centralized; all failures pass through writeError for consistent status codes.
//
//	handle(svc.List)                          func(ctx) (Res, error)
//	handleIn(svc.Create, status(201))         func(ctx, Req) (Res, error)
//	handleID(svc.Get, notFound("… not found"))     func(ctx, id) (Res, error)
//	handleIDIn(svc.Update)                    func(ctx, id, Req) (Res, error)
//	handleErrID(svc.Delete)                   func(ctx, id) error        → 204
//	handleErrIDIn(svc.ChangePassword)         func(ctx, id, Req) error   → 204
//	handleReq(fn)                             func(*http.Request) (Res, error)
//
// id always comes from the {id} path parameter. Routes requiring query or multiple path values use handleReq.

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/tianwei/diskless/internal/control/errs"
)

// responder configures each route's response behavior.
type responder struct {
	status   int
	notFound string
}

type respondOpt func(*responder)

// status sets the success code (default 200; 204 has no response body).
func status(code int) respondOpt {
	return func(rp *responder) { rp.status = code }
}

// notFound overrides storage-layer not-found messages intended for developers, such as `groups "x": not found`.
// Routes can use this override to provide meaningful user-facing errors.
func notFound(msg string) respondOpt {
	return func(rp *responder) { rp.notFound = msg }
}

func newResponder(defaultStatus int, opts []respondOpt) responder {
	rp := responder{status: defaultStatus}
	for _, opt := range opts {
		opt(&rp)
	}
	return rp
}

// funnel is the shared error path: apply notFound overrides, then call writeError. Returns whether a response was written.
func funnel(w http.ResponseWriter, rp responder, err error) bool {
	if err != nil && rp.notFound != "" && errs.IsNotFound(err) {
		http.Error(w, rp.notFound, http.StatusNotFound)
		return true
	}
	return writeError(w, err)
}

// finish routes failures through funnel, otherwise encodes the result.
func finish[Res any](w http.ResponseWriter, rp responder, res Res, err error) {
	if funnel(w, rp, err) {
		return
	}
	writeJSON(w, rp.status, res)
}

// finishEmpty handles methods returning only error: 204 has no body; other successes return {"status":"ok"}.
func finishEmpty(w http.ResponseWriter, rp responder, err error) {
	if funnel(w, rp, err) {
		return
	}
	if rp.status == http.StatusNoContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, rp.status, map[string]string{"status": "ok"})
}

// decodeBody parses JSON request bodies and returns a standard 400 on decoding errors.
func decodeBody[Req any](w http.ResponseWriter, r *http.Request) (Req, bool) {
	var req Req
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request format", http.StatusBadRequest)
		return req, false
	}
	return req, true
}

func handle[Res any](fn func(context.Context) (Res, error), opts ...respondOpt) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := fn(r.Context())
		finish(w, newResponder(http.StatusOK, opts), res, err)
	}
}

func handleIn[Req, Res any](fn func(context.Context, Req) (Res, error), opts ...respondOpt) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := decodeBody[Req](w, r)
		if !ok {
			return
		}
		res, err := fn(r.Context(), req)
		finish(w, newResponder(http.StatusOK, opts), res, err)
	}
}

func handleID[Res any](fn func(context.Context, string) (Res, error), opts ...respondOpt) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := fn(r.Context(), chi.URLParam(r, "id"))
		finish(w, newResponder(http.StatusOK, opts), res, err)
	}
}

func handleIDIn[Req, Res any](fn func(context.Context, string, Req) (Res, error), opts ...respondOpt) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := decodeBody[Req](w, r)
		if !ok {
			return
		}
		res, err := fn(r.Context(), chi.URLParam(r, "id"), req)
		finish(w, newResponder(http.StatusOK, opts), res, err)
	}
}

func handleErrID(fn func(context.Context, string) error, opts ...respondOpt) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := fn(r.Context(), chi.URLParam(r, "id"))
		finishEmpty(w, newResponder(http.StatusNoContent, opts), err)
	}
}

func handleErrIDIn[Req any](fn func(context.Context, string, Req) error, opts ...respondOpt) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := decodeBody[Req](w, r)
		if !ok {
			return
		}
		err := fn(r.Context(), chi.URLParam(r, "id"), req)
		finishEmpty(w, newResponder(http.StatusNoContent, opts), err)
	}
}

// handleReq handles routes needing extra request details and uses the same centralized error path.
func handleReq[Res any](fn func(*http.Request) (Res, error), opts ...respondOpt) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := fn(r)
		finish(w, newResponder(http.StatusOK, opts), res, err)
	}
}
