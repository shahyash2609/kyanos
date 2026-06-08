package watch

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	c "kyanos/common"
	"google.golang.org/api/option"
)

// GCSUploader writes JSON-line records to a local rolling temp file and uploads
// completed windows to GCS under:
//
//	gs://{bucket}/{serviceName}/{deploymentID}/primary/{YYYY-MM-DD}/{window-start-ts}.json
//
// A new window starts immediately after each upload. On shutdown the current
// (partial) window is uploaded so no data is lost.
type GCSUploader struct {
	bucket   string
	prefix   string // "{serviceName}/{deploymentID}/primary"
	interval time.Duration
	client   *storage.Client

	mu          sync.Mutex
	currentFile *os.File
	windowStart time.Time

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// NewGCSUploader creates an uploader and starts the background rolling loop.
func NewGCSUploader(parentCtx context.Context, opts WatchOptions) (*GCSUploader, error) {
	prefix := fmt.Sprintf("%s/%s/primary", opts.GCSServiceName, opts.GCSDeploymentID)

	client, err := newGCSClient(parentCtx, opts.GCSCredentials)
	if err != nil {
		return nil, fmt.Errorf("create GCS client: %w", err)
	}

	ctx, cancel := context.WithCancel(parentCtx)
	u := &GCSUploader{
		bucket:   opts.GCSBucket,
		prefix:   prefix,
		interval: opts.GCSUploadInterval,
		client:   client,
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}

	if err := u.openNewFile(); err != nil {
		cancel()
		client.Close()
		return nil, fmt.Errorf("open initial rolling file: %w", err)
	}

	go u.rollingLoop()
	return u, nil
}

// Write appends data to the current rolling file. Safe for concurrent use.
func (u *GCSUploader) Write(data []byte) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.currentFile == nil {
		return fmt.Errorf("uploader is closed")
	}
	_, err := u.currentFile.Write(data)
	return err
}

// Flush stops the rolling loop, uploads the final partial window, and closes
// the GCS client. Call this once on shutdown.
func (u *GCSUploader) Flush() {
	u.cancel()
	<-u.done
	u.client.Close()
}

// rollingLoop ticks every interval and rotates the file. On context cancel it
// does a final rotate (uploads the partial window) and exits.
func (u *GCSUploader) rollingLoop() {
	defer close(u.done)

	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()

	for {
		select {
		case <-u.ctx.Done():
			u.rotate(false) // final upload, don't open a new file
			return
		case <-ticker.C:
			u.rotate(true) // periodic upload, open a new file afterwards
		}
	}
}

// rotate swaps out the current file, uploads it to GCS, and optionally opens a
// new file for the next window.
func (u *GCSUploader) rotate(openNext bool) {
	// Swap under lock so writes to the old file finish before we close it.
	u.mu.Lock()
	f := u.currentFile
	windowStart := u.windowStart
	u.currentFile = nil
	u.mu.Unlock()

	if f == nil {
		return
	}
	f.Close()

	info, err := os.Stat(f.Name())
	if err == nil && info.Size() > 0 {
		objName := u.objectName(windowStart)
		if err := u.uploadFile(f.Name(), objName); err != nil {
			c.AgentLog.Errorf("GCS upload failed (%s): %v", objName, err)
		} else {
			c.AgentLog.Infof("GCS upload OK: gs://%s/%s (%d bytes)", u.bucket, objName, info.Size())
		}
	}
	os.Remove(f.Name())

	if !openNext {
		return
	}

	u.mu.Lock()
	if err := u.openNewFile(); err != nil {
		c.AgentLog.Errorf("Failed to open new rolling file: %v", err)
	}
	u.mu.Unlock()
}

func (u *GCSUploader) openNewFile() error {
	f, err := os.CreateTemp("", "kyanos-*.json")
	if err != nil {
		return err
	}
	u.currentFile = f
	u.windowStart = time.Now().UTC()
	return nil
}

// objectName returns the GCS object path for a given window start time.
//
//	{prefix}/{YYYY-MM-DD}/{YYYY-MM-DDTHH-MM-SSZ}.json
func (u *GCSUploader) objectName(t time.Time) string {
	return gcsObjectName(u.prefix, t)
}

// uploadFile uploads a local file to GCS. Uses a fresh background context so
// the upload completes even if the parent context has been cancelled.
func (u *GCSUploader) uploadFile(localPath, objName string) error {
	return gcsUploadFile(u.client, u.bucket, localPath, objName)
}

// gcsObjectName builds a GCS object path: {prefix}/{YYYY-MM-DD}/{YYYY-MM-DDTHH-MM-SSZ}.json
func gcsObjectName(prefix string, t time.Time) string {
	date := t.Format("2006-01-02")
	ts := t.Format("2006-01-02T15-04-05Z")
	return fmt.Sprintf("%s/%s/%s.json", prefix, date, ts)
}

// gcsUploadFile uploads a local file to GCS. Uses a fresh background context so
// the upload completes even if the parent context has been cancelled.
func gcsUploadFile(client *storage.Client, bucket, localPath, objName string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	uploadCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	w := client.Bucket(bucket).Object(objName).NewWriter(uploadCtx)
	if _, err := io.Copy(w, f); err != nil {
		w.Close()
		return fmt.Errorf("copy to GCS: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close GCS writer: %w", err)
	}
	return nil
}

// newGCSClient creates a GCS storage client with optional credentials.
func newGCSClient(ctx context.Context, credentials string) (*storage.Client, error) {
	var clientOpts []option.ClientOption
	if credentials != "" {
		clientOpts = append(clientOpts, option.WithCredentialsFile(credentials))
	}
	return storage.NewClient(ctx, clientOpts...)
}
