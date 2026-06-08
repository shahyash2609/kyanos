package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	c "kyanos/common"
)

// podBuffer tracks a single pod's local buffer file and its current size.
type podBuffer struct {
	mu          sync.Mutex
	file        *os.File
	size        int64
	windowStart time.Time
}

// GCSDirUploader manages per-pod local buffer files and uploads each to GCS
// when the buffer reaches a size threshold or a time interval elapses.
//
// GCS path: gs://{bucket}/{prefix}/{pod-name}/{YYYY-MM-DD}/{ts}.json
// where prefix = "{GCSServiceName}/{GCSDeploymentID}".
type GCSDirUploader struct {
	bucket     string
	prefix     string // "{serviceName}/{deploymentID}"
	bufferSize int64
	interval   time.Duration
	localDir   string
	client     *storage.Client

	mu      sync.RWMutex
	buffers map[string]*podBuffer

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// NewGCSDirUploader creates a per-pod GCS directory uploader.
func NewGCSDirUploader(parentCtx context.Context, opts WatchOptions) (*GCSDirUploader, error) {
	if err := os.MkdirAll(opts.JsonOutputDir, 0755); err != nil {
		return nil, fmt.Errorf("create buffer dir %s: %w", opts.JsonOutputDir, err)
	}

	client, err := newGCSClient(context.Background(), opts.GCSCredentials)
	if err != nil {
		return nil, fmt.Errorf("create GCS client: %w", err)
	}

	prefix := opts.GCSServiceName
	if opts.GCSDeploymentID != "" {
		prefix = fmt.Sprintf("%s/%s", opts.GCSServiceName, opts.GCSDeploymentID)
	}

	ctx, cancel := context.WithCancel(parentCtx)
	u := &GCSDirUploader{
		bucket:     opts.GCSBucket,
		prefix:     prefix,
		bufferSize: opts.GCSBufferSize,
		interval:   opts.GCSUploadInterval,
		localDir:   opts.JsonOutputDir,
		client:     client,
		buffers:    make(map[string]*podBuffer),
		ctx:        ctx,
		cancel:     cancel,
		done:       make(chan struct{}),
	}

	go u.timerLoop()
	return u, nil
}

// Write appends data to the buffer for the given pod. If the buffer exceeds
// the size threshold, it is rotated and uploaded to GCS.
func (u *GCSDirUploader) Write(podName string, data []byte) error {
	buf := u.getOrCreateBuffer(podName)

	buf.mu.Lock()
	if buf.file == nil {
		buf.mu.Unlock()
		return fmt.Errorf("buffer closed for pod %s", podName)
	}
	n, err := buf.file.Write(data)
	buf.size += int64(n)
	needsRotate := buf.size >= u.bufferSize
	buf.mu.Unlock()

	if err != nil {
		return err
	}
	if needsRotate {
		u.rotatePod(podName, buf, true)
	}
	return nil
}

// Flush stops the timer loop and uploads all remaining buffers.
func (u *GCSDirUploader) Flush() {
	u.cancel()
	<-u.done

	u.mu.RLock()
	pods := make([]string, 0, len(u.buffers))
	for name := range u.buffers {
		pods = append(pods, name)
	}
	u.mu.RUnlock()

	for _, name := range pods {
		u.mu.RLock()
		buf := u.buffers[name]
		u.mu.RUnlock()
		u.rotatePod(name, buf, false)
	}

	u.client.Close()
}

func (u *GCSDirUploader) getOrCreateBuffer(podName string) *podBuffer {
	u.mu.RLock()
	buf, ok := u.buffers[podName]
	u.mu.RUnlock()
	if ok {
		return buf
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	// Double-check after acquiring write lock.
	if buf, ok = u.buffers[podName]; ok {
		return buf
	}

	buf = &podBuffer{}
	name := podName
	if name == "" {
		name = "unknown"
	}
	fpath := filepath.Join(u.localDir, fmt.Sprintf("%s-%d.json", name, time.Now().UnixNano()))
	f, err := os.Create(fpath)
	if err != nil {
		c.AgentLog.Errorf("Failed to create buffer file for pod %s: %v", name, err)
		// Return the buffer without a file; writes will fail gracefully.
		u.buffers[podName] = buf
		return buf
	}
	buf.file = f
	buf.size = 0
	buf.windowStart = time.Now().UTC()
	u.buffers[podName] = buf
	c.AgentLog.Infof("Per-pod GCS buffer: %s -> %s", name, fpath)
	return buf
}

// timerLoop periodically rotates all pod buffers that have data.
func (u *GCSDirUploader) timerLoop() {
	defer close(u.done)

	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()

	for {
		select {
		case <-u.ctx.Done():
			return
		case <-ticker.C:
			u.rotateAll()
		}
	}
}

// rotateAll rotates every pod buffer that has accumulated data.
func (u *GCSDirUploader) rotateAll() {
	u.mu.RLock()
	pods := make([]string, 0, len(u.buffers))
	for name := range u.buffers {
		pods = append(pods, name)
	}
	u.mu.RUnlock()

	for _, name := range pods {
		u.mu.RLock()
		buf := u.buffers[name]
		u.mu.RUnlock()

		buf.mu.Lock()
		hasData := buf.size > 0
		buf.mu.Unlock()

		if hasData {
			u.rotatePod(name, buf, true)
		}
	}
}

// rotatePod swaps out a pod's buffer file, uploads it to GCS, and optionally
// opens a new buffer. When openNext is true the new buffer is installed before
// the upload starts so that incoming writes never see a nil file.
func (u *GCSDirUploader) rotatePod(podName string, buf *podBuffer, openNext bool) {
	name := podName
	if name == "" {
		name = "unknown"
	}

	// Prepare the replacement file before taking the lock so the swap is instant.
	var newFile *os.File
	if openNext {
		fpath := filepath.Join(u.localDir, fmt.Sprintf("%s-%d.json", name, time.Now().UnixNano()))
		var err error
		newFile, err = os.Create(fpath)
		if err != nil {
			c.AgentLog.Errorf("Failed to create next buffer for pod %s: %v", name, err)
			return // keep writing to the current file rather than losing data
		}
	}

	buf.mu.Lock()
	oldFile := buf.file
	windowStart := buf.windowStart
	// Swap immediately so writes continue to the new file.
	buf.file = newFile
	buf.size = 0
	if newFile != nil {
		buf.windowStart = time.Now().UTC()
	}
	buf.mu.Unlock()

	if oldFile == nil {
		return
	}
	oldFile.Close()

	info, err := os.Stat(oldFile.Name())
	if err == nil && info.Size() > 0 {
		prefix := fmt.Sprintf("%s/%s", u.prefix, name)
		objName := gcsObjectName(prefix, windowStart)
		if err := gcsUploadFile(u.client, u.bucket, oldFile.Name(), objName); err != nil {
			c.AgentLog.Errorf("GCS upload failed (%s): %v", objName, err)
		} else {
			c.AgentLog.Infof("GCS upload OK: gs://%s/%s (%d bytes)", u.bucket, objName, info.Size())
		}
	}
	os.Remove(oldFile.Name())
}
