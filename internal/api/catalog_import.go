package api

import (
	"context"
	"errors"
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
	// Job, returning it in its initial pending state.
	Submit(ctx context.Context, filename string, payload []byte, dryRun bool) (*crawler.ImportJob, error)
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
// and starts an asynchronous Import Job (202 + the pending job DTO). A malformed
// upload (not multipart, no file field, or over the size cap) is a 400.
func (h *Handler) importCatalog(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
	if err := r.ParseMultipartForm(maxImportBytes); err != nil {
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
	job, err := h.cfg.Importer.Submit(r.Context(), header.Filename, payload, dryRun)
	if err != nil {
		slog.Error("api: error submitting catalog import", "err", err)
		writeError(w, http.StatusInternalServerError, "could not start import")
		return
	}

	writeJSON(w, http.StatusAccepted, toImportJobDTO(job))
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
