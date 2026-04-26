package hyperv

import "context"

// CONTRACT (§5): every script body wraps cmdlets in try/catch and pairs
// Write-HypervError with `exit 1`. Without this, terminating PS errors
// reach stderr as native PS error records (multi-line, not JSON), and the
// typed-error mapping in errors.go never fires — Get-VMHost-class
// failures would all collapse into ErrPSExecution.
const getVMHostScript = `try { Get-VMHost | Select-Object ComputerName,LogicalProcessorCount,MemoryCapacity,VirtualMachinePath,VirtualHardDiskPath | Write-HypervResult } catch { Write-HypervError $_; exit 1 }`

// GetVMHost returns the Hyper-V host info. The cmdlet stays inline; when
// M1c lands the vswitch scripts they'll move to
// internal/scripts/<resource>/<verb>.ps1 with Pester coverage.
func (c *Client) GetVMHost(ctx context.Context) (*VMHost, error) {
	var h VMHost
	if err := c.runScript(ctx, getVMHostScript, nil, &h); err != nil {
		return nil, err
	}
	return &h, nil
}
