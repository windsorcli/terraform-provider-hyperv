package hyperv

import (
	"errors"
	"strings"
	"testing"

	"github.com/windsorcli/terraform-provider-hyperv/internal/testutil"
)

func TestGetVMHostScript_HasContractWrapper(t *testing.T) {
	// Pin: every script body MUST wrap cmdlets in try/catch and pair
	// Write-HypervError with `exit 1` per §5. Without this, terminating
	// PS errors reach stderr as native PS error records (multi-line,
	// not JSON), and the typed-error mapping in errors.go never fires.
	t.Parallel()

	for _, want := range []string{`try {`, `Write-HypervError $_`, `exit 1`} {
		if !strings.Contains(getVMHostScript, want) {
			t.Errorf("getVMHostScript missing required contract marker %q", want)
		}
	}
}

func TestClient_GetVMHost_HappyPath(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().On("Get-VMHost").Return(testutil.VMHostFixtureJSON, "", 0)
	c := NewClient(fr)

	h, err := c.GetVMHost(t.Context())
	if err != nil {
		t.Fatalf("GetVMHost: %v", err)
	}
	if h.ComputerName != "WIN-IUNE600K56E" {
		t.Errorf("ComputerName = %q, want WIN-IUNE600K56E", h.ComputerName)
	}
	if h.LogicalProcessorCount != 20 {
		t.Errorf("LogicalProcessorCount = %d, want 20", h.LogicalProcessorCount)
	}
	if h.MemoryCapacity != 102795845632 {
		t.Errorf("MemoryCapacity = %d, want 102795845632", h.MemoryCapacity)
	}
	if !strings.Contains(h.VirtualMachinePath, "Hyper-V") {
		t.Errorf("VirtualMachinePath = %q, want substring 'Hyper-V'", h.VirtualMachinePath)
	}
}

func TestClient_GetVMHost_TransportErrorBubblesUp(t *testing.T) {
	t.Parallel()

	want := errors.New("connection refused")
	fr := testutil.NewFakeRunner().On("Get-VMHost").ReturnErr(want)
	c := NewClient(fr)

	_, err := c.GetVMHost(t.Context())
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want errors.Is(_, %v)", err, want)
	}
	if !strings.Contains(err.Error(), "transport") {
		t.Errorf("err should label the transport layer; got %q", err.Error())
	}
}

func TestClient_GetVMHost_NotFoundEnvelopeMapsToErrNotFound(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"ObjectNotFound","message":"VM not found","cmdlet":"Get-VMHost"}`
	fr := testutil.NewFakeRunner().On("Get-VMHost").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.GetVMHost(t.Context())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestClient_GetVMHost_PermissionDeniedMapsToErrUnauthorized(t *testing.T) {
	t.Parallel()

	envelope := `{"category":"PermissionDenied","message":"access denied","cmdlet":"Get-VMHost"}`
	fr := testutil.NewFakeRunner().On("Get-VMHost").Return("", envelope, 1)
	c := NewClient(fr)

	_, err := c.GetVMHost(t.Context())
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestClient_GetVMHost_EmptyStdoutFailsWithPSExecution(t *testing.T) {
	// Exit 0 but no stdout — the chokepoint catches this with a clear
	// preamble/encoding-pin diagnostic instead of a generic JSON parse
	// failure deep in the stack.
	t.Parallel()

	fr := testutil.NewFakeRunner().On("Get-VMHost").Return("   \n  ", "", 0)
	c := NewClient(fr)

	_, err := c.GetVMHost(t.Context())
	if !errors.Is(err, ErrPSExecution) {
		t.Errorf("err = %v, want ErrPSExecution", err)
	}
	if !strings.Contains(err.Error(), "empty stdout") {
		t.Errorf("err should call out empty stdout; got %q", err.Error())
	}
}

func TestClient_GetVMHost_MalformedJSONReportsBoth(t *testing.T) {
	t.Parallel()

	fr := testutil.NewFakeRunner().On("Get-VMHost").Return(`{"ComputerName": "incomplete`, "", 0)
	c := NewClient(fr)

	_, err := c.GetVMHost(t.Context())
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("err should label decode failure; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "ComputerName") {
		t.Errorf("err should echo the offending stdout; got %q", err.Error())
	}
}
