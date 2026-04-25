package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
)

// Sanity check that the provider type satisfies the framework interface and
// metadata reports the expected name. More substantive tests land per
// resource as schemas grow.
func TestProvider_Metadata(t *testing.T) {
	t.Parallel()

	p := New("test-version")()
	resp := &provider.MetadataResponse{}
	p.Metadata(context.Background(), provider.MetadataRequest{}, resp)

	if resp.TypeName != "hyperv" {
		t.Errorf("TypeName = %q, want %q", resp.TypeName, "hyperv")
	}
	if resp.Version != "test-version" {
		t.Errorf("Version = %q, want %q", resp.Version, "test-version")
	}
}
