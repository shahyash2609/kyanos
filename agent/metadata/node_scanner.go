package metadata

import (
	"bufio"
	"os"
	"strconv"
	"strings"
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
