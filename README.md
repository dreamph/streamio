streamio
========

`streamio` Stream file processing in Go with predictable memory usage via disk-backed sessions and optional in-memory mode.

Contents
--------
- [Features](#features)
- [Requirements](#requirements)
- [Installation](#installation)
- [Quick start](#quick-start)
- [Choosing a session writer](#choosing-a-session-writer)
- [License](#license)

Features
--------
- Unified `StreamReader`/`StreamWriter` interfaces for bytes, files, multipart uploads, and download responses.
- Auto-cleaned `TempFile` helper that implements `io.Reader`, `io.Writer`, `io.Seeker`, and can be re-opened as a reader or writer at any time.
- Session-scoped `IOManager` that isolates temp directories, enforces cleanup, and lets you pick output backends (`TempFile` vs in-memory bytes).
- `CopyStream`, `Output`, and helper utilities for cloning artifacts, saving to disk, or passing data down a multi-stage pipeline.

Requirements
------------
- Go 1.23 or newer

Installation
------------
```bash
go get github.com/dreamph/streamio
```

Import the module in your project with:

```go
import "github.com/dreamph/streamio"
```

Quick start
-----------
```go
ctx := context.Background()

ioManager, err := streamio.NewIOManager("/mytemp/app")
if err != nil {
	log.Fatalf("io manager: %v", err)
}
defer ioManager.Release()

session := ioManager.NewSession(uuid.New().String())
defer session.Release()

output, err := session.Do(ctx, ".txt", func(ctx context.Context, w streamio.StreamWriter) error {
	reader := streamio.NewBytesStreamReader("payload.txt", []byte("hello world"))
	_, err := streamio.CopyStream(reader, w)
	return err
})
if err != nil {
	log.Fatalf("session.Do: %v", err)
}
defer output.Cleanup()

data, err := output.Bytes()
if err != nil {
	log.Fatalf("read output: %v", err)
}
fmt.Printf("processed %d bytes\n", len(data))
```

Choosing a session writer
-------------------------
Sessions default to disk-backed temp files. Switch to an in-memory writer for small outputs, or override per invocation:

```go
session := ioManager.NewSession(uuid.New().String(), streamio.SessionOption{
	WriterType: streamio.OutputBytes, // or streamio.OutputTempFile
})
defer session.Release()

// Override per call (session default stays intact)
output, err := session.Do(ctx, ".zip", func(ctx context.Context, w streamio.StreamWriter) error {
	reader := streamio.NewBytesStreamReader("payload.bin", payload)
	_, err := streamio.CopyStream(reader, w)
	return err
}, streamio.SessionOption{WriterType: streamio.OutputTempFile})
if err != nil {
	log.Fatalf("session.Do override: %v", err)
}
defer output.Cleanup()
```

License
-------
Streamio is distributed under the MIT License. See `LICENSE` for details.


Buy Me a Coffee
=======
[!["Buy Me A Coffee"](https://www.buymeacoffee.com/assets/img/custom_images/orange_img.png)](https://www.buymeacoffee.com/dreamph)