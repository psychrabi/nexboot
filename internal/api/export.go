package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// exportTicketTTL gives the browser enough time to open a download URL but limits replay exposure if URLs leak through proxy logs or history.
// Large downloads may take hours; tickets are validated once at the initial request.
const exportTicketTTL = 60 * time.Second

// exportTickets issues short-lived one-time tokens through authenticated POST for ordinary anchor-based downloads.
// Anchor requests cannot set Authorization headers; putting a login token in the URL would expose it. Tickets remain in memory
// because they are short-lived credentials between two requests on the active node.
type exportTickets struct {
	mu      sync.Mutex
	now     func() time.Time
	tickets map[string]exportTicket
}

type exportTicket struct {
	imageID string
	// A nonempty reductionID selects a restore-point download.
	reductionID string
	compress    bool
	expires     time.Time
}

func newExportTickets(now func() time.Time) *exportTickets {
	if now == nil {
		now = time.Now
	}
	return &exportTickets{now: now, tickets: map[string]exportTicket{}}
}

func (t *exportTickets) Issue(imageID string, compress bool) (string, error) {
	return t.issue(exportTicket{imageID: imageID, compress: compress})
}

// IssueReduction issues a download ticket for a restore point.
func (t *exportTickets) IssueReduction(reductionID string, compress bool) (string, error) {
	return t.issue(exportTicket{reductionID: reductionID, compress: compress})
}

func (t *exportTickets) issue(ticket exportTicket) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	// Lazily remove expired tickets when issuing a new one.
	for k, v := range t.tickets {
		if now.After(v.expires) {
			delete(t.tickets, k)
		}
	}
	ticket.expires = now.Add(exportTicketTTL)
	t.tickets[token] = ticket
	return token, nil
}

// Consume redeems a token once and returns the issued image and compression preference.
// A plain download GET has no request body, so the ticket carries this preference.
func (t *exportTickets) Consume(token string) (exportTicket, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ticket, ok := t.tickets[token]
	if !ok {
		return exportTicket{}, false
	}
	delete(t.tickets, token)
	if t.now().After(ticket.expires) {
		return exportTicket{}, false
	}
	return ticket, true
}

func exportTicketHandler(service ImageImportService, tickets *exportTickets) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		compress := r.URL.Query().Get("compress") == "gzip"
		// Perform the same eligibility checks before issuing the URL so the UI can report imports still in progress.
		name, err := service.ExportDownloadName(r.Context(), id, compress)
		if writeError(w, err) {
			return
		}
		token, err := tickets.Issue(id, compress)
		if writeError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"token":      token,
			"url":        "/api/images/export-download?token=" + token,
			"expires_in": int(exportTicketTTL / time.Second),
			"file_name":  name,
		})
	}
}

// reductionExportTicketHandler is the restore-point counterpart of exportTicketHandler.
func reductionExportTicketHandler(service ImageImportService, tickets *exportTickets) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		compress := r.URL.Query().Get("compress") == "gzip"
		name, err := service.ReductionExportName(r.Context(), id, compress)
		if writeError(w, err) {
			return
		}
		token, err := tickets.IssueReduction(id, compress)
		if writeError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"token":      token,
			"url":        "/api/images/export-download?token=" + token,
			"expires_in": int(exportTicketTTL / time.Second),
			"file_name":  name,
		})
	}
}

func exportDownloadHandler(service ImageImportService, tickets *exportTickets, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ticket, ok := tickets.Consume(r.URL.Query().Get("token"))
		if !ok {
			http.Error(w, "Download link is invalid or expired. Return to Image Management to start a new download.", http.StatusUnauthorized)
			return
		}
		name, err := "", error(nil)
		if ticket.reductionID != "" {
			name, err = service.ReductionExportName(r.Context(), ticket.reductionID, ticket.compress)
		} else {
			name, err = service.ExportDownloadName(r.Context(), ticket.imageID, ticket.compress)
		}
		if writeError(w, err) {
			return
		}
		// Mark the payload as an archive. Do not use Content-Encoding: browsers may transparently decompress and save a non-gzip .gz file.
		contentType := "application/octet-stream"
		if ticket.compress {
			contentType = "application/gzip"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", contentDisposition(name))
		w.WriteHeader(http.StatusOK)
		// The status code has already been sent; on failure, abort the stream so zfs receive detects truncation.
		stream := func() error { return service.StreamImage(r.Context(), ticket.imageID, w, ticket.compress) }
		if ticket.reductionID != "" {
			stream = func() error { return service.StreamReduction(r.Context(), ticket.reductionID, w, ticket.compress) }
		}
		if err := stream(); err != nil {
			if logger != nil {
				logger.Warn("image download aborted", "image", ticket.imageID, "reduction", ticket.reductionID, "error", fmt.Sprint(err))
			}
			// Returning normally ends chunked transfer and may look successful. ErrAbortHandler closes the connection instead.
			panic(http.ErrAbortHandler)
		}
	}
}
