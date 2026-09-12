package main

import (
	"context"
	"log"

	"github.com/Veritas-Calculus/terraform-provider-vcworkspace/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

var version = "dev"

func main() {
	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/veritas-calculus/vcworkspace",
	})
	if err != nil {
		log.Fatal(err)
	}
}
