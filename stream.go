package streamio

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"errors"

	"github.com/google/uuid"
)

/* ---------- Core Types ---------- */

// Reader represents a readable stream that supports seeking and closing.
type Reader interface {
	io.Reader
	io.Seeker
	io.Closer
}

// Writer represents a writable stream that supports closing.
type Writer interface {
	io.Writer
	io.Closer
}

// FileInfo holds basic metadata about a file or stream.
type FileInfo struct {
	Name        string
	Size        int64
	ContentType string
}

/* ---------- High-level Abstractions ---------- */

// StreamReader is a high-level abstraction for any data source
// that can be opened as a Reader (e.g. bytes, files, multipart uploads).
type StreamReader interface {
	Open() (Reader, error)
	Meta() FileInfo
	Cleanup() error
}

// StreamWriter is a high-level abstraction for any destination
// that can create a Writer for writing data (e.g. files, buffers, temp files).
type StreamWriter interface {
	Create() (Writer, error)
}

/* ---------- Internal Reader Wrapper ---------- */

type readStream struct {
	io.ReadSeeker
}

func (s *readStream) Close() error { return nil }

func newReadStream(rs io.ReadSeeker) Reader {
	return &readStream{ReadSeeker: rs}
}

// open streamio.StreamReader
func OpenReader(src StreamReader) (Reader, error) {
	if src == nil {
		return nil, errors.New("nil source")
	}
	rs, err := src.Open()
	if err != nil {
		return nil, err
	}
	return rs, nil
}

// open streamio.StreamWriter
func OpenWriter(dest StreamWriter) (Writer, error) {
	if dest == nil {
		return nil, errors.New("nil destination")
	}
	w, err := dest.Create()
	if err != nil {
		return nil, err
	}
	return w, nil
}

func Reset(rs Reader) error {
	_, err := rs.Seek(0, io.SeekStart)
	if err != nil {
		return err
	}
	return nil
}

/* ---------- Generic io.Reader Implementation (StreamReader) ---------- */

/*
type genericStreamReader struct {
	name        string
	contentType string
	data        []byte
	reader      io.Reader
}

func (s *genericStreamReader) Open() (Reader, error) {
	// Lazily buffer reader contents the first time Open() is called
	if s.data == nil && s.reader != nil {
		data, err := io.ReadAll(s.reader)
		if err != nil {
			return nil, err
		}
		s.data = data
		s.reader = nil
	}
	return newReadStream(bytes.NewReader(s.data)), nil
}

func (s *genericStreamReader) Meta() FileInfo {
	return FileInfo{
		Name:        s.name,
		Size:        int64(len(s.data)), // Will be 0 until Open() is called
		ContentType: s.contentType,
	}
}

func (s *genericStreamReader) Cleanup() error {
	s.reader = nil
	s.data = nil
	return nil
}

// NewGenericStreamReader creates a StreamReader from a generic io.Reader.
// The data is buffered into memory when Open() is first called.
func NewGenericStreamReader(name, contentType string, r io.Reader) StreamReader {
	return &genericStreamReader{name: name, contentType: contentType, reader: r}
}
*/

/* ---------- Multipart.FileHeader Implementation (StreamReader) ---------- */

type multipartStreamReader struct {
	header *multipart.FileHeader
}

func (s *multipartStreamReader) Open() (Reader, error) {
	f, err := s.header.Open()
	if err != nil {
		return nil, err
	}
	// multipart.File implements Read, Seek, Close → matches Reader
	return f, nil
}

func (s *multipartStreamReader) Meta() FileInfo {
	contentType := s.header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return FileInfo{
		Name:        s.header.Filename,
		Size:        s.header.Size,
		ContentType: contentType,
	}
}

// NewMultipartStreamReader creates a StreamReader from a multipart.FileHeader (e.g. Fiber upload).
func NewMultipartStreamReader(h *multipart.FileHeader) StreamReader {
	if h == nil {
		return nil
	}
	return &multipartStreamReader{header: h}
}

func (s *multipartStreamReader) Cleanup() error {
	s.header = nil
	return nil
}

/* ---------- File Path Implementation (StreamReader) ---------- */

type fileStreamReader struct {
	path string
	meta FileInfo
}

func (s *fileStreamReader) Open() (Reader, error) {
	return os.Open(s.path)
}

func (s *fileStreamReader) Meta() FileInfo { return s.meta }

func (s *fileStreamReader) Cleanup() error {
	return nil
}

// NewFileStreamReader creates a StreamReader from a file path on disk.
func NewFileStreamReader(path string) (StreamReader, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	contentType := mime.TypeByExtension(filepath.Ext(path))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return &fileStreamReader{
		path: path,
		meta: FileInfo{
			Name:        st.Name(),
			Size:        st.Size(),
			ContentType: contentType,
		},
	}, nil
}

/* ---------- []byte Implementation (StreamReader) ---------- */

type bytesStreamReader struct {
	name        string
	contentType string
	data        []byte
}

func (s *bytesStreamReader) Open() (Reader, error) {
	r := bytes.NewReader(s.data)
	return newReadStream(r), nil
}

func (s *bytesStreamReader) Meta() FileInfo {
	return FileInfo{
		Name:        s.name,
		Size:        int64(len(s.data)),
		ContentType: s.contentType,
	}
}

func (s *bytesStreamReader) Cleanup() error {
	s.data = nil
	return nil
}

// NewBytesStreamReader creates a StreamReader from a byte slice.
func NewBytesStreamReader(name string, data []byte) StreamReader {
	contentType := mime.TypeByExtension(filepath.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return &bytesStreamReader{name: name, contentType: contentType, data: data}
}

/* ---------- TempFile (Auto Cleanup, Read/Write Capable) ---------- */

/*
TempFile
- Used for temp-file backed I/O
- Focus: rely on disk instead of RAM, auto cleanup, share a single handle compatible with Output.Bytes()
*/
type TempFile struct {
	Path string   // path of the temp file on disk
	file *os.File // current handle (nil means not open yet)
}

// NewTempFile creates a temp file with a random name.
func NewTempFile(prefix string) (*TempFile, error) {
	f, err := os.CreateTemp("", prefix)
	if err != nil {
		return nil, err
	}
	return &TempFile{Path: f.Name(), file: f}, nil
}

// NewTempFileWithName creates a temp file using prefix + filename.
func NewTempFileWithName(prefix, filename string) (*TempFile, error) {
	dir := os.TempDir()
	path := filepath.Join(dir, prefix+filename)

	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}

	return &TempFile{Path: path, file: f}, nil
}

// NewTempFileInDir creates a temp file inside the provided directory.
func NewTempFileInDir(dir, prefix, filename string) (*TempFile, error) {
	path := filepath.Join(dir, prefix+filename)

	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}

	return &TempFile{Path: path, file: f}, nil
}

/* ---------- Implement Reader / Writer / Seeker / Closer ---------- */

// Read implements io.Reader.
func (t *TempFile) Read(p []byte) (int, error) {
	if t.file == nil {
		// Lazily open the file in read-only mode if needed.
		f, err := os.Open(t.Path)
		if err != nil {
			return 0, err
		}
		t.file = f
	}
	return t.file.Read(p)
}

// Write implements io.Writer.
func (t *TempFile) Write(p []byte) (int, error) {
	if t.file == nil {
		// Lazily open the file read-write if no handle exists.
		f, err := os.OpenFile(t.Path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
		if err != nil {
			return 0, err
		}
		t.file = f
	}
	return t.file.Write(p)
}

// Seek implements io.Seeker.
func (t *TempFile) Seek(offset int64, whence int) (int64, error) {
	if t.file == nil {
		// Open in read-only mode if no handle is available.
		f, err := os.Open(t.Path)
		if err != nil {
			return 0, err
		}
		t.file = f
	}
	return t.file.Seek(offset, whence)
}

// Close closes the current handle but keeps the file on disk.
func (t *TempFile) Close() error {
	if t.file == nil {
		return nil
	}
	err := t.file.Close()
	t.file = nil
	return err
}

/* ---------- Utility methods ---------- */

// Cleanup closes the handle and deletes the file from disk.
func (t *TempFile) Cleanup() error {
	_ = t.Close()
	return os.Remove(t.Path)
}

// Exists reports whether the file still exists on disk.
func (t *TempFile) Exists() bool {
	_, err := os.Stat(t.Path)
	return err == nil
}

// Size returns the file size on disk (bytes).
func (t *TempFile) Size() int64 {
	info, err := os.Stat(t.Path)
	if err != nil {
		return 0
	}
	return info.Size()
}

/* ---------- Make TempFile act as a StreamReader ---------- */

// Meta returns the FileInfo of the temp file.
func (t *TempFile) Meta() FileInfo {
	info, err := os.Stat(t.Path)
	size := int64(0)
	if err == nil {
		size = info.Size()
	}

	contentType := mime.TypeByExtension(filepath.Ext(t.Path))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return FileInfo{
		Name:        filepath.Base(t.Path),
		Size:        size,
		ContentType: contentType,
	}
}

// Open exposes TempFile as a StreamReader.
// Returns a Reader that supports Seek and Close.
func (t *TempFile) Open() (Reader, error) {
	// Close any existing handle first so the cursor resets.
	if t.file != nil {
		_ = t.file.Close()
		t.file = nil
	}

	f, err := os.Open(t.Path)
	if err != nil {
		return nil, err
	}

	t.file = f
	return t, nil // TempFile implement Reader (Read + Seek + Close)
}

/* ---------- Make TempFile act as a StreamWriter ---------- */

// Create exposes TempFile as a StreamWriter.
// Always prepare a blank file before writing.
func (t *TempFile) Create() (Writer, error) {
	// Close the previous handle first (if any).
	if t.file != nil {
		_ = t.file.Close()
		t.file = nil
	}

	// os.Create truncates any existing file.
	f, err := os.Create(t.Path)
	if err != nil {
		return nil, err
	}

	t.file = f
	return t, nil // TempFile implement Writer (Write + Close)
}

/* ---------- Helper adapters for existing code ---------- */

// AsStreamReader exposes TempFile as a StreamReader.
func (t *TempFile) AsStreamReader() StreamReader {
	return t
}

// AsStreamWriter exposes TempFile as a StreamWriter.
func (t *TempFile) AsStreamWriter() StreamWriter {
	return t
}

// AsOutStream kept for legacy naming (alias of AsStreamWriter).
func (t *TempFile) AsOutStream() StreamWriter {
	return t
}

func GenerateFileName(fileName string) string {
	fileExtension := filepath.Ext(fileName)
	return uuid.New().String() + fileExtension
}

// WithTemp lifecycle helper
func WithTemp(fileExtension string, fn func(*TempFile) error) (err error) {
	tmp, err := NewTempFileWithName("temp-file-", GenerateFileName(fileExtension))
	if err != nil {
		return err
	}
	defer tmp.Cleanup()
	return fn(tmp)
}

func Do(ctx context.Context, outputFileExtension string, doFn func(ctx context.Context, out StreamWriter) error) (*Output, error) {
	tmp, err := NewTempFileWithName("temp-file-", GenerateFileName(outputFileExtension))
	if err != nil {
		return nil, err
	}

	err = doFn(ctx, tmp.AsStreamWriter())
	if err != nil {
		_ = tmp.Cleanup()
		return nil, err
	}

	return &Output{tmp: tmp}, nil
}

/* ---------- In-memory StreamWriter (Buffer) ---------- */

type bufferWriteStream struct{ buf *bytes.Buffer }

func (w *bufferWriteStream) Write(p []byte) (int, error) { return w.buf.Write(p) }
func (w *bufferWriteStream) Close() error                { return nil }

type bytesStreamWriter struct {
	buf *bytes.Buffer
}

func (d *bytesStreamWriter) Create() (Writer, error) {
	d.buf.Reset()
	return &bufferWriteStream{buf: d.buf}, nil
}

func (d *bytesStreamWriter) Bytes() []byte { return d.buf.Bytes() }

// NewBytesStreamWriter creates a StreamWriter that writes data into memory.
// Use Bytes() to retrieve the final in-memory result.
func NewBytesStreamWriter() *bytesStreamWriter {
	return &bytesStreamWriter{
		buf: &bytes.Buffer{},
	}
}

/* ---------- File Path Implementation (StreamWriter) ---------- */

type fileStreamWriter struct {
	path string
}

func (d *fileStreamWriter) Create() (Writer, error) {
	f, err := os.Create(d.path)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// NewFileStreamWriter creates a StreamWriter that writes data to a file path on disk.
func NewFileStreamWriter(path string) StreamWriter {
	return &fileStreamWriter{
		path: path,
	}
}

/* ---------- adapters & utilities ---------- */
func CopyStream(src StreamReader, dst StreamWriter) (int64, error) {
	r, err := OpenReader(src)
	if err != nil {
		return 0, err
	}
	defer r.Close()

	w, err := OpenWriter(dst)
	if err != nil {
		return 0, err
	}
	defer w.Close()

	return io.Copy(w, r)
}

func Copy(src Reader, dst Writer) (int64, error) {
	if err := Reset(src); err != nil {
		return 0, err
	}
	return io.Copy(dst, src)
}

type Output struct {
	bytes []byte
	tmp   *TempFile

	keep    bool    // <--- new
	session Session // <--- optional: link session to remove from tmpFiles
}

func (o *Output) AsStreamReader() StreamReader {
	return o.tmp.AsStreamReader()
}

func (o *Output) Cleanup() {
	if o.tmp != nil {
		_ = o.tmp.Cleanup()
	}
}

func (o *Output) Bytes() ([]byte, error) {
	if o.bytes != nil {
		return o.bytes, nil
	}
	if o.tmp == nil {
		return nil, fmt.Errorf("no data")
	}
	if _, err := o.tmp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(o.tmp)
	if err != nil {
		return nil, err
	}

	o.bytes = b

	return b, nil
}

func (o *Output) Path() (string, error) {
	if o.tmp == nil {
		return "", fmt.Errorf("no tempfile")
	}
	return o.tmp.Path, nil
}

// Keep on session and will delete on process release
func (o *Output) Keep() *Output {
	if o == nil || o.tmp == nil || o.session == nil {
		return o
	}

	// mark this output as keep
	o.keep = true

	// remove this tmp file from session tracking when supported
	if cleaner, ok := o.session.(interface{ unregisterTempFile(*TempFile) }); ok {
		cleaner.unregisterTempFile(o.tmp)
	}

	// detach from session (so session is not responsible anymore)
	o.session = nil

	return o
}

/* ---------- Pipeline Pattern ---------- */
/*
// Pipeline manages a sequence of file processing stages with automatic temp file management.
type Pipeline struct {
	stages    []*Stage
	tempFiles []*TempFile
	finalOut  StreamWriter
	current   int // Track current stage index
}

// Stage represents a single processing step with input StreamReader and output StreamWriter.
type Stage struct {
	input  StreamReader
	output StreamWriter
	temp   *TempFile
}

// ProcessFunc is a function that processes data from input to output.
type ProcessFunc func(ctx context.Context, input StreamReader, output StreamWriter) error

// NewPipeline creates a new processing pipeline.
func NewPipeline(input StreamReader, output StreamWriter) *Pipeline {
	return &Pipeline{
		stages: []*Stage{{
			input:  input,
			output: output,
		}},
		tempFiles: make([]*TempFile, 0),
		finalOut:  output,
		current:   0,
	}
}

// Input returns the input StreamReader for this stage.
func (s *Stage) Input() StreamReader {
	return s.input
}

// Output returns the output StreamWriter for this stage.
func (s *Stage) Output() StreamWriter {
	return s.output
}

// InputReader opens and returns a Reader for this stage's input.
func (s *Stage) InputReader() (Reader, error) {
	return OpenReader(s.input)
}

// OutputWriter creates and returns a Writer for this stage's output.
func (s *Stage) OutputWriter() (Writer, error) {
	return OpenWriter(s.output)
}

// Run executes a processing function on the pipeline.
// Automatically manages stage creation and progression.
func (p *Pipeline) Run(ctx context.Context, fn ProcessFunc) error {
	stage, err := p.getOrCreateStage()
	if err != nil {
		return err
	}

	return fn(ctx, stage.Input(), stage.Output())
}

// Do is an alias for Run for cleaner syntax.
func (p *Pipeline) Do(ctx context.Context, fn ProcessFunc) error {
	return p.Run(ctx, fn)
}

// getOrCreateStage returns the current stage or creates a new one.
func (p *Pipeline) getOrCreateStage() (*Stage, error) {
	// First call: return the initial stage
	if p.current < len(p.stages) {
		stage := p.stages[p.current]
		p.current++
		return stage, nil
	}

	// Subsequent calls: create new stages
	if len(p.stages) == 0 {
		return nil, fmt.Errorf("no stages in pipeline")
	}

	lastStage := p.stages[len(p.stages)-1]

	// Create input from last stage's output
	nextInput, err := p.createInputFromStage(lastStage)
	if err != nil {
		return nil, fmt.Errorf("failed to create input from previous output: %w", err)
	}

	// Generate temp file for this stage
	tempFile, err := p.generateTempFile()
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}

	// Mark the previous stage's temp file for cleanup
	if lastStage.output != p.finalOut && lastStage.temp != nil {
		p.tempFiles = append(p.tempFiles, lastStage.temp)
	}

	stage := &Stage{
		input:  nextInput,
		output: tempFile.AsStreamWriter(),
		temp:   tempFile,
	}
	p.stages = append(p.stages, stage)
	p.current++

	return stage, nil
}

// createInputFromStage creates a StreamReader from the previous stage's output.
func (p *Pipeline) createInputFromStage(stage *Stage) (StreamReader, error) {
	if stage.temp != nil {
		return stage.temp.AsStreamReader(), nil
	}

	if fsw, ok := stage.output.(*fileStreamWriter); ok {
		return NewFileStreamReader(fsw.path)
	}

	return nil, fmt.Errorf("cannot create input from stage output: unsupported StreamWriter type")
}

// GetResult returns the final output as a StreamReader.
func (p *Pipeline) GetResult() (StreamReader, error) {
	if len(p.stages) == 0 {
		return nil, fmt.Errorf("no stages in pipeline")
	}

	lastStage := p.stages[len(p.stages)-1]
	return p.createInputFromStage(lastStage)
}

// GetResultMeta returns the FileInfo metadata of the final output.
func (p *Pipeline) GetResultMeta() (FileInfo, error) {
	if len(p.stages) == 0 {
		return FileInfo{}, fmt.Errorf("no stages in pipeline")
	}

	lastStage := p.stages[len(p.stages)-1]

	// Get path from the last stage
	var path string
	if lastStage.temp != nil {
		path = lastStage.temp.Path
	} else if fsw, ok := lastStage.output.(*fileStreamWriter); ok {
		path = fsw.path
	} else {
		return FileInfo{}, fmt.Errorf("cannot get metadata: output is not file-based")
	}

	// Get file info
	stat, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, fmt.Errorf("failed to stat output file: %w", err)
	}

	return FileInfo{
		Name:        stat.Name(),
		Size:        stat.Size(),
		ContentType: "", // ContentType not stored in os.FileInfo, leave empty
	}, nil
}

// GetResultPath returns the final output file path (if applicable).
func (p *Pipeline) GetResultPath() (string, error) {
	if len(p.stages) == 0 {
		return "", fmt.Errorf("no stages in pipeline")
	}

	lastStage := p.stages[len(p.stages)-1]

	if lastStage.temp != nil {
		return lastStage.temp.Path, nil
	}

	if fsw, ok := lastStage.output.(*fileStreamWriter); ok {
		return fsw.path, nil
	}

	return "", fmt.Errorf("output is not file-based")
}

// GetStages returns all stages in the pipeline.
func (p *Pipeline) GetStages() []*Stage {
	return p.stages
}

// StageCount returns the number of stages in the pipeline.
func (p *Pipeline) StageCount() int {
	return len(p.stages)
}

// CleanUp removes all temporary files except the final output.
func (p *Pipeline) CleanUp() error {
	var firstErr error

	for _, temp := range p.tempFiles {
		if temp != nil {
			if err := temp.Cleanup(); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("failed to cleanup temp file %s: %w", temp.Path, err)
				}
			}
		}
	}

	if len(p.stages) > 0 {
		lastStage := p.stages[len(p.stages)-1]
		if lastStage.temp != nil && lastStage.output != p.finalOut {
			if err := lastStage.temp.Cleanup(); err != nil {
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}

	return firstErr
}

// CleanUpAll removes ALL files including the final output.
func (p *Pipeline) CleanUpAll() error {
	var firstErr error

	for _, temp := range p.tempFiles {
		if temp != nil {
			if err := temp.Cleanup(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}

	for _, stage := range p.stages {
		if stage.temp != nil {
			if err := stage.temp.Cleanup(); err != nil && firstErr == nil {
				firstErr = err
			}
		} else if fsw, ok := stage.output.(*fileStreamWriter); ok {
			if err := os.Remove(fsw.path); err != nil && !os.IsNotExist(err) && firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
}

// generateTempFile creates a unique temporary file for a stage.
func (p *Pipeline) generateTempFile() (*TempFile, error) {
	ext := ".tmp"
	if fsw, ok := p.finalOut.(*fileStreamWriter); ok {
		if e := filepath.Ext(fsw.path); e != "" {
			ext = e
		}
	}

	prefix := fmt.Sprintf("pipeline-stage%d-", len(p.stages))
	return NewTempFileWithName(prefix, GenerateFileName(ext))
}

// Reset clears all stages except the first one and cleans up temp files.
func (p *Pipeline) Reset() error {
	if err := p.CleanUp(); err != nil {
		return err
	}
	p.stages = p.stages[:1]
	p.tempFiles = make([]*TempFile, 0)
	p.current = 0
	return nil
}
*/

/* ---------- Session Manager (Session-based temp manager) ---------- */

// Session represents one request/job scope and has its own
// subdirectory for temp files.
type Session interface {
	Do(ctx context.Context, outputExt string, fn func(ctx context.Context, w StreamWriter) error, opts ...SessionOption) (*Output, error)
	Release() error
}

// IOManager is the root manager for all temp files.
type IOManager interface {
	NewSession(id string, opts ...SessionOption) Session
	Release() error // cleanup the entire baseDir
}

// OutputType controls how session outputs are written.
type OutputType string

const (
	// OutputTempFile stores results on disk (default).
	OutputTempFile OutputType = "tempfile"
	// OutputBytes keeps results in memory using a bytes buffer.
	OutputBytes OutputType = "bytes"
)

// SessionOption customizes the behavior of Session instances.
type SessionOption struct {
	WriterType OutputType
}

func resolveSessionWriterType(defaultType OutputType, opts []SessionOption) OutputType {
	writerType := defaultType
	if writerType == "" {
		writerType = OutputTempFile
	}
	for _, opt := range opts {
		switch opt.WriterType {
		case OutputTempFile, OutputBytes:
			writerType = opt.WriterType
		}
	}
	return writerType
}

// Concrete implementation of IOManager.
type sessionManager struct {
	baseDir       string
	mu            sync.Mutex
	sessions      map[string]Session
	removeBaseDir bool // true removes baseDir on Release, false keeps it
}

// NewIOManager creates a base temp directory for the whole process.
func NewIOManager(baseDir ...string) (IOManager, error) {
	if len(baseDir) > 0 {
		processBaseDir := strings.TrimSpace(baseDir[0])
		if processBaseDir == "" {
			return nil, fmt.Errorf("NewIOManager: baseDir must not be empty")
		}
		if err := os.MkdirAll(processBaseDir, 0o755); err != nil {
			return nil, err
		}

		return &sessionManager{
			baseDir:       processBaseDir,
			sessions:      make(map[string]Session),
			removeBaseDir: false, // user-provided baseDir → keep on Release
		}, nil

	}

	processBaseDir, err := os.MkdirTemp("", "streamio-")
	if err != nil {
		return nil, err
	}

	return &sessionManager{
		baseDir:       processBaseDir,
		sessions:      make(map[string]Session),
		removeBaseDir: true, // temp dir → safe to delete
	}, nil

}

// processSession is the default implementation of Session.
type processSession struct {
	id       string
	dir      string
	manager  *sessionManager
	tmpFiles []*TempFile
	writer   OutputType
}

// NewSession creates a session under baseDir.
// Generates a UUID if id == "".
func (m *sessionManager) NewSession(id string, opts ...SessionOption) Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	if id == "" {
		id = uuid.New().String()
	}

	sessionDir := filepath.Join(m.baseDir, id)
	_ = os.MkdirAll(sessionDir, 0o755)

	s := &processSession{
		id:       id,
		dir:      sessionDir,
		manager:  m,
		tmpFiles: make([]*TempFile, 0),
		writer:   resolveSessionWriterType(OutputTempFile, opts),
	}

	m.sessions[id] = s
	return s
}

// Release removes every session and the SessionManager base directory.
func (m *sessionManager) Release() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var firstErr error

	// 1) Let every session Release() itself first.
	for _, s := range m.sessions {
		if err := s.Release(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.sessions = make(map[string]Session)

	// 2) Sweep files not tracked by sessions (e.g. kept outputs).
	if m.baseDir != "" {
		if m.removeBaseDir {
			// Temp mode → remove the entire root directory.
			if err := os.RemoveAll(m.baseDir); err != nil && firstErr == nil {
				firstErr = err
			}
		} else {
			// Custom baseDir → delete children but keep the directory.
			entries, err := os.ReadDir(m.baseDir)
			if err != nil && firstErr == nil {
				firstErr = err
			}
			for _, e := range entries {
				childPath := filepath.Join(m.baseDir, e.Name())
				if err := os.RemoveAll(childPath); err != nil && !os.IsNotExist(err) && firstErr == nil {
					firstErr = err
				}
			}
		}
	}

	return firstErr
}

// Do creates a temp file in this session directory and lets fn write it.
// outputExt examples: ".pdf", ".zip".
func (s *processSession) Do(
	ctx context.Context,
	outputExt string,
	fn func(ctx context.Context, w StreamWriter) error,
	opts ...SessionOption,
) (*Output, error) {
	if fn == nil {
		return nil, fmt.Errorf("Session.Do: nil fn")
	}

	writerType := resolveSessionWriterType(s.writer, opts)

	if writerType == OutputBytes {
		return s.doInMemory(ctx, fn)
	}

	// Use GenerateFileName for a unique name with the desired extension.
	filename := GenerateFileName(outputExt)

	tmp, err := NewTempFileInDir(s.dir, "proc-", filename)
	if err != nil {
		return nil, err
	}

	// Track the temp file for cleanup on Release.
	s.tmpFiles = append(s.tmpFiles, tmp)

	// Let the business function write to this file via StreamWriter.
	if err := fn(ctx, tmp.AsStreamWriter()); err != nil {
		_ = tmp.Cleanup()
		return nil, err
	}

	return &Output{tmp: tmp, session: s}, nil
}

func (s *processSession) DoStream(
	ctx context.Context,
	in StreamReader,
	outputExt string,
	fn func(ctx context.Context, r io.Reader, w io.Writer) error,
	opts ...SessionOption,
) (*Output, error) {
	if fn == nil {
		return nil, fmt.Errorf("Session.DoStream: nil fn")
	}

	writerType := resolveSessionWriterType(s.writer, opts)

	if writerType == OutputBytes {
		return s.doStreamInMemory(ctx, in, fn)
	}

	// Use GenerateFileName for a unique name with the desired extension.
	filename := GenerateFileName(outputExt)
	tmp, err := NewTempFileInDir(s.dir, "proc-", filename)
	if err != nil {
		return nil, err
	}

	// Track the temp file for cleanup on Release.
	s.tmpFiles = append(s.tmpFiles, tmp)

	r, err := OpenReader(in)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	w, err := OpenWriter(tmp.AsStreamWriter())
	if err != nil {
		return nil, err
	}
	defer w.Close()

	if err := fn(ctx, r, w); err != nil {
		_ = tmp.Cleanup()
		return nil, err
	}

	return &Output{tmp: tmp, session: s}, nil
}

func (s *processSession) doStreamInMemory(
	ctx context.Context,
	in StreamReader,
	fn func(ctx context.Context, r io.Reader, w io.Writer) error) (*Output, error) {
	sw := NewBytesStreamWriter()

	r, err := OpenReader(in)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	w, err := OpenWriter(sw)
	if err != nil {
		return nil, err
	}
	defer w.Close()

	if err := fn(ctx, r, w); err != nil {
		return nil, err
	}

	data := sw.Bytes()
	result := make([]byte, len(data))
	copy(result, data)

	return &Output{bytes: result, session: s}, nil
}

func (s *processSession) Release() error {
	var firstErr error

	for _, tmp := range s.tmpFiles {
		if tmp != nil {
			if err := tmp.Cleanup(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	s.tmpFiles = nil

	if s.manager != nil {
		s.manager.mu.Lock()
		delete(s.manager.sessions, s.id)
		s.manager.mu.Unlock()
	}

	return firstErr
}

func (s *processSession) unregisterTempFile(target *TempFile) {
	if s == nil || target == nil {
		return
	}

	tmpList := s.tmpFiles
	newList := make([]*TempFile, 0, len(tmpList))

	for _, t := range tmpList {
		if t != target {
			newList = append(newList, t)
		}
	}
	s.tmpFiles = newList
}

func (s *processSession) doInMemory(
	ctx context.Context,
	fn func(ctx context.Context, w StreamWriter) error,
) (*Output, error) {
	writer := NewBytesStreamWriter()

	if err := fn(ctx, writer); err != nil {
		return nil, err
	}

	data := writer.Bytes()
	result := make([]byte, len(data))
	copy(result, data)

	return &Output{bytes: result, session: s}, nil
}

type DownloadReaderCloser interface {
	io.Reader
	io.Closer
}

type downloadReaderCloser struct {
	streamReader StreamReader
	reader       Reader
	Cleanup      func()
}

func (d *downloadReaderCloser) Read(p []byte) (int, error) {
	if d.reader == nil {
		return 0, io.ErrClosedPipe
	}
	return d.reader.Read(p)
}

func (d *downloadReaderCloser) Close() error {
	var errs error

	if d.reader != nil {
		readerCloseErr := d.reader.Close()
		if readerCloseErr != nil {
			errs = errors.Join(errs, readerCloseErr)
		}
		d.reader = nil
	}

	if d.streamReader != nil {
		streamReaderCleanupErr := d.streamReader.Cleanup()
		if streamReaderCleanupErr != nil {
			errs = errors.Join(errs, streamReaderCleanupErr)
		}

		d.streamReader = nil
	}

	if d.Cleanup != nil {
		d.Cleanup()
		d.Cleanup = nil
	}

	return errs
}

func NewDownloadReaderCloser(streamReader StreamReader, cleanup ...func()) (DownloadReaderCloser, error) {
	if streamReader == nil {
		return nil, fmt.Errorf("nil streamReader")
	}

	reader, err := OpenReader(streamReader)
	if err != nil {
		return nil, err
	}

	closer := &downloadReaderCloser{
		streamReader: streamReader,
		reader:       reader,
	}

	if len(cleanup) > 0 {
		closer.Cleanup = cleanup[0]
	}

	return closer, nil
}
