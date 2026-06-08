package watch

import (
	"strings"
	"time"
)

type WatchOptions struct {
	WideOutput                   bool
	StaticRecord                 bool
	Opts                         string
	DebugOutput                  bool
	JsonOutput                   string
	JsonOutputDir                string // per-pod JSONL output directory (writes <dir>/<pod_name>.json)
	AutoReflect                  bool   // auto-discover listening ports and probe gRPC reflection
	MaxRecordContentDisplayBytes int
	MaxRecords                   int
	TraceDevEvent                bool
	TraceSocketEvent             bool
	TraceSslEvent                bool

	// GCS rolling-file upload options.
	// When GCSBucket is non-empty, records are written to a local rolling file
	// and uploaded to GCS every GCSUploadInterval under:
	//   gs://{GCSBucket}/{GCSServiceName}/{GCSDeploymentID}/primary/{date}/{ts}.json
	GCSBucket         string
	GCSServiceName    string
	GCSDeploymentID   string
	GCSUploadInterval time.Duration
	GCSCredentials    string // path to service-account JSON; empty = Application Default Credentials
	GCSBufferSize     int64  // per-pod buffer size in bytes before uploading to GCS (used with --json-output-dir)
}

func (w *WatchOptions) Init() {
	if w.Opts != "" {
		if strings.Contains(w.Opts, "wide") {
			w.WideOutput = true
		}
	}
	if w.MaxRecordContentDisplayBytes <= 0 {
		w.MaxRecordContentDisplayBytes = 1024
	}
	if w.MaxRecords <= 0 {
		w.MaxRecords = 100
	}
	if w.GCSUploadInterval <= 0 {
		w.GCSUploadInterval = 3 * time.Minute
	}
	if w.GCSBufferSize <= 0 {
		w.GCSBufferSize = 10 * 1024 * 1024 // 10 MB
	}
}

func (w *WatchOptions) UseTui() bool {
	return !w.DebugOutput && w.JsonOutput == "" && w.GCSBucket == "" && w.JsonOutputDir == ""
}
