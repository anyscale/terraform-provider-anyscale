package main

import (
	"context"
	"flag"
	"log"

	"github.com/brent/terraform-provider-anyscale/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

// Version information (set via ldflags)
var (
	version = "dev"
)

func main() {
	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		// Use proper registry namespace format
		// For HashiCorp registry: registry.terraform.io/<namespace>/<name>
		// The namespace should match your GitHub organization or username
		Address: "registry.terraform.io/anyscale/anyscale",
		Debug:   debug,
	}

	err := providerserver.Serve(context.Background(), provider.NewFramework(version), opts)

	if err != nil {
		log.Fatal(err.Error())
	}
}
