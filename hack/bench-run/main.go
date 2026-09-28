// Maintainer-only WinRM script runner against the Hyper-V bench, for
// arbitrary PowerShell without a full terraform plan/apply. Reads
// HYPERV_HOST/USERNAME/PASSWORD from env:
//
//	set -a; source .env.local; set +a
//	go run ./hack/bench-run 'Get-VM | Select-Object Name, State'
//
// Kept under hack/, not a cmd/ binary, since usage is editing the script literal at the call site.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: bench-run <script>")
		os.Exit(2)
	}
	conn, err := connection.NewWinRM(connection.WinRMOptions{
		Host:     os.Getenv("HYPERV_HOST"),
		Username: os.Getenv("HYPERV_USERNAME"),
		Password: []byte(os.Getenv("HYPERV_PASSWORD")),
		UseHTTPS: true,
		Insecure: true,
		Auth:     "ntlm",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "NewWinRM:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if err := conn.Open(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Open:", err)
		os.Exit(1)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "Close:", err)
		}
	}()
	res, err := conn.RunScript(ctx, os.Args[1], nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "RunScript:", err)
		os.Exit(1)
	}
	if len(res.Stdout) > 0 {
		_, _ = os.Stdout.Write(res.Stdout)
	}
	if len(res.Stderr) > 0 {
		_, _ = os.Stderr.Write(res.Stderr)
	}
	os.Exit(res.ExitCode)
}
