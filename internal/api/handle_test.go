package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/tianwei/diskless/internal/control/errs"
)

// call executes handlers via chi so {id} parsing matches production.
func call(t *testing.T, method, path string, body string, register func(r chi.Router)) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	register(r)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestHandleFunnelsClassifiedErrors(t *testing.T) {
	// Centralized mapping must preserve classified errors rather than convert them to HTTP 500.
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"invalid", errs.ErrInvalid, http.StatusBadRequest},
		{"conflict", errs.ErrConflict, http.StatusConflict},
		{"notfound", errs.ErrNotFound, http.StatusNotFound},
		{"bare", errors.New("boom"), http.StatusInternalServerError},
	} {
		w := call(t, http.MethodGet, "/x", "", func(r chi.Router) {
			r.Get("/x", handle(func(context.Context) (struct{}, error) {
				return struct{}{}, tc.err
			}))
		})
		if w.Code != tc.want {
			t.Fatalf("%s: status = %d, want %d", tc.name, w.Code, tc.want)
		}
	}
}

func TestHandleInRejectsBadJSON(t *testing.T) {
	w := call(t, http.MethodPost, "/x", "{not json", func(r chi.Router) {
		r.Post("/x", handleIn(func(_ context.Context, req struct{ Name string }) (string, error) {
			t.Fatal("service must not run on bad JSON")
			return "", nil
		}))
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Invalid request format") {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestHandleIDPassesPathParamAndStatus(t *testing.T) {
	w := call(t, http.MethodPost, "/x/abc", "", func(r chi.Router) {
		r.Post("/x/{id}", handleID(func(_ context.Context, id string) (map[string]string, error) {
			return map[string]string{"id": id}, nil
		}, status(http.StatusAccepted)))
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"id":"abc"`) {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestNotFoundOptionKeepsChineseMessage(t *testing.T) {
	// Storage errors such as `groups "x": not found` are developer-facing; route overrides produce user-facing messages.
	w := call(t, http.MethodGet, "/x/nope", "", func(r chi.Router) {
		r.Get("/x/{id}", handleID(func(context.Context, string) (struct{}, error) {
			return struct{}{}, errors.New(`groups "nope": not found`)
		}, notFound("Group not found")))
	})
	// Unclassified errors still map to HTTP 500.
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("bare error: status = %d", w.Code)
	}

	w = call(t, http.MethodGet, "/x/nope", "", func(r chi.Router) {
		r.Get("/x/{id}", handleID(func(context.Context, string) (struct{}, error) {
			return struct{}{}, errs.ErrNotFound
		}, notFound("Group not found")))
	})
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "Group not found") {
		t.Fatalf("status = %d body = %q", w.Code, w.Body.String())
	}
}

func TestHandleErrIDStatusShapes(t *testing.T) {
	// 204 omits a body; other successful handlers return {"status":"ok"}.
	w := call(t, http.MethodDelete, "/x/1", "", func(r chi.Router) {
		r.Delete("/x/{id}", handleErrID(func(context.Context, string) error { return nil }))
	})
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("default: status = %d body = %q", w.Code, w.Body.String())
	}

	w = call(t, http.MethodPost, "/x/1", "", func(r chi.Router) {
		r.Post("/x/{id}", handleErrID(func(context.Context, string) error { return nil }, status(http.StatusOK)))
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Fatalf("ack: status = %d body = %q", w.Code, w.Body.String())
	}
}

func TestHandleErrIDInDecodesAndFunnels(t *testing.T) {
	var got string
	w := call(t, http.MethodPost, "/x/u1", `{"password":"s3cret"}`, func(r chi.Router) {
		r.Post("/x/{id}", handleErrIDIn(func(_ context.Context, id string, req struct {
			Password string `json:"password"`
		}) error {
			got = id + ":" + req.Password
			return nil
		}))
	})
	if w.Code != http.StatusNoContent || got != "u1:s3cret" {
		t.Fatalf("status = %d got = %q", w.Code, got)
	}
}

func TestHandleReqStillFunnels(t *testing.T) {
	// handleReq must use the same centralized error mapper.
	w := call(t, http.MethodGet, "/x", "", func(r chi.Router) {
		r.Get("/x", handleReq(func(*http.Request) (struct{}, error) {
			return struct{}{}, errs.ErrConflict
		}))
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d", w.Code)
	}
}
