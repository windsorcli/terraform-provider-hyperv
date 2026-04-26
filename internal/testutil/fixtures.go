package testutil

// VMHostFixtureJSON is the canonical Get-VMHost output captured by spike #2
// against a real Server 2022 host. Single source of truth for tests across
// internal/hyperv and internal/datasources/host — drift between packages
// would otherwise let one suite pass against stale shape data while the
// other catches the change. See docs/spikes/02-json-contract.md.
const VMHostFixtureJSON = `{
	"ComputerName": "WIN-IUNE600K56E",
	"LogicalProcessorCount": 20,
	"MemoryCapacity": 102795845632,
	"VirtualMachinePath": "C:\\ProgramData\\Microsoft\\Windows\\Hyper-V",
	"VirtualHardDiskPath": "C:\\ProgramData\\Microsoft\\Windows\\Virtual Hard Disks"
}`
