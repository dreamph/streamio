package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"github.com/dreamph/streamio"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

func newServerApp(processIO streamio.ProcessIO) (*fiber.App, error) {
	if processIO == nil {
		return nil, fmt.Errorf("processIO is required")
	}

	app := fiber.New()

	app.Post("/process-by-io", func(c *fiber.Ctx) error {
		fileHeader, err := c.FormFile("file")
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "missing multipart form field \"file\"")
		}

		reader := streamio.NewMultipartStreamReader(fileHeader)
		defer reader.Cleanup()

		outputExt := filepath.Ext(fileHeader.Filename)
		if outputExt == "" {
			outputExt = ".bin"
		}

		session := processIO.NewSession(uuid.New().String())
		defer session.Release()

		ctx := requestContext(c)

		result, err := session.Do(ctx, outputExt, func(ctx context.Context, out streamio.StreamWriter) error {
			_, err := streamio.CopyStream(reader, out)
			return err
		})
		if err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, fmt.Sprintf("process failed: %v", err))
		}
		defer result.Cleanup()

		streamReader := result.AsStreamReader()
		stream, err := streamio.OpenReader(streamReader)
		if err != nil {
			_ = streamReader.Cleanup()
			return fiber.NewError(fiber.StatusInternalServerError, fmt.Sprintf("open reader failed: %v", err))
		}
		defer stream.Close()
		defer streamReader.Cleanup()

		c.Type("application/octet-stream")
		c.Set(fiber.HeaderContentDisposition, fmt.Sprintf("attachment; filename=%q", "processed"+outputExt))
		return c.SendStream(stream)
	})

	app.Post("/process-by-bytes", func(c *fiber.Ctx) error {
		fileHeader, err := c.FormFile("file")
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "missing multipart form field \"file\"")
		}

		reader := streamio.NewMultipartStreamReader(fileHeader)
		defer reader.Cleanup()

		writer := streamio.NewBytesStreamWriter()

		if _, err := streamio.CopyStream(reader, writer); err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, fmt.Sprintf("process failed: %v", err))
		}

		c.Type("application/octet-stream")
		c.Set(fiber.HeaderContentDisposition, fmt.Sprintf("attachment; filename=%q", fileHeader.Filename))
		return c.Send(writer.Bytes())
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

	log.Println("server listening on :8080")
	if err := app.Listen(":8080"); err != nil {
		log.Fatal(err)
	}
}

func requestContext(c *fiber.Ctx) context.Context {
	ctx := c.UserContext()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
