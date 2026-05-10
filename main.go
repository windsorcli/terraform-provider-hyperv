// Terraform provider for Hyper-V. Entry point.
//
// Real provider behavior lives under internal/provider. This file does only
// the minimum needed for `terraform init` to discover us: parse a -debug
// flag, then hand off to providerserver.Serve.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/windsorcli/terraform-provider-hyperv/internal/provider"
)

// version is replaced at release time via -ldflags "-X main.version=...".
var version = "dev"

// shutdownGrace bounds how long we wait for providerserver.Serve to
// unwind after rootCtx cancels before we force-exit. 2s is short
// enough that operators don't notice and long enough that go-plugin's
// gRPC GracefulStop finishes in normal cases.
const shutdownGrace = 2 * time.Second

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/windsorcli/hyperv",
		Debug:   debug,
	}

	// Cancel rootCtx on SIGINT/SIGTERM so providerserver.Serve unwinds
	// cleanly. Pair with provider.CloseActive() to send SSH_MSG_DISCONNECT
	// before the process exits -- otherwise an orphaned plugin (terraform
	// parent died via Ctrl-C, OOM, or panic, and forwarded SIGTERM to
	// children) would hold the bench-side OpenSSH MaxSessions slot until
	// SO_KEEPALIVE timeouts reap the half-open socket. SIGKILL is
	// unreachable -- nothing in userspace can clean up after that.
	rootCtx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Belt-and-suspenders: close backends from a goroutine the moment
	// the signal lands, then force-exit at shutdownGrace if Serve's
	// own gRPC drain hangs. The deferred CloseActive below covers the
	// normal-return path for graceful Serve exits.
	go func() {
		<-rootCtx.Done()
		provider.CloseActive()
		time.AfterFunc(shutdownGrace, func() { os.Exit(0) })
	}()

	err := providerserver.Serve(rootCtx, provider.New(version), opts)
	provider.CloseActive()
	// context.Canceled here is the signal-driven exit path -- not an
	// error. Anything else (real Serve failure) still surfaces.
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err.Error())
	}
}
