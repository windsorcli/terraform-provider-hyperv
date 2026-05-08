package iso_volume_test

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/windsorcli/terraform-provider-hyperv/internal/acctest"
)

// TestValidate_FilesDrivenByVariable mirrors the protective regression
// from image_file's TestValidate_URLDrivenByVariable. The motivation is
// the same: when a Required Map<string, string> attribute is driven
// from `each.value.files` of a `for_each`-typed variable, the framework
// marshals the value as Unknown until the parent variable resolves. A
// model field of the wrong shape (e.g. map[string]string rather than
// types.Map) cannot represent unknown and surfaces as a "Value
// Conversion Error" at validate. We use types.Map; this test pins
// that choice.
//
// Empty default map means no resources are actually planned -- the
// framework still validates the resource schema against the typed
// variable, which is where a broken shape would fire.
func TestValidate_FilesDrivenByVariable(t *testing.T) {
	t.Parallel()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
variable "seeds" {
  type = map(object({
    destination_path = string
    volume_label     = string
    files            = map(string)
  }))
  default = {}
}

resource "hyperv_iso_volume" "seeds" {
  for_each         = var.seeds
  destination_path = each.value.destination_path
  volume_label     = each.value.volume_label
  files            = each.value.files
}
`,
				PlanOnly: true,
			},
		},
	})
}

// TestValidate_RejectsLowercaseVolumeLabel pins the schema-level regex
// that confines volume_label to ECMA-119 d-characters (A-Z, 0-9, _).
// cloud-init and the Windows installer both uppercase before matching,
// but storing user-supplied lowercase would make state and the on-disk
// PVD bytes disagree -- surfacing as phantom drift on the next refresh.
// The validator catches this at plan time.
func TestValidate_RejectsLowercaseVolumeLabel(t *testing.T) {
	t.Parallel()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "hyperv_iso_volume" "lowercase" {
  destination_path = "C:/hyperv/seeds/x.iso"
  volume_label     = "cidata"
  files            = { "meta-data" = "x" }
}
`,
				ExpectError: regexp.MustCompile(`A-Z, 0-9, and underscore`),
			},
		},
	})
}

// TestValidate_RejectsOversizedVolumeLabel pins the 32-byte ECMA-119
// PVD VolumeIdentifier limit. kdomanski/iso9660 silently truncates
// longer labels; a clean validate-time rejection points the user at
// the offending attribute.
func TestValidate_RejectsOversizedVolumeLabel(t *testing.T) {
	t.Parallel()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "hyperv_iso_volume" "oversized" {
  destination_path = "C:/hyperv/seeds/x.iso"
  volume_label     = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"  # 33 chars
  files            = { "meta-data" = "x" }
}
`,
				ExpectError: regexp.MustCompile(`(?i)length`),
			},
		},
	})
}

// TestValidate_RejectsFilenamesWithPathSeparator confirms the Map
// KeysAre validator rejects slashes and backslashes -- v1 only
// supports root-level files, and a key like "EFI/Boot/bootx64.efi"
// would otherwise reach kdomanski's mangler and produce a silently-
// flattened layout the user did not ask for.
func TestValidate_RejectsFilenamesWithPathSeparator(t *testing.T) {
	t.Parallel()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "hyperv_iso_volume" "with_subdir" {
  destination_path = "C:/hyperv/seeds/x.iso"
  volume_label     = "CIDATA"
  files = {
    "EFI/Boot/bootx64.efi" = "x"
  }
}
`,
				ExpectError: regexp.MustCompile(`path\s+separators`),
			},
		},
	})
}
