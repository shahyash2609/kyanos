package metadata

import (
	"bufio"
	"context"
	"fmt"
	"kyanos/agent/metadata/k8s"
	"kyanos/agent/metadata/types"
	"kyanos/common"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/containerd"
)

// DiscoverListeningPorts reads /proc/1/net/tcp and /proc/1/net/tcp6 (host network namespace)
// and returns all ports in LISTEN state (st=0A) with port number >= 1024.
// Running from /proc/1 ensures we see the host network namespace even from inside a container.
func DiscoverListeningPorts() []uint16 {
	seen := make(map[uint16]bool)
	for _, path := range []string{"/proc/1/net/tcp", "/proc/1/net/tcp6"} {
		parseTCPFile(path, seen)
	}
	ports := make([]uint16, 0, len(seen))
	for p := range seen {
		ports = append(ports, p)
	}
	return ports
}

// PodEndpoint is a reachable IP:port where a service is listening inside a pod.
type PodEndpoint struct {
	IP   string
	Port uint16
}

// DiscoverPodEndpoints uses CRI to enumerate running pods and their IPs, then
// reads each pod's container network namespace to find listening ports.
// Returns nil if CRI is unavailable; caller should fall back to DiscoverListeningPorts.
func DiscoverPodEndpoints(criEndpoint, containerdHost string) []PodEndpoint {
	// Step 1: pod name → IP via CRI
	md, err := k8s.NewMetaData(criEndpoint)
	if err != nil {
		common.AgentLog.Debugf("auto-reflect: CRI unavailable: %v", err)
		return nil
	}
	podIPs := md.PodIPMap()
	if len(podIPs) == 0 {
		common.AgentLog.Debugln("auto-reflect: no running pods found via CRI")
		return nil
	}

	// Step 2: pod name → container RootPid via containerd
	podPIDs := containerdPodPIDs(containerdHost)

	// Step 3: for each pod with both IP and PID, read its netns listening ports
	seen := make(map[string]bool)
	var results []PodEndpoint
	for key, ip := range podIPs {
		pid, ok := podPIDs[key]
		if !ok {
			continue
		}
		for _, port := range podListeningPorts(pid) {
			addr := fmt.Sprintf("%s:%d", ip, port)
			if !seen[addr] {
				seen[addr] = true
				results = append(results, PodEndpoint{IP: ip, Port: port})
			}
		}
	}
	common.AgentLog.Infof("auto-reflect: discovered %d pod endpoint(s) across %d pods", len(results), len(podIPs))
	return results
}

// containerdPodPIDs returns a map of "namespace/name" -> RootPid for all running
// containers in the k8s.io containerd namespace.
func containerdPodPIDs(host string) map[string]int {
	if host == "" {
		host = "/run/containerd/containerd.sock"
	}
	c, err := containerd.New(host,
		containerd.WithDefaultNamespace("k8s.io"),
		containerd.WithTimeout(3*time.Second))
	if err != nil {
		common.AgentLog.Debugf("auto-reflect: containerd unavailable at %s: %v", host, err)
		return nil
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	containers, err := c.Containers(ctx)
	if err != nil {
		common.AgentLog.Debugf("auto-reflect: list containers failed: %v", err)
		return nil
	}

	result := make(map[string]int)
	for _, cr := range containers {
		info, err := cr.Info(ctx)
		if err != nil {
			continue
		}
		podName := info.Labels[types.ContainerLabelKeyPodName]
		podNs := info.Labels[types.ContainerLabelKeyPodNamespace]
		if podName == "" || podNs == "" {
			continue
		}
		key := podNs + "/" + podName
		if _, exists := result[key]; exists {
			continue // already have a PID for this pod
		}
		task, err := cr.Task(ctx, nil)
		if err != nil {
			continue
		}
		result[key] = int(task.Pid())
	}
	return result
}

// podListeningPorts reads /proc/<pid>/net/tcp and /proc/<pid>/net/tcp6
// to find all LISTEN ports in the pod's network namespace.
func podListeningPorts(pid int) []uint16 {
	seen := make(map[uint16]bool)
	for _, name := range []string{"tcp", "tcp6"} {
		parseTCPFile(fmt.Sprintf("/proc/%d/net/%s", pid, name), seen)
	}
	ports := make([]uint16, 0, len(seen))
	for p := range seen {
		ports = append(ports, p)
	}
	return ports
}

// parseTCPFile parses /proc/net/tcp or /proc/net/tcp6 format.
// Each data line: sl  local_address  rem_address  st  ...
// local_address = HEXIP:HEXPORT (big-endian port), st=0A means LISTEN.
func parseTCPFile(path string, seen map[uint16]bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan() // skip header line
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if fields[3] != "0A" { // 0A = TCP_LISTEN
			continue
		}
		// local_address field: "HEXIP:HEXPORT"
		addrPort := strings.SplitN(fields[1], ":", 2)
		if len(addrPort) != 2 {
			continue
		}
		portVal, err := strconv.ParseUint(addrPort[1], 16, 16)
		if err != nil {
			continue
		}
		port := uint16(portVal)
		if port < 1024 {
			continue
		}
		seen[port] = true
	}
}
