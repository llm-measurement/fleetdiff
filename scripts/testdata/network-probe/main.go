// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Test-only route check for restricted-smoke.sh; no application code imports it.
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "bridge" && os.Args[1] != "none") {
		fmt.Fprintln(os.Stderr, "supply bridge or none")
		os.Exit(2)
	}
	// UDP connect checks the kernel route without sending a packet. TEST-NET-1
	// is a documentation address, so this does not depend on a live service.
	conn, err := net.DialTimeout("udp4", "192.0.2.1:9", 2*time.Second)
	if conn != nil {
		conn.Close()
	}
	if os.Args[1] == "bridge" && err == nil {
		fmt.Println("PASS network probe: bridge has an outbound route (no packets sent)")
		return
	}
	if os.Args[1] == "none" && errors.Is(err, syscall.ENETUNREACH) {
		fmt.Println("PASS network probe: none rejects outbound routing with ENETUNREACH")
		return
	}
	fmt.Fprintf(os.Stderr, "unexpected %s route result: %v\n", os.Args[1], err)
	os.Exit(1)
}
