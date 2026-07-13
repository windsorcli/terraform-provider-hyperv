package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/xeitu/terraform-provider-hyperv/internal/connection"
	"github.com/xeitu/terraform-provider-hyperv/internal/hyperv"
)

func main() {
	conn, err := connection.NewSSH(connection.SSHOptions{
		Host:           os.Getenv("HYPERV_HOST"),
		Username:       os.Getenv("HYPERV_USERNAME"),
		Password:       []byte(os.Getenv("HYPERV_PASSWORD")),
		KnownHostsPath: os.ExpandEnv("$HOME/.ssh/known_hosts"),
		CommandTimeout: 90 * time.Second,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "NewSSH:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	if err := conn.Open(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Open:", err)
		os.Exit(1)
	}
	defer func() { _ = conn.Close() }()

	c := hyperv.NewClient(conn)

	// Mimic terraform's parallel refresh: 10 concurrent reads over the
	// shared SSH client.
	const fanout = 10
	type result struct {
		idx  int
		kind string
		dur  time.Duration
		err  error
	}
	out := make(chan result, fanout)
	for i := 1; i <= fanout; i++ {
		go func() {
			start := time.Now()
			var err error
			kind := "GetVM"
			if i%2 == 0 {
				kind = "GetVMHost"
				_, err = c.GetVMHost(ctx)
			} else {
				_, err = c.GetVM(ctx, "controlplane")
			}
			out <- result{idx: i, kind: kind, dur: time.Since(start), err: err}
		}()
	}
	for i := 0; i < fanout; i++ {
		r := <-out
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "[%02d %s] err after %s: %v\n", r.idx, r.kind, r.dur, r.err)
		} else {
			fmt.Printf("[%02d %s] OK in %s\n", r.idx, r.kind, r.dur)
		}
	}
	fmt.Println("ALL DONE")
}
