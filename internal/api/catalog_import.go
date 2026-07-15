package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// Importer starts asynchronous Catalog Imports. It is injected so the api package
// stays decoupled from the importer implementation, mirroring how Runner is
// injected. In the server it is *importer.Importer.
type Importer interface {
	// Submit buffers an uploaded catalog file and starts an asynchronous Import
	// Job. idempotencyKey (empty when no header was sent) makes submission
	// retriable: replay=true signals the returned job is a replay of a prior
	// submission (answer 200, not 202). A key reused with a different request
	// yields crawler.ErrIdempotencyKeyConflict.
	Submit(ctx context.Context, filename string, payload []byte, dryRun bool, idempotencyKey string) (job *crawler.ImportJob, replay bool, err error)
}

// maxImportBytes caps an uploaded catalog file at ~32 MB. It bounds the whole
// request body (MaxBytesReader) and the in-memory multipart parse, so the payload
// is buffered in memory and never spilled to disk.
const maxImportBytes = 32 << 20

type importJobDTO struct {
	ID        string           `json:"id"`
	Status    string           `json:"status"`
	DryRun    bool             `json:"dryRun"`
	Filename  string           `json:"filename"`
	FileSize  int64            `json:"fileSize"`
	Result    *importResultDTO `json:"result"` // null until the job completes
	Error     string           `json:"error"`
	CreatedAt time.Time        `json:"createdAt"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

type importResultDTO struct {
	CompaniesUpserted int              `json:"companiesUpserted"`
	PagesUpserted     int              `json:"pagesUpserted"`
	Errors            []importErrorDTO `json:"errors"`
	ErrorCount        int              `json:"errorCount"`
}

type importErrorDTO struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

func toImportJobDTO(job *crawler.ImportJob) importJobDTO {
	dto := importJobDTO{
		ID:        job.ID.String(),
		Status:    string(job.Status),
		DryRun:    job.DryRun,
		Filename:  job.Filename,
		FileSize:  job.FileSize,
		Error:     job.Error,
		CreatedAt: job.CreatedAt,
		UpdatedAt: job.UpdatedAt,
	}
	if job.Result != nil {
		// Coalesce to a non-nil slice so the JSON is [] rather than null, keeping
		// the frontend's array handling uniform.
		errs := make([]importErrorDTO, 0, len(job.Result.Errors))
		for _, e := range job.Result.Errors {
			errs = append(errs, importErrorDTO{Line: e.Line, Message: e.Message})
		}
		dto.Result = &importResultDTO{
			CompaniesUpserted: job.Result.CompaniesUpserted,
			PagesUpserted:     job.Result.PagesUpserted,
			Errors:            errs,
			ErrorCount:        job.Result.ErrorCount,
		}
	}
	return dto
}

// importCatalog accepts a multipart catalog file and a ?dryRun= flag, buffers it,
// and starts an asynchronous Import Job (202 + the pending job DTO). An optional
// Idempotency-Key header makes submission retriable: a replay of the same key and
// request returns the original job with 200, while reusing a key with a different
// file or flag is a 422 (ADR-0014). A malformed upload (not multipart, no file
// field) is a 400; a body over the size cap is a 413 naming the limit.
func (h *Handler) importCatalog(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
	if err := r.ParseMultipartForm(maxImportBytes); err != nil {
		// MaxBytesReader signals the cap via *http.MaxBytesError; surface it as
		// 413 with the limit named, so an operator with a too-big export learns
		// the actual problem instead of "invalid multipart upload".
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("upload exceeds the %d MB limit", maxImportBytes>>20))
			return
		}
		writeError(w, http.StatusBadRequest, "invalid multipart upload")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	payload, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read uploaded file")
		return
	}

	dryRun := r.URL.Query().Get("dryRun") == "true"
	idempotencyKey := r.Header.Get("Idempotency-Key")
	job, replay, err := h.cfg.Importer.Submit(r.Context(), header.Filename, payload, dryRun, idempotencyKey)
	if err != nil {
		if errors.Is(err, crawler.ErrIdempotencyKeyConflict) {
			// The key was already used for a different file or dry-run flag; refusing
			// avoids aliasing a job that imported different bytes (ADR-0014).
			writeError(w, http.StatusUnprocessableEntity, "idempotency key already used with a different request")
			return
		}
		slog.Error("api: error submitting catalog import", "err", err)
		writeError(w, http.StatusInternalServerError, "could not start import")
		return
	}

	// A fresh submission is 202 Accepted (async job created); an idempotent replay
	// returns the original job with 200 OK. Identical DTO shape either way.
	status := http.StatusAccepted
	if replay {
		status = http.StatusOK
	}
	writeJSON(w, status, toImportJobDTO(job))
}

// listImportJobs returns every Import Job, newest first.
func (h *Handler) listImportJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.cfg.ImportJobs.List(r.Context())
	if err != nil {
		slog.Error("api: error listing import jobs", "err", err)
		writeError(w, http.StatusInternalServerError, "could not list import jobs")
		return
	}

	dtos := make([]importJobDTO, 0, len(jobs))
	for _, j := range jobs {
		dtos = append(dtos, toImportJobDTO(j))
	}

	writeJSON(w, http.StatusOK, dtos)
}

// getImportJob returns one Import Job for polling; unknown id is 404, invalid id
// is 400.
func (h *Handler) getImportJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid import job id")
		return
	}

	job, err := h.cfg.ImportJobs.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, crawler.ErrNotFound) {
			writeError(w, http.StatusNotFound, "import job not found")
			return
		}
		slog.Error("api: error getting import job", "err", err)
		writeError(w, http.StatusInternalServerError, "could not get import job")
		return
	}

	writeJSON(w, http.StatusOK, toImportJobDTO(job))
}
