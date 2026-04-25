// Terraform provider for Hyper-V. Entry point.
//
// Real provider behavior lives under internal/provider. This file does only
// the minimum needed for `terraform init` to discover us: parse a -debug
// flag, then hand off to providerserver.Serve.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/windsorcli/terraform-provider-hyperv/internal/provider"
)

// version is replaced at release time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/windsorcli/hyperv",
		Debug:   debug,
	}

	if err := providerserver.Serve(context.Background(), provider.New(version), opts); err != nil {
		log.Fatal(err.Error())
	}
}
