streamio
========

`streamio` is a Go module for building streaming file-processing workflows without leaking disk space or juggling raw `io` primitives. It ships with a production-ready Fiber server that demonstrates how to accept multipart uploads, process them through temporary sessions, and stream the results back to clients.

Features
--------
- High-level `StreamReader`/`StreamWriter` abstractions for bytes, files, multipart uploads, and generic readers.
- Disk-backed `TempFile` helper that behaves like both reader and writer and cleans itself up automatically.
- `Pipeline` utility for chaining multi-stage transformations while managing intermediate artifacts.
- Session-scoped `ProcessIO` manager that isolates temp directories per request/job and ensures cleanup with configurable writer backends.
- Sample Fiber HTTP server exposing `/process-by-io` (multipart) and `/process-by-bytes` endpoints for immediate experimentation.
- Concurrent load tests that stress the HTTP endpoints and catch regressions early.

Requirements
------------
- Go 1.23 or newer

Installation
------------
```bash
go get github.com/dreamph/streamio
```

The command above adds the module to your `go.mod`. Import it with:

```go
import "github.com/dreamph/streamio"
```

Library Overview
----------------
### Working with temporary outputs
```go
ctx := context.Background()
processIO, _ := streamio.NewProcessIO()
defer processIO.Release()

session := processIO.NewSession("example")
defer session.Release()

output, err := session.Do(ctx, ".txt", func(ctx context.Context, w streamio.StreamWriter) error {
	reader := streamio.NewBytesStreamReader("payload.txt", []byte("hello world"))
	_, err := streamio.CopyStream(reader, w)
	return err
})
if err != nil {
	log.Fatal(err)
}
defer output.Cleanup()

bytes, _ := output.Bytes()
fmt.Printf("processed %d bytes\n", len(bytes))
```

### Choosing a session writer type
By default, `ProcessSession` writes to disk-backed temp files. You can switch to an in-memory writer for small outputs:

```go
session := processIO.NewSession("example-bytes", streamio.SessionOption{
	WriterType: streamio.SessionWriterBytes, // or streamio.SessionWriterTempFile
})
defer session.Release()

// Override per call (keeps session default untouched)
output, err := session.Do(ctx, ".zip", func(ctx context.Context, w streamio.StreamWriter) error {
	reader := streamio.NewBytesStreamReader("payload.bin", payload)
	_, err := streamio.CopyStream(reader, w)
	return err
}, streamio.SessionOption{WriterType: streamio.SessionWriterTempFile})
```

HTTP Server
-----------
The `server` package exposes two endpoints that mirror typical ingestion flows:

- `POST /process-by-io`: accepts a multipart file field named `file`, streams the upload via `ProcessIO`, and returns the processed bytes.
- `POST /process-by-bytes`: accepts a raw request body, echoes the processed payload.

Run the sample server:
```bash
go run ./server
```

Load & Regression Tests
-----------------------
The repository contains concurrency-focused tests (`server/main_test.go`) that issue burst traffic against both routes. Run the whole suite with:
```bash
go test ./...
```

Project Layout
--------------
- `stream.go`: core library (readers, writers, pipelines, `ProcessIO`, temp files).
- `server/`: Fiber example server plus load tests.
- `README.md`: you are here.

License
-------
Streamio is distributed under the MIT License. See `LICENSE` for details.
