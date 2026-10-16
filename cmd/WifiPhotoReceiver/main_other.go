//go:build !windows

package main

import "fmt"

func main() {
	fmt.Println("SPMAG Wifi Photo Receiver's GUI runs on Windows only. On other systems use the command-line tool: go run ./cmd/wpr")
}
