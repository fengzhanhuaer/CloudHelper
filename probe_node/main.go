package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"runtime"
	"time"
)

var BuildVersion = "dev"

type snapshot struct {
	Version   string `json:"version"`
	Time      string `json:"time"`
	Hostname  string `json:"hostname"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	CPUs      int    `json:"cpus"`
	GoVersion string `json:"go_version"`
	Memory    memory `json:"memory"`
	Disk      disk   `json:"disk"`
}

type memory struct {
	AllocBytes     uint64 `json:"alloc_bytes"`
	HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
	SysBytes       uint64 `json:"sys_bytes"`
	NumGC          uint32 `json:"num_gc"`
	Goroutines     int    `json:"goroutines"`
}

type disk struct {
	Path       string `json:"path"`
	TotalBytes uint64 `json:"total_bytes,omitempty"`
	FreeBytes  uint64 `json:"free_bytes,omitempty"`
	Error      string `json:"error,omitempty"`
}

func main() {
	listen := flag.String("listen", "", "optional local status endpoint, for example 127.0.0.1:16030")
	once := flag.Bool("once", false, "print one status snapshot and exit")
	flag.Parse()

	if *once || *listen == "" {
		writeJSON(os.Stdout, collectSnapshot())
		if *listen == "" {
			return
		}
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen failed: %v\n", err)
		os.Exit(1)
	}
	defer ln.Close()
	fmt.Fprintf(os.Stderr, "probe status endpoint listening on %s\n", ln.Addr())
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go handleConn(conn)
	}
}

func handleConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	payload, _ := json.MarshalIndent(collectSnapshot(), "", "  ")
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(payload), payload)
}

func collectSnapshot() snapshot {
	host, _ := os.Hostname()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return snapshot{
		Version:   BuildVersion,
		Time:      time.Now().UTC().Format(time.RFC3339),
		Hostname:  host,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		CPUs:      runtime.NumCPU(),
		GoVersion: runtime.Version(),
		Memory: memory{
			AllocBytes:     stats.Alloc,
			HeapAllocBytes: stats.HeapAlloc,
			SysBytes:       stats.Sys,
			NumGC:          stats.NumGC,
			Goroutines:     runtime.NumGoroutine(),
		},
		Disk: collectDisk("."),
	}
}

func collectDisk(path string) disk {
	return collectDiskPlatform(path)
}

func writeJSON(file *os.File, value any) {
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}
