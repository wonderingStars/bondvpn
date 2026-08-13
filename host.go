package main

import (
	"os"
	"strconv"
	"strings"
)

// HostStatus is the machine the gateway runs on.
//
// Read from /proc rather than shelled out to: `uptime`, `free` and `df` differ
// between distributions and busybox, and one of them hanging on a dead NFS
// mount used to stop the whole status document being written. Nothing here can
// block - /proc reads are served from memory - except the disk figures, which
// are handled separately for exactly that reason.
type HostStatus struct {
	Name      string       `json:"name"`
	Uptime    int64        `json:"uptime"`
	Cores     int          `json:"cores"`
	Load1     float64      `json:"load1"`
	LoadPct   int          `json:"load_pct"`
	MemUsedKB int64        `json:"mem_used_kb"`
	MemTotKB  int64        `json:"mem_total_kb"`
	Disks     []DiskStatus `json:"disks"`
}

// DiskStatus is one watched filesystem. Free is zero and OK false when the
// mount could not be read, which is the honest answer for an NFS export whose
// server has gone away - and distinguishable from a genuinely full disk.
type DiskStatus struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	Total int64  `json:"total"`
	Free  int64  `json:"free"`
	OK    bool   `json:"ok"`
}

func readHost(cfg *Config) HostStatus {
	h := HostStatus{Cores: numCPU()}
	if n, err := os.Hostname(); err == nil {
		h.Name = n
	}
	h.Uptime = readUptime()
	h.Load1 = readLoad1()
	if h.Cores > 0 {
		h.LoadPct = int(h.Load1 / float64(h.Cores) * 100)
	}
	h.MemUsedKB, h.MemTotKB = readMem()
	for _, d := range cfg.Disks {
		ds := DiskStatus{Label: d.Label, Path: d.Path}
		if total, free, err := diskUsage(d.Path); err == nil {
			ds.Total, ds.Free, ds.OK = total, free, true
		}
		h.Disks = append(h.Disks, ds)
	}
	return h
}

func numCPU() int {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "processor") {
			n++
		}
	}
	return n
}

func readUptime() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	return int64(v)
}

func readLoad1() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// readMem returns used and total in kB.
//
// Used is total minus MemAvailable, not minus MemFree. Free excludes the page
// cache, so a healthy Linux box that has read some files reports itself as
// nearly out of memory - which is how a dashboard ends up permanently amber on
// a machine with 30 GB spare.
func readMem() (used, total int64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var avail int64
	for _, line := range strings.Split(string(b), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(val)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			total = n
		case "MemAvailable":
			avail = n
		}
	}
	if total > 0 && avail > 0 {
		used = total - avail
	}
	return used, total
}
