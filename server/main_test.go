package main

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/dreamph/streamio"

	"github.com/gofiber/fiber/v2"
)

func TestProcessEndpointsHandleConcurrentLoad(t *testing.T) {
	app := newTestApp(t)
	payload := bytes.Repeat([]byte("streamio-load-test-"), 1000000) // ~10KB payload

	t.Run("process-by-io", func(t *testing.T) {
		beforeMem := captureMemSnapshot()
		duration := runLoadTest(t, app, func() *http.Request {
			return newMultipartRequest(t, "/process-by-io", payload)
		}, payloadValidator(http.StatusOK, payload), 8, 10)
		afterMem := captureMemSnapshot()

		total := 8 * 10
		t.Logf("/process-by-io handled %d requests in %s (avg %s/request)", total, duration, duration/time.Duration(total))
		logMemUsage(t, "process-by-io", beforeMem, afterMem)
	})

	t.Run("process-by-bytes", func(t *testing.T) {
		beforeMem := captureMemSnapshot()
		duration := runLoadTest(t, app, func() *http.Request {
			return newMultipartRequest(t, "/process-by-bytes", payload)
		}, payloadValidator(http.StatusOK, payload), 8, 10)
		afterMem := captureMemSnapshot()

		total := 8 * 10
		t.Logf("/process-by-bytes handled %d requests in %s (avg %s/request)", total, duration, duration/time.Duration(total))
		logMemUsage(t, "process-by-bytes", beforeMem, afterMem)
	})
}

func newTestApp(t *testing.T) *fiber.App {
	t.Helper()

	ioManager, err := streamio.NewIOManager("/Users/dream/Data/projects/dreamph/streamio/tmp")
	if err != nil {
		t.Fatalf("streamio.NewIOManager: %v", err)
	}
	t.Cleanup(func() {
		_ = ioManager.Release()
	})

	app, err := newServerApp(ioManager)
	if err != nil {
		t.Fatalf("newServerApp: %v", err)
	}

	return app
}

func runLoadTest(t *testing.T, app *fiber.App, requestFactory func() *http.Request, validator func(status int, body []byte) error, workers, iterations int) time.Duration {
	t.Helper()

	totalRequests := workers * iterations
	errCh := make(chan error, totalRequests)
	var wg sync.WaitGroup

	start := time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				req := requestFactory()
				resp, err := app.Test(req, -1)
				if err != nil {
					errCh <- fmt.Errorf("request failed: %w", err)
					return
				}
				body, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				if readErr != nil {
					errCh <- fmt.Errorf("read body: %w", readErr)
					return
				}
				if validator != nil {
					if err := validator(resp.StatusCode, body); err != nil {
						errCh <- err
						return
					}
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)

	if err := <-errCh; err != nil {
		t.Fatalf("load test failed: %v", err)
	}

	return time.Since(start)
}

func payloadValidator(expectedStatus int, expectedBody []byte) func(status int, body []byte) error {
	return func(status int, body []byte) error {
		if status != expectedStatus {
			return fmt.Errorf("unexpected status code: got %d want %d", status, expectedStatus)
		}
		if !bytes.Equal(body, expectedBody) {
			return fmt.Errorf("unexpected response payload: got %d bytes want %d bytes", len(body), len(expectedBody))
		}
		return nil
	}
}

func newMultipartRequest(t *testing.T, path string, payload []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	fileWriter, err := writer.CreateFormFile("file", fmt.Sprintf("payload-%d.bin", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fileWriter.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart writer close: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

type memSnapshot struct {
	alloc      uint64
	totalAlloc uint64
	sys        uint64
	numGC      uint32
}

func captureMemSnapshot() memSnapshot {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return memSnapshot{
		alloc:      ms.Alloc,
		totalAlloc: ms.TotalAlloc,
		sys:        ms.Sys,
		numGC:      ms.NumGC,
	}
}

func logMemUsage(t *testing.T, label string, before, after memSnapshot) {
	t.Helper()
	t.Logf("%s memory: alloc=%s (Δ%s) total_alloc=%s sys=%s gc=%d→%d",
		label,
		formatBytes(after.alloc),
		formatBytesDiff(after.alloc, before.alloc),
		formatBytes(after.totalAlloc),
		formatBytes(after.sys),
		before.numGC,
		after.numGC,
	)
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func formatBytesDiff(after, before uint64) string {
	if after <= before {
		return fmt.Sprintf("-%s", formatBytes(before-after))
	}
	return fmt.Sprintf("+%s", formatBytes(after-before))
}
