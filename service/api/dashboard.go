package api

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"
)

const (
	dashboardRoutePrefix               = "/dashboard/"
	dashboardEtagFileName              = ".etag"
	defaultDashboardURL                = "https://github.com/SagerNet/sing-box-dashboard/archive/refs/heads/gh-pages.zip"
	dashboardArchiveMaxCompressedBytes = int64(32 << 20)
	dashboardArchiveMaxEntries         = 4096
	dashboardArchiveMaxEntryBytes      = uint64(16 << 20)
	dashboardArchiveMaxExtractedBytes  = uint64(128 << 20)
)

type dashboardStatus int

const (
	dashboardEmpty dashboardStatus = iota
	dashboardManaged
	dashboardUserProvided
)

var dashboardGenerationCounter atomic.Uint64

var dashboardPublicationOwners = struct {
	sync.Mutex
	paths map[string]uint64
}{paths: make(map[string]uint64)}

type dashboard struct {
	ctx            context.Context
	cancel         context.CancelFunc
	logger         log.ContextLogger
	options        option.APIDashboardOptions
	path           string
	url            string
	updateInterval time.Duration
	fileServer     http.Handler
	builtIn        bool
	httpClient     *http.Client
	lastEtag       string
	lastUpdated    time.Time
	generation     uint64
	workers        sync.WaitGroup
	closeOnce      sync.Once
	claimed        bool
}

func newDashboard(ctx context.Context, logger log.ContextLogger, options option.APIDashboardOptions) *dashboard {
	ctx, cancel := context.WithCancel(ctx)
	generation := dashboardGenerationCounter.Add(1)
	builtIn := options.Path == "" && options.DownloadURL == ""
	if builtIn {
		return &dashboard{
			ctx:        ctx,
			cancel:     cancel,
			logger:     logger,
			options:    options,
			fileServer: http.StripPrefix(dashboardRoutePrefix, newBuiltInDashboardHandler()),
			builtIn:    true,
			generation: generation,
		}
	}
	path := options.Path
	if path == "" {
		path = "dashboard"
	}
	path = filepath.Clean(filemanager.BasePath(ctx, os.ExpandEnv(path)))
	url := options.DownloadURL
	if url == "" {
		url = defaultDashboardURL
	}
	updateInterval := 24 * time.Hour
	if options.UpdateInterval > 0 {
		updateInterval = time.Duration(options.UpdateInterval)
	}
	return &dashboard{
		ctx:            ctx,
		cancel:         cancel,
		logger:         logger,
		options:        options,
		path:           path,
		url:            url,
		updateInterval: updateInterval,
		fileServer:     http.StripPrefix(dashboardRoutePrefix, http.FileServer(dashboardDir(path))),
		generation:     generation,
	}
}

func (d *dashboard) start() error {
	if d.builtIn {
		return nil
	}
	_, err := filemanager.ReadDir(d.ctx, d.path)
	if err != nil && !os.IsNotExist(err) {
		return E.Cause(err, "read dashboard directory")
	}
	transport, err := d.resolveTransport()
	if err != nil {
		return E.Cause(err, "create dashboard http client")
	}
	d.httpClient = &http.Client{Transport: transport}
	d.claimPublication()
	d.workers.Add(1)
	go func() {
		defer d.workers.Done()
		d.loopUpdate()
	}()
	return nil
}

func (d *dashboard) close() error {
	d.closeOnce.Do(func() {
		d.cancel()
		if d.httpClient != nil {
			d.httpClient.CloseIdleConnections()
		}
		d.workers.Wait()
		d.releasePublication()
	})
	return nil
}

func (d *dashboard) claimPublication() {
	if d.builtIn {
		return
	}
	dashboardPublicationOwners.Lock()
	dashboardPublicationOwners.paths[d.path] = d.generation
	d.claimed = true
	dashboardPublicationOwners.Unlock()
}

func (d *dashboard) releasePublication() {
	if d.builtIn {
		return
	}
	dashboardPublicationOwners.Lock()
	if d.claimed && dashboardPublicationOwners.paths[d.path] == d.generation {
		delete(dashboardPublicationOwners.paths, d.path)
	}
	d.claimed = false
	dashboardPublicationOwners.Unlock()
}

func (d *dashboard) resolveTransport() (adapter.HTTPTransport, error) {
	httpClientManager := service.FromContext[adapter.HTTPClientManager](d.ctx)
	if httpClientManager == nil {
		return nil, E.New("missing http client manager in context")
	}
	if d.options.HTTPClient != nil && !d.options.HTTPClient.IsEmpty() {
		return httpClientManager.ResolveTransport(d.ctx, d.logger, *d.options.HTTPClient)
	}
	defaultTransport := httpClientManager.DefaultTransport()
	if defaultTransport == nil {
		return nil, E.New("default http client transport is not initialized")
	}
	return defaultTransport, nil
}

func (d *dashboard) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, dashboardRoutePrefix) {
		d.fileServer.ServeHTTP(writer, request)
		return
	}
	http.Redirect(writer, request, dashboardRoutePrefix, http.StatusFound)
}

func (d *dashboard) loopUpdate() {
	status := d.loadState()
	if status == dashboardUserProvided {
		d.logger.Info("dashboard: serving user-provided files at ", d.path, ", auto-update disabled")
		return
	}
	var nextUpdate time.Time
	if status == dashboardManaged {
		nextUpdate = d.lastUpdated.Add(d.updateInterval)
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-timer.C:
		}
		now := time.Now()
		if !now.Before(nextUpdate) {
			err := d.fetch(d.ctx)
			if err != nil {
				if d.ctx.Err() != nil || errors.Is(err, os.ErrClosed) {
					return
				}
				d.logger.Error(E.Cause(err, "update dashboard"))
				nextUpdate = now.Add(d.updateInterval)
			} else {
				nextUpdate = d.lastUpdated.Add(d.updateInterval)
			}
		}
		timer.Reset(max(time.Until(nextUpdate), 0))
	}
}

func (d *dashboard) loadState() dashboardStatus {
	entries, err := filemanager.ReadDir(d.ctx, d.path)
	if err != nil {
		return dashboardEmpty
	}
	if len(entries) == 0 {
		return dashboardEmpty
	}
	etagPath := filepath.Join(d.path, dashboardEtagFileName)
	etagBytes, err := filemanager.ReadFile(d.ctx, etagPath)
	if err != nil {
		return dashboardUserProvided
	}
	d.lastEtag = strings.TrimSpace(string(etagBytes))
	info, err := filemanager.Stat(d.ctx, etagPath)
	if err == nil {
		d.lastUpdated = info.ModTime()
	}
	return dashboardManaged
}

func (d *dashboard) fetch(ctx context.Context) error {
	d.logger.Info("updating dashboard from URL: ", d.url)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return err
	}
	if d.lastEtag != "" {
		request.Header.Set("If-None-Match", d.lastEtag)
	}
	defer d.httpClient.CloseIdleConnections()
	response, err := d.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotModified:
		d.lastUpdated = time.Now()
		err = d.writeUpdateTime()
		if err != nil {
			d.logger.Warn(E.Cause(err, "save dashboard update time"))
			if errors.Is(err, os.ErrClosed) || d.ctx.Err() != nil {
				return err
			}
		}
		d.logger.Info("dashboard: not modified")
		return nil
	default:
		return E.New("unexpected status: ", response.Status)
	}
	etag := response.Header.Get("Etag")
	if err = d.extract(response.Body, etag); err != nil {
		return err
	}
	d.lastEtag = etag
	d.lastUpdated = time.Now()
	d.logger.Info("dashboard: updated")
	return nil
}

func (d *dashboard) extract(body io.Reader, etag string) error {
	tempFile, err := filemanager.CreateTemp(d.ctx, fmt.Sprintf("sidera-dashboard-%d-*.zip", d.generation))
	if err != nil {
		return err
	}
	tempZipPath := tempFile.Name()
	defer filemanager.Remove(context.WithoutCancel(d.ctx), tempZipPath)
	written, copyErr := copyWithContext(d.ctx, tempFile, io.LimitReader(body, dashboardArchiveMaxCompressedBytes+1))
	if copyErr == nil && written > dashboardArchiveMaxCompressedBytes {
		copyErr = E.New("dashboard archive exceeds compressed size limit")
	}
	if copyErr == nil {
		copyErr = tempFile.Sync()
	}
	closeErr := tempFile.Close()
	if copyErr != nil || closeErr != nil {
		return errors.Join(copyErr, closeErr)
	}

	reader, err := zip.OpenReader(tempZipPath)
	if err != nil {
		return err
	}
	defer reader.Close()
	if err = validateDashboardArchive(reader.File); err != nil {
		return err
	}

	tempDir, err := d.createGenerationDirectory("extract")
	if err != nil {
		return err
	}
	defer filemanager.RemoveAll(context.WithoutCancel(d.ctx), tempDir)
	trimDir := zipIsInSingleDirectory(reader.File)
	seenPaths := make(map[string]bool, len(reader.File))
	var extractedBytes uint64
	for _, file := range reader.File {
		if err = d.ctx.Err(); err != nil {
			return err
		}
		if file.FileInfo().IsDir() {
			continue
		}
		pathElements := strings.Split(file.Name, "/")
		if trimDir {
			pathElements = pathElements[1:]
		}
		if len(pathElements) == 0 {
			continue
		}
		relativePath := filepath.Join(pathElements...)
		if !filepath.IsLocal(relativePath) {
			return E.New("invalid dashboard archive entry: ", file.Name)
		}
		if seenPaths[relativePath] {
			return E.New("duplicate dashboard archive entry: ", file.Name)
		}
		seenPaths[relativePath] = true
		savePath := filepath.Join(tempDir, relativePath)
		if err = filemanager.MkdirAll(d.ctx, filepath.Dir(savePath), 0o755); err != nil {
			return err
		}
		remaining := dashboardArchiveMaxExtractedBytes - extractedBytes
		entryLimit := min(dashboardArchiveMaxEntryBytes, remaining)
		var written uint64
		written, err = extractZipEntry(d.ctx, file, savePath, entryLimit)
		if err != nil {
			if entryLimit < dashboardArchiveMaxEntryBytes {
				return E.Cause(err, "dashboard archive exceeds extracted size limit")
			}
			return err
		}
		extractedBytes += written
	}
	if err = filemanager.WriteFile(d.ctx, filepath.Join(tempDir, dashboardEtagFileName), []byte(etag), 0o644); err != nil {
		return err
	}
	return d.publishDirectory(tempDir)
}

func validateDashboardArchive(files []*zip.File) error {
	if len(files) > dashboardArchiveMaxEntries {
		return E.New("dashboard archive has too many entries")
	}
	var aggregate uint64
	for _, file := range files {
		info := file.FileInfo()
		if info.IsDir() {
			continue
		}
		if info.Mode()&os.ModeType != 0 {
			return E.New("unsupported dashboard archive entry: ", file.Name)
		}
		if file.UncompressedSize64 > dashboardArchiveMaxEntryBytes {
			return E.New("dashboard archive entry exceeds size limit: ", file.Name)
		}
		if file.UncompressedSize64 > dashboardArchiveMaxExtractedBytes-aggregate {
			return E.New("dashboard archive exceeds extracted size limit")
		}
		aggregate += file.UncompressedSize64
	}
	return nil
}

func (d *dashboard) writeUpdateTime() error {
	dashboardPublicationOwners.Lock()
	defer dashboardPublicationOwners.Unlock()
	if err := d.ctx.Err(); err != nil {
		return err
	}
	if !d.claimed || dashboardPublicationOwners.paths[d.path] != d.generation {
		return os.ErrClosed
	}
	return filemanager.WriteFile(d.ctx, filepath.Join(d.path, dashboardEtagFileName), []byte(d.lastEtag), 0o644)
}

func (d *dashboard) createGenerationDirectory(kind string) (string, error) {
	parent := filepath.Dir(d.path)
	if parent != "." {
		if err := filemanager.MkdirAll(d.ctx, parent, 0o755); err != nil {
			return "", err
		}
	}
	for attempt := 0; attempt < 16; attempt++ {
		path, err := d.generationPath(kind)
		if err != nil {
			return "", err
		}
		err = filemanager.Mkdir(d.ctx, path, 0o755)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return path, err
	}
	return "", E.New("allocate unique dashboard extraction directory")
}

func (d *dashboard) generationPath(kind string) (string, error) {
	var nonce [10]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	name := fmt.Sprintf(".%s.%s-%d-%s", filepath.Base(d.path), kind, d.generation, hex.EncodeToString(nonce[:]))
	return filepath.Join(filepath.Dir(d.path), name), nil
}

func (d *dashboard) publishDirectory(tempDir string) error {
	dashboardPublicationOwners.Lock()
	defer dashboardPublicationOwners.Unlock()
	if err := d.ctx.Err(); err != nil {
		return err
	}
	if !d.claimed || dashboardPublicationOwners.paths[d.path] != d.generation {
		return os.ErrClosed
	}
	backupDir, err := d.generationPath("backup")
	if err != nil {
		return err
	}
	_, statErr := filemanager.Stat(d.ctx, d.path)
	hasCurrent := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	if hasCurrent {
		if err = filemanager.Rename(d.ctx, d.path, backupDir); err != nil {
			return err
		}
	}
	if err = d.ctx.Err(); err == nil {
		err = filemanager.Rename(d.ctx, tempDir, d.path)
	}
	if err != nil {
		if hasCurrent {
			err = errors.Join(err, filemanager.Rename(context.WithoutCancel(d.ctx), backupDir, d.path))
		}
		return err
	}
	if hasCurrent {
		_ = filemanager.RemoveAll(context.WithoutCancel(d.ctx), backupDir)
	}
	return nil
}

func extractZipEntry(ctx context.Context, file *zip.File, savePath string, maxBytes uint64) (uint64, error) {
	reader, err := file.Open()
	if err != nil {
		return 0, err
	}
	defer reader.Close()
	writer, err := filemanager.Create(ctx, savePath)
	if err != nil {
		return 0, err
	}
	written, copyErr := copyWithContext(ctx, writer, io.LimitReader(reader, int64(maxBytes)+1))
	if copyErr == nil && uint64(written) > maxBytes {
		copyErr = E.New("dashboard archive entry exceeds size limit: ", file.Name)
	}
	closeErr := writer.Close()
	if copyErr != nil || closeErr != nil {
		_ = filemanager.Remove(context.WithoutCancel(ctx), savePath)
		return 0, errors.Join(copyErr, closeErr)
	}
	return uint64(written), nil
}

func copyWithContext(ctx context.Context, writer io.Writer, reader io.Reader) (int64, error) {
	buffer := make([]byte, 32<<10)
	var written int64
	emptyReads := 0
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		readCount, readErr := reader.Read(buffer)
		if readCount > 0 {
			emptyReads = 0
			writeCount, writeErr := writer.Write(buffer[:readCount])
			written += int64(writeCount)
			if writeErr != nil {
				return written, writeErr
			}
			if writeCount != readCount {
				return written, io.ErrShortWrite
			}
		} else if readErr == nil {
			emptyReads++
			if emptyReads >= 100 {
				return written, io.ErrNoProgress
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return written, nil
			}
			return written, readErr
		}
	}
}

// GitHub archives wrap every file under a single "<repo>-<branch>/" top-level directory.
func zipIsInSingleDirectory(files []*zip.File) bool {
	var dirName string
	for _, file := range files {
		if file.FileInfo().IsDir() {
			continue
		}
		pathElements := strings.Split(file.Name, "/")
		if len(pathElements) < 2 {
			return false
		}
		if dirName == "" {
			dirName = pathElements[0]
		} else if dirName != pathElements[0] {
			return false
		}
	}
	return dirName != ""
}
