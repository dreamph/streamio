package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dreamph/streamio"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

func newServerApp(processIO streamio.ProcessIO) (*fiber.App, error) {
	if processIO == nil {
		return nil, fmt.Errorf("processIO is required")
	}

	app := fiber.New(fiber.Config{
		BodyLimit: 100 * 1024 * 1024,
	})

	app.Post("/process-by-io", func(c *fiber.Ctx) error {
		fileHeader, err := c.FormFile("file")
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "missing multipart form field \"file\"")
		}

		inReader := streamio.NewMultipartStreamReader(fileHeader)
		defer inReader.Cleanup()

		outputExt := filepath.Ext(fileHeader.Filename)
		if outputExt == "" {
			outputExt = ".bin"
		}

		session := processIO.NewSession(uuid.New().String(), streamio.SessionOption{WriterType: streamio.SessionWriterTempFile})
		defer session.Release()

		ctx := requestContext(c)

		result, err := session.Do(ctx, outputExt, func(ctx context.Context, out streamio.StreamWriter) error {
			_, err := streamio.CopyStream(inReader, out)
			return err
		})
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, fmt.Sprintf("process failed: %v", err))
		}

		streamReader := result.Keep().AsStreamReader()
		reader, err := streamReader.Open()
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, fmt.Sprintf("open reader failed: %v", err))
		}

		c.Type("application/octet-stream")
		c.Set(fiber.HeaderContentDisposition, fmt.Sprintf("attachment; filename=%q", "processed"+outputExt))

		return c.SendStream(reader)
	})

	app.Post("/process-by-bytes", func(c *fiber.Ctx) error {
		fileHeader, err := c.FormFile("file")
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "missing multipart form field \"file\"")
		}

		file, err := fileHeader.Open()
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, fmt.Sprintf("open upload failed: %v", err))
		}
		defer file.Close()

		data, err := io.ReadAll(file)
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, fmt.Sprintf("read upload failed: %v", err))
		}

		var out bytes.Buffer
		out.Write(data)

		c.Type("application/octet-stream")
		c.Set(fiber.HeaderContentDisposition, fmt.Sprintf("attachment; filename=%q", fileHeader.Filename))
		return c.Send(out.Bytes())
	})

	return app, nil
}

func main() {
	processIO, err := streamio.NewProcessIO()
	if err != nil {
		log.Fatalf("streamio.NewProcessIO: %v", err)
	}
	defer processIO.Release()

	app, err := newServerApp(processIO)
	if err != nil {
		log.Fatalf("newServerApp: %v", err)
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- app.Listen(":8080")
	}()

	log.Println("server listening on :8080")

	select {
	case <-shutdownCtx.Done():
		log.Println("shutdown signal received")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.ShutdownWithContext(ctx); err != nil {
			log.Printf("server shutdown error: %v", err)
		}
		if err := <-serverErr; err != nil {
			log.Printf("server exited with error: %v", err)
		}
	case err := <-serverErr:
		if err != nil {
			log.Fatalf("server error: %v", err)
		}
	}

	log.Println("server stopped")
}

func requestContext(c *fiber.Ctx) context.Context {
	ctx := c.UserContext()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
