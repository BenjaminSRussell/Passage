package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/batch/internal/domain"
)

const (
	HandlerProductExplode     = "product.explode"
	HandlerProductGroupChange = "product.group-change"
	MaxPageSize               = 500

	statusChanged = "changed"
	statusSkipped = "skipped"
	moreCursor    = "more"
)

var ErrRejected = errors.New("product service rejected the request")

type ExplodePage struct {
	Created   int
	Remaining int
}

type ReleasedPage struct {
	IDs  []string
	Next string
}

type ItemResult struct {
	ID     string
	Status string
	Code   string
}

type ProductClient interface {
	ExplodePage(ctx context.Context, masterID string, selected map[string][]string, limit int) (ExplodePage, error)
	ReleasedPage(ctx context.Context, after string, limit int) (ReleasedPage, error)
	GroupChange(ctx context.Context, releasedIDs []string) ([]ItemResult, error)
}

type Summary struct {
	Pages     int
	Items     int
	Succeeded int
	Skipped   int
}

type ExplodeParams struct {
	MasterID string              `json:"masterId"`
	Selected map[string][]string `json:"selected"`
}

type ProductJobs struct {
	Client      ProductClient
	Pages       PageStore
	PageSize    int
	MaxAttempts int
}

func (p ProductJobs) Run(ctx context.Context, handler, runID string) (Summary, error) {
	size := p.PageSize
	if size == 0 {
		size = MaxPageSize
	}
	known := handler == HandlerProductExplode || handler == HandlerProductGroupChange
	if p.Client == nil || p.Pages == nil || p.MaxAttempts <= 0 || size < 1 || size > MaxPageSize || !ids.Valid(runID) || !known {
		return Summary{}, ErrInvalidArgument
	}
	claim, err := p.Pages.Claim(ctx, runID, handler)
	if err != nil {
		return Summary{}, err
	}
	var summary Summary
	switch handler {
	case HandlerProductExplode:
		var in ExplodeParams
		if jsonErr := json.Unmarshal(claim.Params, &in); jsonErr != nil || !ids.Valid(in.MasterID) {
			err = fmt.Errorf("batch_run.params: %w", ErrInvalidArgument)
		} else {
			summary, err = p.explode(ctx, runID, in, size)
		}
	case HandlerProductGroupChange:
		summary, err = p.groupChange(ctx, runID, size)
	}
	status, msg := domain.RunEnded, ""
	if err != nil {
		status, msg = domain.RunError, err.Error()
	}
	if finishErr := p.Pages.Finish(context.WithoutCancel(ctx), runID, status, msg); finishErr != nil {
		slog.Error("finish batch run", "err", finishErr, "runId", runID, "handler", handler)
		err = errors.Join(err, finishErr)
	}
	return summary, err
}

func (p ProductJobs) explode(ctx context.Context, runID string, in ExplodeParams, size int) (Summary, error) {
	var summary Summary
	resume, err := p.Pages.Resume(ctx, runID)
	if err != nil || resume.Done {
		return summary, err
	}
	previous := remainingIn(resume.Cursor)
	for pageNo := resume.NextPage; ; pageNo++ {
		var res ExplodePage
		err := p.attempt(ctx, func() (err error) {
			res, err = p.Client.ExplodePage(ctx, in.MasterID, in.Selected, size)
			return err
		})
		if err == nil && res.Created == 0 && res.Remaining > 0 {
			err = errors.New("explode made no progress")
		}
		created := res.Created
		if err == nil && previous >= 0 {
			created = max(created, previous-res.Remaining)
		}
		page := Page{No: pageNo, Cursor: cursorFor(pageNo), Items: created, Succeeded: created, Status: PageOK}
		if res.Remaining > 0 {
			page.NextCursor = fmt.Sprintf("%s:%d", moreCursor, res.Remaining)
		}
		if err := p.settle(ctx, runID, &page, err); err != nil {
			return summary, err
		}
		summary.add(page)
		if res.Remaining == 0 {
			return summary, nil
		}
		previous = res.Remaining
	}
}

func remainingIn(cursor string) int {
	rest, ok := strings.CutPrefix(cursor, moreCursor+":")
	if !ok {
		return -1
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return -1
	}
	return n
}

func (p ProductJobs) groupChange(ctx context.Context, runID string, size int) (Summary, error) {
	var summary Summary
	resume, err := p.Pages.Resume(ctx, runID)
	if err != nil || resume.Done {
		return summary, err
	}
	for pageNo, after := resume.NextPage, resume.Cursor; ; pageNo++ {
		var released ReleasedPage
		var results []ItemResult
		err := p.attempt(ctx, func() (err error) {
			if released, err = p.Client.ReleasedPage(ctx, after, size); err != nil || len(released.IDs) == 0 {
				return err
			}
			if results, err = p.Client.GroupChange(ctx, released.IDs); err == nil && len(results) != len(released.IDs) {
				err = fmt.Errorf("group change returned %d results for %d items", len(results), len(released.IDs))
			}
			return err
		})
		page := Page{No: pageNo, Cursor: after, NextCursor: released.Next, Items: len(released.IDs), Status: PageOK}
		for _, r := range results {
			switch r.Status {
			case statusChanged:
				page.Succeeded++
			case statusSkipped:
				page.Skipped++
			}
		}
		if err := p.settle(ctx, runID, &page, err); err != nil {
			return summary, err
		}
		summary.add(page)
		if released.Next == "" {
			return summary, nil
		}
		after = released.Next
	}
}

func (p ProductJobs) settle(ctx context.Context, runID string, page *Page, cause error) error {
	if cause != nil {
		page.Status, page.Error, page.NextCursor = PageFailed, cause.Error(), ""
		slog.Error("batch page failed", "err", cause, "runId", runID, "page", page.No)
	}
	if err := p.Pages.Record(context.WithoutCancel(ctx), runID, *page); err != nil {
		return errors.Join(cause, fmt.Errorf("record page %d: %w", page.No, err))
	}
	if cause != nil {
		return fmt.Errorf("page %d: %w", page.No, cause)
	}
	return nil
}

func (p ProductJobs) attempt(ctx context.Context, call func() error) error {
	var err error
	for range p.MaxAttempts {
		if err = call(); err == nil || errors.Is(err, ErrRejected) || ctx.Err() != nil {
			return err
		}
	}
	return err
}

func (s *Summary) add(page Page) {
	s.Pages++
	s.Items += page.Items
	s.Succeeded += page.Succeeded
	s.Skipped += page.Skipped
}

func cursorFor(pageNo int) string { return fmt.Sprintf("page-%d", pageNo) }
