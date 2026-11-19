package inventory

import (
	"path/filepath"
	"testing"
)

func TestSelect(t *testing.T) {
	inv, err := New([]Printer{
		{Name: "printer-a", Address: "stock-plus4.example", Firmware: FirmwareStock, Tags: []string{"stock"}},
		{Name: "printer-b", Address: "mainline-plus4.example", Firmware: FirmwareMainline, Tags: []string{"mainline"}},
	}, map[string][]string{"plus4": {"printer-a", "printer-b"}})
	if err != nil {
		t.Fatal(err)
	}

	got, err := inv.Select("@plus4,!#stock")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "printer-b" {
		t.Fatalf("unexpected selection: %#v", got)
	}
}

func TestRejectsUnknownGroupMember(t *testing.T) {
	_, err := New(
		[]Printer{{Name: "printer-a", Address: "stock-plus4.example", Firmware: FirmwareAuto}},
		map[string][]string{"fleet": {"missing"}},
	)
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestRejectsSharedKlippeeWorkspace(t *testing.T) {
	shared := filepath.Join("workspaces", "shared")
	absolute, err := filepath.Abs(shared)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New([]Printer{
		{Name: "printer-a", Address: "a.example", Firmware: FirmwareStock,
			Klippee: Klippee{Workspace: shared}},
		{Name: "printer-b", Address: "b.example", Firmware: FirmwareMainline,
			Klippee: Klippee{Workspace: absolute}},
	}, nil)
	if err == nil {
		t.Fatal("expected shared Klippee workspace to be rejected")
	}
}
