package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

type JobListingRepository struct {
	db *sql.DB
}

var _ crawler.JobListingRepository = &JobListingRepository{}

func (jr *JobListingRepository) Save(ctx context.Context, jobListing *crawler.JobListing) error {
	techJSON, err := json.Marshal(jobListing.TechStack)
	if err != nil {
		return fmt.Errorf("error marschalling tech stack: %w", err)
	}

	_, err = jr.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO job_listing (url, title, description, company, location, remote, tech_stack)
		VALUES (?, ?, ?, ?, ?, ?, ?);
		`, jobListing.URL, jobListing.Title, jobListing.Description, jobListing.Company, jobListing.Location, jobListing.Remote, techJSON)
	if err != nil {
		return fmt.Errorf("error saving job listing %v: %w", jobListing, err)
	}

	return nil
}

func (jr *JobListingRepository) Find(ctx context.Context) ([]*crawler.JobListing, error) {
	var jobListings []*crawler.JobListing

	rows, err := jr.db.QueryContext(ctx, `
		SELECT url, title, description, company, location, remote, tech_stack FROM job_listing;
		`)
	if err != nil {
		return nil, fmt.Errorf("error querying job listings: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		jobListing := &crawler.JobListing{}
		var techJSON string
		err := rows.Scan(
			&jobListing.URL,
			&jobListing.Title,
			&jobListing.Description,
			&jobListing.Company,
			&jobListing.Location,
			&jobListing.Remote,
			&techJSON)
		if err != nil {
			slog.Error("error scanning job listing", "err", err)
			continue
		}

		if err := json.Unmarshal([]byte(techJSON), &jobListing.TechStack); err != nil {
			slog.Error("error unmarshalling tech stack", "err", err)
			continue
		}
		jobListings = append(jobListings, jobListing)
	}

	return jobListings, nil
}

func NewJobListingRepository(db *sql.DB) *JobListingRepository {
	return &JobListingRepository{
		db: db,
	}
}
