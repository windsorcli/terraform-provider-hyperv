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
