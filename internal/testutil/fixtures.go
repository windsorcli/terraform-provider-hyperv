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

// VMSwitchExternalFixtureJSON is the canonical six-field shape that
// vswitch/{get,new,set}.ps1 emit, locked by the Pester contract tests.
// Single source of truth across the typed-client and resource-layer suites.
const VMSwitchExternalFixtureJSON = `{
	"Name": "external-switch",
	"SwitchType": "External",
	"AllowManagementOS": true,
	"NetAdapterInterfaceDescription": "Intel(R) Ethernet I210",
	"Notes": "production",
	"Id": "12345678-1234-5678-1234-567812345678"
}`

// VMSwitchPrivateFixtureJSON is the Private-switch variant -- no NIC
// description, no AllowManagementOS toggle in practice (the cmdlet ignores
// it). Useful for resource-layer tests that need a non-External shape.
const VMSwitchPrivateFixtureJSON = `{
	"Name": "private-switch",
	"SwitchType": "Private",
	"AllowManagementOS": false,
	"NetAdapterInterfaceDescription": null,
	"Notes": "",
	"Id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
}`

// ImageFileFixtureJSON is the canonical three-field shape that
// image_file/{get,new}.ps1 emit. SizeBytes is deliberately above 2^31
// (5 GiB) so int64 round-tripping is exercised -- a default-precision
// JSON number would land in float64 and lose precision above 2^53, but
// a careless int32 decode would overflow well before that.
const ImageFileFixtureJSON = `{
	"Path": "C:\\hyperv\\images\\ubuntu-22.04.vhdx",
	"SizeBytes": 5368709120,
	"Sha256": "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
}`

// VHDDynamicFixtureJSON is the canonical eight-field shape vhd/{get,new,set}.ps1
// emit for a sparse dynamic VHDX. Size is the declared 32 GiB; FileSize is
// the actual sparse on-disk size after creation (tiny). ParentPath is empty
// because dynamic disks have no parent. Format is uppercase "VHDX" because
// that's what Get-VHD's VhdFormat enum's ToString() emits on a real host
// (verified against Server 2019 in the M4 smoke test); the Pester _test_helpers
// stub mirrors this.
const VHDDynamicFixtureJSON = `{
	"Path": "C:\\hyperv\\vhds\\my-vm-system.vhdx",
	"VhdType": "Dynamic",
	"SizeBytes": 34359738368,
	"FileSizeBytes": 4194304,
	"BlockSizeBytes": 33554432,
	"ParentPath": "",
	"Format": "VHDX",
	"Attached": false
}`

// VHDDifferencingFixtureJSON exercises the parent-path round-trip and the
// "size inherited from parent" semantic (SizeBytes matches the parent's
// declared size; FileSize is small because the child has no writes yet).
const VHDDifferencingFixtureJSON = `{
	"Path": "C:\\hyperv\\vhds\\child.vhdx",
	"VhdType": "Differencing",
	"SizeBytes": 34359738368,
	"FileSizeBytes": 1048576,
	"BlockSizeBytes": 33554432,
	"ParentPath": "C:\\hyperv\\vhds\\parent.vhdx",
	"Format": "VHDX",
	"Attached": false
}`
