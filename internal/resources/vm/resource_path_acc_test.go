package vm_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/windsorcli/terraform-provider-hyperv/internal/acctest"
	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
)

// TestAcc_VM_path creates a VM in a chosen folder, starts it, then moves
// it in place while running and checks the host-side locations each time.
func TestAcc_VM_path(t *testing.T) {
	dir := acctest.RequireEnv(t, "HYPERV_TEST_VHD_DIR")
	client := acctest.NewClient(t)

	name := acctest.RandomName("vm-path")
	root := joinHostPath(dir, name)
	first := toForwardSlash(joinHostPath(root, "a"))
	second := joinHostPath(root, "b")
	t.Cleanup(func() { removeHostDir(t, client, root) })
	vmIDStable := statecheck.CompareValue(compare.ValuesSame())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckResourceGone("hyperv_vm", client.GetVM),
		Steps: []resource.TestStep{
			{
				Config: vmWithPathConfig(name, first, "Off"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hyperv_vm.test", tfjsonpath.New("path"), knownvalue.StringExact(first)),
					vmIDStable.AddStateValue("hyperv_vm.test", tfjsonpath.New("vm_id")),
				},
				Check: checkVMStorageLocations(client, name, first),
			},
			{
				Config: vmWithPathConfig(name, first, "Running"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hyperv_vm.test", plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				Config: vmWithPathConfig(name, second, "Running"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hyperv_vm.test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hyperv_vm.test", tfjsonpath.New("path"), knownvalue.StringExact(second)),
					statecheck.ExpectKnownValue("hyperv_vm.test", tfjsonpath.New("state").AtMapKey("current"), knownvalue.StringExact("Running")),
					vmIDStable.AddStateValue("hyperv_vm.test", tfjsonpath.New("vm_id")),
				},
				Check: checkVMStorageLocations(client, name, second),
			},
		},
	})
}

// vmWithPathConfig renders a no-attachment VM with path and state.desired set.
func vmWithPathConfig(vmName, path, desired string) string {
	return fmt.Sprintf(`
resource "hyperv_vm" "test" {
  name       = %q
  generation = 2
  cpu    = { count = 1 }
  memory = { startup_bytes = %d }
  path   = %q
  state  = { desired = %q }
}
`, vmName, vmMinimumMemoryBytes, path, desired)
}

// checkVMStorageLocations asserts the VM's path, configuration, checkpoint,
// and smart-paging locations all equal want, compared case- and slash-insensitively.
func checkVMStorageLocations(client *hyperv.Client, name, want string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		script := fmt.Sprintf(
			`$vm = Get-VM -Name %q -ErrorAction Stop; `+
				`@($vm.Path, $vm.ConfigurationLocation, $vm.SnapshotFileLocation, $vm.SmartPagingFilePath) -join "`+"`n"+`"`,
			name)
		res, err := client.RunScript(ctx, script, nil)
		if err != nil {
			return fmt.Errorf("read storage locations of %s: %w", name, err)
		}
		labels := []string{"Path", "ConfigurationLocation", "SnapshotFileLocation", "SmartPagingFilePath"}
		got := strings.Split(strings.TrimSpace(string(res.Stdout)), "\n")
		if len(got) != len(labels) {
			return fmt.Errorf("read storage locations of %s: got %d lines, want %d: %q", name, len(got), len(labels), got)
		}
		norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "/", `\`)) }
		for i, label := range labels {
			if norm(got[i]) != norm(want) {
				return fmt.Errorf("%s.%s = %q, want %q", name, label, strings.TrimSpace(got[i]), want)
			}
		}
		return nil
	}
}

// removeHostDir deletes a test-owned folder on the bench, logging rather than failing on error.
func removeHostDir(t *testing.T, client *hyperv.Client, dir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	script := fmt.Sprintf(`Remove-Item -LiteralPath '%s' -Recurse -Force -ErrorAction SilentlyContinue`, strings.ReplaceAll(dir, "'", "''"))
	if _, err := client.RunScript(ctx, script, nil); err != nil {
		t.Logf("remove %s: %v", dir, err)
	}
}
