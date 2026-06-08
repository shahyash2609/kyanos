package k8s

import (
	"context"
	"errors"
	"fmt"
	"kyanos/agent/metadata/types"
	"kyanos/common"
	"os"
	"strings"
	"time"

	cri "k8s.io/cri-api/pkg/apis"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	"k8s.io/kubernetes/pkg/kubelet/cri/remote"
)

var DefaultRuntimeEndpoints = []string{
	"unix:///var/run/dockershim.sock",
	"unix:///var/run/cri-dockerd.sock",
	"unix:///run/crio/crio.sock",
	"unix:///run/containerd/containerd.sock",
}

const defaultTimeout = 2 * time.Second

type MetaData struct {
	res cri.RuntimeService
}

func NewMetaData(criRuntimeEndpoint string) (*MetaData, error) {
	res, errs := getRuntimeService(criRuntimeEndpoint)
	if len(errs) > 0 {
		return nil, fmt.Errorf("skip kubernetes integration due to [%s]", formatErrors(errs))
	}

	return &MetaData{
		res: res,
	}, nil
}

func (m *MetaData) GetPodByContainer(c types.Container) types.Pod {
	p := types.Pod{}
	p.LoadFromContainer(c)
	if m.res != nil {
		tmp := m.GetPodByName(context.TODO(), p.Name, p.Namespace)
		p.Labels = tmp.Labels
		p.Annotations = tmp.Annotations
	}
	return p
}

func (m *MetaData) GetPodByName(ctx context.Context, name, namespace string) (p types.Pod) {
	if m.res == nil {
		return
	}
	sandboxes, err := m.res.ListPodSandbox(nil)
	if err != nil {
		// TODO: use errors.Is
		if strings.Contains(err.Error(), "Unimplemented") &&
			strings.Contains(err.Error(), "v1alpha2.RuntimeService") {

			common.DefaultLog.Infof("list pod sandbox failed: %s", err)
		} else {
			common.DefaultLog.Errorf("list pod sandbox failed: %s", err)
		}
		return
	}
	for _, sandbox := range sandboxes {
		if sandbox.Metadata.Name != name || sandbox.Metadata.Namespace != namespace {
			continue
		}
		p.Labels = tidyLabels(sandbox.Labels)
		p.Annotations = sandbox.Annotations
		break
	}
	return p
}

// PodIPMap returns a map of "namespace/name" -> primary IP for all READY pod sandboxes.
func (m *MetaData) PodIPMap() map[string]string {
	if m.res == nil {
		return nil
	}
	sandboxes, err := m.res.ListPodSandbox(nil)
	if err != nil {
		common.AgentLog.Debugf("auto-reflect: list pod sandbox failed: %v", err)
		return nil
	}
	result := make(map[string]string)
	for _, sb := range sandboxes {
		if sb.State != runtimeapi.PodSandboxState_SANDBOX_READY {
			continue
		}
		resp, err := m.res.PodSandboxStatus(sb.Id, false)
		if err != nil || resp.Status == nil || resp.Status.Network == nil {
			continue
		}
		ip := resp.Status.Network.Ip
		if ip == "" {
			continue
		}
		key := sb.Metadata.Namespace + "/" + sb.Metadata.Name
		result[key] = ip
	}
	return result
}

func tidyLabels(raw map[string]string) map[string]string {
	if len(raw) == 0 {
		return raw
	}

	newLabels := make(map[string]string)
	for k, v := range raw {
		if k == types.ContainerLabelKeyPodName ||
			k == types.ContainerLabelKeyPodNamespace ||
			k == types.ContainerLabelKeyPodUid {
			continue
		}
		newLabels[k] = v
	}
	return newLabels
}

func getRuntimeService(criRuntimeEndpoint string) (res cri.RuntimeService, errs []error) {
	t := defaultTimeout
	endpoints := DefaultRuntimeEndpoints
	if criRuntimeEndpoint != "" {
		endpoints = []string{criRuntimeEndpoint}
	}
	for _, endPoint := range endpoints {
		var err error
		common.AgentLog.Infof("Connect using endpoint %q with %q timeout", endPoint, t)
		res, err = remote.NewRemoteRuntimeService(endPoint, t)
		path := strings.TrimPrefix(endPoint, "unix://")
		if err != nil {
			common.AgentLog.Infof(err.Error())
			err = common.UnwrapErr(err)
			if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file or directory") {
				err = errors.New("no such file or directory")
			}
			errs = append(errs, fmt.Errorf("connect using endpoint %s: %w", path, err))
			continue
		}
		if _, err1 := res.Version(string(remote.CRIVersionV1)); err1 != nil {
			common.AgentLog.Infof("check version %s failed: %s", remote.CRIVersionV1, err1)
			if _, err2 := res.Version(string("v1alpha2")); err2 != nil {
				common.AgentLog.Infof("check version %s failed: %s", "v1alpha2", err2)
				errs = append(errs, fmt.Errorf("using endpoint %s failed: %w", path, err1))
				res = nil
				continue
			}
		}
		common.AgentLog.Infof("Connected successfully using endpoint: %s", endPoint)
		errs = nil
		break
	}

	return res, errs
}

func formatErrors(errs []error) string {
	var messages []string
	for _, err := range errs {
		if err == nil {
			continue
		}
		msg := err.Error()
		if strings.Contains(msg, "while dialing: ") {
			messages = append(messages, strings.Trim(strings.Split(msg, "while dialing: ")[1], `"'`))
		} else {
			messages = append(messages, msg)
		}
	}

	return strings.Join(messages, ", ")
}
