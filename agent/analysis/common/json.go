package common

import (
	"encoding/json"
	"kyanos/bpf"
	"time"
)

type annotatedRecordAlias struct {
	StartTime     string  `json:"start_time"`
	EndTime       string  `json:"end_time"`
	Protocol      string  `json:"protocol"`
	Side          string  `json:"side"`
	PodName       string  `json:"pod_name,omitempty"`
	LocalAddr     string  `json:"local_addr"`
	LocalPort     uint16  `json:"local_port"`
	RemoteAddr    string  `json:"remote_addr"`
	RemotePort    uint16  `json:"remote_port"`
	Pid           uint32  `json:"pid"`
	IsSsl         bool    `json:"is_ssl"`
	TotalDuration float64 `json:"total_duration_ms"`
	ReqSize       int     `json:"req_size_bytes"`
	RespSize      int     `json:"resp_size_bytes"`
	Request       string  `json:"request"`
	Response      string  `json:"response"`
}

// MarshalJSON implements custom JSON marshaling for AnnotatedRecord
func (r *AnnotatedRecord) MarshalJSON() ([]byte, error) {
	return json.Marshal(&annotatedRecordAlias{
		StartTime:     time.Unix(0, int64(r.StartTs)).Format(time.RFC3339Nano),
		EndTime:       time.Unix(0, int64(r.EndTs)).Format(time.RFC3339Nano),
		Protocol:      bpf.ProtocolNamesMap[bpf.AgentTrafficProtocolT(r.ConnDesc.Protocol)],
		Side:          r.ConnDesc.Side.String(),
		PodName:       r.ConnDesc.PodName,
		LocalAddr:     r.ConnDesc.LocalAddr.String(),
		LocalPort:     uint16(r.ConnDesc.LocalPort),
		RemoteAddr:    r.ConnDesc.RemoteAddr.String(),
		RemotePort:    uint16(r.ConnDesc.RemotePort),
		Pid:           r.ConnDesc.Pid,
		IsSsl:         r.ConnDesc.IsSsl,
		TotalDuration: r.GetTotalDurationMills(),
		ReqSize:       r.ReqSize,
		RespSize:      r.RespSize,
		Request:       r.Req.FormatToString(),
		Response:      r.Resp.FormatToString(),
	})
}
