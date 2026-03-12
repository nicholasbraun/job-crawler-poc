package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

type JobRepository struct {
	db *sql.DB
}

var _ crawler.JobRepository = &JobRepository{}

func (jr *JobRepository) Save(ctx context.Context, job *crawler.Job) error {
	techJSON, err := json.Marshal(job.TechStack)
	if err != nil {
		return fmt.Errorf("error marschalling tech stack: %w", err)
	}

	_, err = jr.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO job (url, title, company, location, tech_stack)
		VALUES (?, ?, ?, ?, ?);
		`, job.URL, job.Title, job.Company, job.Location, techJSON)
	if err != nil {
		return fmt.Errorf("error saving job %v: %w", job, err)
	}

	return nil
}

func (jr *JobRepository) Find(ctx context.Context) ([]*crawler.Job, error) {
	var jobs []*crawler.Job

	rows, err := jr.db.QueryContext(ctx, `
		SELECT url, title, company, location, tech_stack FROM job;
		`)
	if err != nil {
		return nil, fmt.Errorf("error querying jobs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		job := &crawler.Job{}
		var techJSON string
		err := rows.Scan(
			&job.URL,
			&job.Title,
			&job.Company,
			&job.Location,
			&techJSON)
		if err != nil {
			slog.Error("error scanning job", "err", err)
			continue
		}

		if err := json.Unmarshal([]byte(techJSON), &job.TechStack); err != nil {
			slog.Error("error unmarshalling tech stack", "err", err)
			continue
		}
		jobs = append(jobs, job)
	}

	return jobs, nil
}

func NewJobRepository(db *sql.DB) *JobRepository {
	return &JobRepository{
		db: db,
	}
}
