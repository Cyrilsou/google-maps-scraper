package web

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Service struct {
	repo       JobRepository
	dataFolder string
}

func NewService(repo JobRepository, dataFolder string) *Service {
	return &Service{
		repo:       repo,
		dataFolder: dataFolder,
	}
}

func (s *Service) Create(ctx context.Context, job *Job) error {
	return s.repo.Create(ctx, job)
}

func (s *Service) All(ctx context.Context) ([]Job, error) {
	return s.repo.Select(ctx, SelectParams{})
}

func (s *Service) Get(ctx context.Context, id string) (Job, error) {
	return s.repo.Get(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return fmt.Errorf("invalid file name")
	}

	// Remove every possible artefact file (CSV, XLSX or JSONL). Missing
	// files are fine.
	for _, ext := range []string{".csv", ".xlsx", ".jsonl"} {
		datapath := filepath.Join(s.dataFolder, id+ext)
		if _, err := os.Stat(datapath); err == nil {
			if err := os.Remove(datapath); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}

	return s.repo.Delete(ctx, id)
}

func (s *Service) Update(ctx context.Context, job *Job) error {
	return s.repo.Update(ctx, job)
}

func (s *Service) SelectPending(ctx context.Context) ([]Job, error) {
	return s.repo.Select(ctx, SelectParams{Status: StatusPending, Limit: 1})
}

func (s *Service) GetCSV(_ context.Context, id string) (string, error) {
	return s.ResultFile(id, FormatCSV)
}

// ResultFile returns the path to the result artefact for a job, picking the
// CSV or XLSX variant based on format. If format is empty, both are checked
// (XLSX first) so callers can stay format-agnostic.
func (s *Service) ResultFile(id, format string) (string, error) {
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid file name")
	}

	try := []string{format}
	if format == "" {
		// Preference order when auto-detecting: richest export first.
		try = []string{FormatXLSX, FormatJSONL, FormatCSV}
	}

	for _, f := range try {
		if f == "" {
			continue
		}

		path := filepath.Join(s.dataFolder, id+"."+f)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("result file not found for job %s", id)
}
