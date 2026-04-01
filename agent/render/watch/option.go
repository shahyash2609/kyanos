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
	MaxRecordContentDisplayBytes int
	MaxRecords                   int
	TraceDevEvent                bool
	TraceSocketEvent             bool
	TraceSslEvent                bool

	// GCS rolling-file upload options.
	// When GCSBucket is non-empty, records are written to a local rolling file
	// and uploaded to GCS every GCSUploadInterval under:
	//   gs://{GCSBucket}/{GCSServiceName}/{GCSDeploymentID}/primary/{date}/{ts}.jsonl
	GCSBucket         string
	GCSServiceName    string
	GCSDeploymentID   string
	GCSUploadInterval time.Duration
	GCSCredentials    string // path to service-account JSON; empty = Application Default Credentials
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
}

func (w *WatchOptions) UseTui() bool {
	return !w.DebugOutput && w.JsonOutput == "" && w.GCSBucket == ""
}
