package connector

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveConnectorIdentityInitializesAndRestores(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConnectorIdentityFilename)

	initialized, err := ResolveConnectorIdentity(path, "  connector-1  ", "  instance-1  ")
	if err != nil {
		t.Fatalf("initialize identity: %v", err)
	}
	want := ConnectorIdentity{ConnectorID: "connector-1", InstanceID: "instance-1"}
	if initialized != want {
		t.Fatalf("initialized identity = %+v, want %+v", initialized, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat identity: %v", err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0600 {
		t.Fatalf("identity mode = %o, want 600", got)
	}

	restored, err := ResolveConnectorIdentity(path, "", "")
	if err != nil {
		t.Fatalf("restore identity: %v", err)
	}
	if restored != want {
		t.Fatalf("restored identity = %+v, want %+v", restored, want)
	}
	if _, err := ResolveConnectorIdentity(path, want.ConnectorID, want.InstanceID); err != nil {
		t.Fatalf("matching configured identity rejected: %v", err)
	}
}

func TestResolveConnectorIdentityRejectsUnsafeConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		connectorID string
		instanceID  string
		wantError   string
	}{
		{name: "both missing before initialization", wantError: "required before identity"},
		{name: "connector only", connectorID: "connector-1", wantError: "configured together"},
		{name: "instance only", instanceID: "instance-1", wantError: "configured together"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ConnectorIdentityFilename)
			_, err := ResolveConnectorIdentity(path, tt.connectorID, tt.instanceID)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("ResolveConnectorIdentity() error = %v, want %q", err, tt.wantError)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("invalid configuration created identity file: %v", statErr)
			}
		})
	}
}

func TestResolveConnectorIdentityRejectsConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConnectorIdentityFilename)
	if _, err := ResolveConnectorIdentity(path, "connector-1", "instance-1"); err != nil {
		t.Fatalf("initialize identity: %v", err)
	}

	for _, configured := range []ConnectorIdentity{
		{ConnectorID: "connector-2", InstanceID: "instance-1"},
		{ConnectorID: "connector-1", InstanceID: "instance-2"},
	} {
		_, err := ResolveConnectorIdentity(path, configured.ConnectorID, configured.InstanceID)
		if err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Fatalf("conflicting identity %+v error = %v", configured, err)
		}
	}

	restored, err := ResolveConnectorIdentity(path, "", "")
	if err != nil {
		t.Fatalf("restore original identity: %v", err)
	}
	if restored != (ConnectorIdentity{ConnectorID: "connector-1", InstanceID: "instance-1"}) {
		t.Fatalf("conflicts changed persisted identity: %+v", restored)
	}
}

func TestResolveConnectorIdentityRejectsInvalidPersistedFile(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "malformed", raw: `{`},
		{name: "unknown field", raw: `{"version":1,"connector_id":"connector-1","instance_id":"instance-1","token":"secret"}`},
		{name: "unsupported version", raw: `{"version":2,"connector_id":"connector-1","instance_id":"instance-1"}`},
		{name: "incomplete", raw: `{"version":1,"connector_id":"connector-1","instance_id":""}`},
		{name: "not normalized", raw: `{"version":1,"connector_id":" connector-1 ","instance_id":"instance-1"}`},
		{name: "trailing value", raw: `{"version":1,"connector_id":"connector-1","instance_id":"instance-1"} {}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ConnectorIdentityFilename)
			if err := os.WriteFile(path, []byte(tt.raw), 0600); err != nil {
				t.Fatalf("write identity: %v", err)
			}
			if _, err := ResolveConnectorIdentity(path, "", ""); err == nil {
				t.Fatal("invalid persisted identity was accepted")
			}
		})
	}
}

func TestResolveConnectorIdentityRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "identity-target.json")
	link := filepath.Join(dir, ConnectorIdentityFilename)
	if err := os.WriteFile(target, []byte(`{"version":1,"connector_id":"connector-1","instance_id":"instance-1"}`), 0600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ResolveConnectorIdentity(link, "", ""); err == nil {
		t.Fatal("symlink identity path was accepted")
	}
}
