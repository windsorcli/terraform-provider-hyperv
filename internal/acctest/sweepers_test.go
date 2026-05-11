// Sweeper registrations + the -sweep flag dispatcher for acceptance-test
// orphan cleanup. All sweepers live here (not in their respective
// internal/resources/* packages) for two load-bearing reasons:
//
//  1. resource.sweeperFuncs is per-test-binary. A sweeper registered
//     in one package's test binary is invisible to another's. With
//     per-package sweepers, `go test -sweep=local ./...` would run
//     each package's sweepers in isolation against its own
//     sweeperFuncs map, and any cross-resource Dependencies (e.g.,
//     image_file depends on vm) would silently no-op because the
//     dependee lives in a different binary.
//
//  2. `go test`'s default package execution order is alphabetical
//     (image_file -> vhd -> vm -> vswitch), which is exactly wrong:
//     VMs hold disks, so vm must sweep FIRST or the image_file /
//     vhd Remove operations hit file-locked errors. Centralizing
//     registration lets the framework's Dependencies graph do the
//     ordering.
//
// The corresponding Taskfile entry scopes `task sweep` to
// `./internal/acctest/...` so the -sweep flag is only dispatched to
// the one test binary that knows what to do with it. Other test
// packages don't see the flag.

package acctest_test

import (
	"context"
	"errors"
	"log"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/windsorcli/terraform-provider-hyperv/internal/acctest"
	"github.com/windsorcli/terraform-provider-hyperv/internal/hyperv"
)

// TestMain dispatches the `-sweep=...` flag to terraform-plugin-testing's
// sweeper runner when set, otherwise runs the package's tests normally.
// Required because only the test binary built from this package owns the
// -sweep flag wiring; running `go test -sweep=local` against any other
// package would emit the flag-help dump and exit 1.
func TestMain(m *testing.M) {
	resource.TestMain(m)
}

// sweepBudget caps the wall-time any individual sweeper's enumerate +
// delete loop is allowed to consume. 5 minutes handles a bench with
// dozens of orphan resources at worst-case per-cmdlet latency; a hang
// past this surfaces as a sweep error so the operator notices.
const sweepBudget = 5 * time.Minute

// init registers all resource sweepers with terraform-plugin-testing's
// global sweeperFuncs map. Runs at test-binary load. The Dependencies
// graph below encodes the ordering required by Hyper-V's locking
// semantics:
//
//   - hyperv_vm sweeps FIRST (no Dependencies). VMs hold their disks
//     by path; Remove-VHD / Remove-Item fail with file-locked errors
//     while a VM still references the file.
//
// As sweepers for the other resources land in this file, their
// Dependencies will list "hyperv_vm" so they run after vm has cleared.
func init() {
	resource.AddTestSweepers("hyperv_vm", &resource.Sweeper{
		Name: "hyperv_vm",
		F: func(_ string) error {
			ctx, cancel := context.WithTimeout(context.Background(), sweepBudget)
			defer cancel()

			client, closeClient, err := acctest.NewClientForSweep(ctx)
			if err != nil {
				return err
			}
			defer closeClient()

			vms, err := client.ListVMsByPrefix(ctx, acctest.SweepPrefix)
			if err != nil {
				return err
			}
			log.Printf("[INFO] hyperv_vm sweeper: found %d orphan VMs under prefix %q", len(vms), acctest.SweepPrefix)

			// Best-effort per-VM: log and continue on individual
			// failures rather than aborting the whole sweep on the
			// first stuck VM. A VM in a transitional state shouldn't
			// block cleanup of the rest. Aggregate errors so the
			// sweeper still reports non-nil at the end when anything
			// failed -- the runner surfaces that as a non-zero exit.
			var sweepErr error
			for _, vm := range vms {
				log.Printf("[INFO] hyperv_vm sweeper: removing %q", vm.Name)
				if rmErr := client.RemoveVM(ctx, vm.Name); rmErr != nil && !errors.Is(rmErr, hyperv.ErrNotFound) {
					log.Printf("[WARN] hyperv_vm sweeper: remove %q failed: %v", vm.Name, rmErr)
					sweepErr = errors.Join(sweepErr, rmErr)
				}
			}
			return sweepErr
		},
	})
}
