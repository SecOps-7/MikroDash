package db

import (
	"errors"
	"testing"
)

func strp(s string) *string { return &s }

func TestAProvisioningRecordRoundTrips(t *testing.T) {
	d := openTest(t, t.TempDir())
	dev := ZTPDevice{ID: "z1", Mode: ZTPRemote, State: ZTPAwaiting, Label: "Branch 12", Serial: "HF0000000AB",
		TokenHash: strp("h-1"), TunnelIP: strp("10.249.1.7"), PeerKey: strp("pk-1"), CreatedBy: "u-1",
		ValuesJSON: `{"dns":"192.0.2.53"}`}
	if err := d.CreateZTPDevice(dev); err != nil {
		t.Fatal(err)
	}
	for name, get := range map[string]func() (*ZTPDevice, error){
		"id": func() (*ZTPDevice, error) { return d.ZTPDevice("z1") }, "token": func() (*ZTPDevice, error) { return d.ZTPDeviceByToken("h-1") },
		"peer": func() (*ZTPDevice, error) { return d.ZTPDeviceByPeer("pk-1") },
	} {
		got, err := get()
		if err != nil || got.Label != "Branch 12" || got.Serial != "HF0000000AB" || *got.TunnelIP != "10.249.1.7" ||
			got.ValuesJSON != `{"dns":"192.0.2.53"}` || got.SiteIDs != "[]" || got.AckedJSON != "[]" ||
			got.FactsJSON != "{}" || got.CreatedAt == 0 || got.CreatedBy != "u-1" {
			t.Errorf("by %s: %+v, %v", name, got, err)
		}
	}
	if _, err := d.ZTPDeviceByToken("nope"); !errors.Is(err, ErrZTPNotFound) {
		t.Errorf("an unknown token gave %v", err)
	}

	// ONE TUNNEL ADDRESS, ONE KEY, ONE TOKEN PER DEVICE: a second device
	// claiming any of them is refused by the table, not by the caller's care.
	for field, dup := range map[string]ZTPDevice{
		"tunnel address": {ID: "z2", Mode: ZTPRemote, State: ZTPAwaiting, TunnelIP: strp("10.249.1.7"), CreatedBy: "u-1"},
		"peer key":       {ID: "z3", Mode: ZTPGeneric, State: ZTPPending, PeerKey: strp("pk-1"), CreatedBy: "u-1"},
		"token":          {ID: "z4", Mode: ZTPLocal, State: ZTPAwaiting, TokenHash: strp("h-1"), CreatedBy: "u-1"},
	} {
		if err := d.CreateZTPDevice(dup); err == nil {
			t.Errorf("two devices share a %s", field)
		}
	}

	got, _ := d.ZTPDevice("z1")
	got.State, got.Secret, got.RouterID, got.FactsJSON = ZTPEnrolled, strp("sealed:x"), strp("r-9"), `{"model":"RB5009"}`
	got.TokenHash = nil // spent
	if err := d.SaveZTPDevice(*got); err != nil {
		t.Fatal(err)
	}
	again, _ := d.ZTPDevice("z1")
	if again.State != ZTPEnrolled || *again.Secret != "sealed:x" || *again.RouterID != "r-9" || again.TokenHash != nil ||
		again.FactsJSON != `{"model":"RB5009"}` || again.CreatedBy != "u-1" {
		t.Errorf("after save: %+v", again)
	}
	if err := d.SaveZTPDevice(ZTPDevice{ID: "missing"}); !errors.Is(err, ErrZTPNotFound) {
		t.Errorf("saving a missing device gave %v", err)
	}
	list, _ := d.ZTPDevices()
	if len(list) != 1 {
		t.Errorf("%d devices listed", len(list))
	}
	if err := d.DeleteZTPDevice("z1"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ZTPDevice("z1"); !errors.Is(err, ErrZTPNotFound) {
		t.Error("a deleted device is still there")
	}
}

// A revoked batch stays readable (so its devices can say where they came
// from), is revoked once, and a device keeps its batch reference until the
// batch row is deleted.
func TestABatchIsRevokedOnce(t *testing.T) {
	d := openTest(t, t.TempDir())
	if err := d.CreateZTPBatch(ZTPBatch{ID: "b1", Name: "Rollout", TokenHash: "bh", PublicKey: "pub", CreatedBy: "u-1"}); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateZTPDevice(ZTPDevice{ID: "g1", Mode: ZTPGeneric, State: ZTPPending, BatchID: strp("b1"), CreatedBy: "u-1"}); err != nil {
		t.Fatal(err)
	}
	if err := d.RevokeZTPBatch("b1", 1790000000000); err != nil {
		t.Fatal(err)
	}
	if err := d.RevokeZTPBatch("b1", 1790000000001); !errors.Is(err, ErrZTPNotFound) {
		t.Errorf("a batch was revoked twice: %v", err)
	}
	b, err := d.ZTPBatchByToken("bh")
	if err != nil || b.RevokedAt == nil || *b.RevokedAt != 1790000000000 || b.CreatedBy != "u-1" {
		t.Errorf("revoked batch: %+v, %v", b, err)
	}
	g, _ := d.ZTPDevice("g1")
	if g.BatchID == nil || *g.BatchID != "b1" {
		t.Errorf("the device lost its batch: %+v", g)
	}
	list, _ := d.ZTPBatches()
	if len(list) != 1 {
		t.Errorf("%d batches listed", len(list))
	}
}
