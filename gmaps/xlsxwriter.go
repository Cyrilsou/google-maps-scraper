package gmaps

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"sync"

	"github.com/gosom/scrapemate"
)

// XLSXWriter is a scrapemate.ResultWriter that accumulates entries in
// memory and streams them into a multi-sheet XLSX workbook when the input
// channel is closed.
//
// The trade-off is intentional: XLSX cannot be appended incrementally like
// CSV can (the ZIP footer is only known at the end), and a typical web-runner
// job fits comfortably in RAM. For very large batches, keep using CSV.
type XLSXWriter struct {
	w io.WriteCloser

	mu      sync.Mutex
	entries []*Entry
}

var _ scrapemate.ResultWriter = (*XLSXWriter)(nil)

// NewXLSXWriter returns a writer that will flush to w when the result
// channel closes. w is closed after the flush.
func NewXLSXWriter(w io.WriteCloser) *XLSXWriter {
	return &XLSXWriter{w: w}
}

// Run consumes results until the channel closes, then writes a workbook
// and closes the output. It never returns a fatal error on unexpected types
// so a misrouted result does not break the whole pipeline.
func (x *XLSXWriter) Run(_ context.Context, in <-chan scrapemate.Result) error {
	for result := range in {
		x.collect(result.Data)
	}

	x.mu.Lock()
	entries := x.entries
	x.entries = nil
	x.mu.Unlock()

	defer func() { _ = x.w.Close() }()

	if err := WriteXLSX(x.w, entries); err != nil {
		return fmt.Errorf("xlsx write: %w", err)
	}

	return nil
}

func (x *XLSXWriter) collect(data any) {
	if data == nil {
		return
	}

	if e, ok := data.(*Entry); ok {
		x.mu.Lock()
		x.entries = append(x.entries, e)
		x.mu.Unlock()

		return
	}

	if entries, ok := data.([]*Entry); ok {
		x.mu.Lock()
		x.entries = append(x.entries, entries...)
		x.mu.Unlock()

		return
	}

	// Fall back to reflection for slice-of-*Entry shapes that type-assertion
	// misses (e.g. when the scraper wraps entries in a named slice type).
	rv := reflect.ValueOf(data)
	if rv.Kind() != reflect.Slice {
		return
	}

	x.mu.Lock()
	defer x.mu.Unlock()

	for i := 0; i < rv.Len(); i++ {
		if e, ok := rv.Index(i).Interface().(*Entry); ok {
			x.entries = append(x.entries, e)
		}
	}
}
