// Sweeper registrations + the -sweep flag dispatcher for acceptance-test
// orphan cleanup. All sweepers live here, not in their respective
// internal/resources/* packages, since resource.sweeperFuncs is
// per-test-binary: per-package sweepers would let cross-resource
// Dependencies (e.g. image_file depends on vm) silently no-op across
// binaries, and `go test`'s alphabetical package order would sweep
// vm after image_file/vhd instead of before, hitting file-locked
// errors on VMs that still hold their disks. `task sweep` scopes to
// `./internal/acctest/...`, the one binary that dispatches -sweep.

package acctest_test

import (
	"context"
	"errors"
	"log"
	"os"
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
// global sweeperFuncs map, encoding Hyper-V's locking semantics via
// the Dependencies graph: hyperv_vm has none and sweeps first, since
// Remove-VHD / Remove-Item fail with file-locked errors while a VM
// still references the file.
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

			// Best-effort: log and continue past individual failures, aggregating errors so a non-zero exit still surfaces.
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

	// A single stuck NetNat blocks every subsequent NAT test, since Windows allows exactly one NetNat per host.
	resource.AddTestSweepers("hyperv_nat", &resource.Sweeper{
		Name:         "hyperv_nat",
		Dependencies: []string{"hyperv_vm"},
		F: func(_ string) error {
			ctx, cancel := context.WithTimeout(context.Background(), sweepBudget)
			defer cancel()

			client, closeClient, err := acctest.NewClientForSweep(ctx)
			if err != nil {
				return err
			}
			defer closeClient()

			removed, err := client.SweepNetNats(ctx, acctest.SweepPrefix)
			if err != nil {
				return err
			}
			log.Printf("[INFO] hyperv_nat sweeper: removed %d orphan NetNat(s) under prefix %q: %v", len(removed), acctest.SweepPrefix, removed)
			return nil
		},
	})

	// Runs after hyperv_vm and hyperv_nat: Remove-VMSwitch fails while a NIC or a NetNat still references the switch.
	resource.AddTestSweepers("hyperv_virtual_switch", &resource.Sweeper{
		Name:         "hyperv_virtual_switch",
		Dependencies: []string{"hyperv_vm", "hyperv_nat"},
		F: func(_ string) error {
			ctx, cancel := context.WithTimeout(context.Background(), sweepBudget)
			defer cancel()

			client, closeClient, err := acctest.NewClientForSweep(ctx)
			if err != nil {
				return err
			}
			defer closeClient()

			switches, err := client.ListVMSwitchesByPrefix(ctx, acctest.SweepPrefix)
			if err != nil {
				return err
			}
			log.Printf("[INFO] hyperv_virtual_switch sweeper: found %d orphan switches under prefix %q", len(switches), acctest.SweepPrefix)

			var sweepErr error
			for _, sw := range switches {
				log.Printf("[INFO] hyperv_virtual_switch sweeper: removing %q", sw.Name)
				if rmErr := client.RemoveVMSwitch(ctx, sw.Name, ""); rmErr != nil && !errors.Is(rmErr, hyperv.ErrNotFound) {
					log.Printf("[WARN] hyperv_virtual_switch sweeper: remove %q failed: %v", sw.Name, rmErr)
					sweepErr = errors.Join(sweepErr, rmErr)
				}
			}
			return sweepErr
		},
	})

	// Runs after hyperv_vm: while a VM still references a VHD, Remove-VHD fails with a sharing violation.
	resource.AddTestSweepers("hyperv_vhd", &resource.Sweeper{
		Name:         "hyperv_vhd",
		Dependencies: []string{"hyperv_vm"},
		F: func(_ string) error {
			ctx, cancel := context.WithTimeout(context.Background(), sweepBudget)
			defer cancel()

			parentDir := os.Getenv("HYPERV_TEST_VHD_DIR")
			if parentDir == "" {
				log.Printf("[INFO] hyperv_vhd sweeper: HYPERV_TEST_VHD_DIR unset; nothing to sweep")
				return nil
			}

			client, closeClient, err := acctest.NewClientForSweep(ctx)
			if err != nil {
				return err
			}
			defer closeClient()

			vhds, err := client.ListVHDsByPrefix(ctx, parentDir, acctest.SweepPrefix)
			if err != nil {
				return err
			}
			log.Printf("[INFO] hyperv_vhd sweeper: found %d orphan VHDs under %s with prefix %q", len(vhds), parentDir, acctest.SweepPrefix)

			var sweepErr error
			for _, vhd := range vhds {
				log.Printf("[INFO] hyperv_vhd sweeper: removing %q", vhd.Path)
				if rmErr := client.RemoveVHD(ctx, vhd.Path); rmErr != nil && !errors.Is(rmErr, hyperv.ErrNotFound) {
					log.Printf("[WARN] hyperv_vhd sweeper: remove %q failed: %v", vhd.Path, rmErr)
					sweepErr = errors.Join(sweepErr, rmErr)
				}
			}
			return sweepErr
		},
	})

	// Sweeps non-VHD-family files (ISOs, .bin fixtures) under HYPERV_TEST_VHD_DIR; depends on hyperv_vm for the same reason hyperv_vhd does.
	resource.AddTestSweepers("hyperv_image_file", &resource.Sweeper{
		Name:         "hyperv_image_file",
		Dependencies: []string{"hyperv_vm"},
		F: func(_ string) error {
			ctx, cancel := context.WithTimeout(context.Background(), sweepBudget)
			defer cancel()

			parentDir := os.Getenv("HYPERV_TEST_VHD_DIR")
			if parentDir == "" {
				log.Printf("[INFO] hyperv_image_file sweeper: HYPERV_TEST_VHD_DIR unset; nothing to sweep")
				return nil
			}

			client, closeClient, err := acctest.NewClientForSweep(ctx)
			if err != nil {
				return err
			}
			defer closeClient()

			removed, err := client.SweepImageFiles(ctx, parentDir, acctest.SweepPrefix)
			if err != nil {
				return err
			}
			log.Printf("[INFO] hyperv_image_file sweeper: removed %d orphan file(s) under %s with prefix %q: %v", len(removed), parentDir, acctest.SweepPrefix, removed)
			return nil
		},
	})
}
