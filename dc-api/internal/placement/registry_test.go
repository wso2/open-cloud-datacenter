package placement

import "testing"

// TestSamePlacement_Identical pins that equal region+zone yields no error so a
// peering proceeds — the common single-zone production case.
func TestSamePlacement_Identical(t *testing.T) {
	if e := SamePlacement("lk", "zone-1", "lk", "zone-1"); e != nil {
		t.Errorf("SamePlacement(equal) = %v, want nil", e)
	}
}

// TestSamePlacement_CrossRegion pins the cross-region branch: 400 with the exact
// legacy message and operand order (a, b) == (vnet, peerVNet).
func TestSamePlacement_CrossRegion(t *testing.T) {
	e := SamePlacement("lk", "zone-1", "eu", "zone-1")
	if e == nil {
		t.Fatal("SamePlacement(cross-region) = nil, want error")
	}
	if e.Status != 400 {
		t.Errorf("status = %d, want 400", e.Status)
	}
	want := "cross-region peering is not supported in M2 (VNet region: lk, peer VNet region: eu)"
	if e.Msg != want {
		t.Errorf("msg = %q, want %q", e.Msg, want)
	}
}

// TestSamePlacement_CrossZone pins the cross-zone branch: equal region, differing
// zone → 422 with the exact legacy message and operand order.
func TestSamePlacement_CrossZone(t *testing.T) {
	e := SamePlacement("lk", "zone-1", "lk", "zone-2")
	if e == nil {
		t.Fatal("SamePlacement(cross-zone) = nil, want error")
	}
	if e.Status != 422 {
		t.Errorf("status = %d, want 422", e.Status)
	}
	want := "cross-zone peering is not supported (VNet zone: zone-1, peer VNet zone: zone-2)"
	if e.Msg != want {
		t.Errorf("msg = %q, want %q", e.Msg, want)
	}
}

// TestSamePlacement_RegionWinsOverZone pins precedence: when BOTH region and zone
// differ, the region (400) guard fires first, exactly as the two consecutive
// legacy if-blocks ordered them.
func TestSamePlacement_RegionWinsOverZone(t *testing.T) {
	e := SamePlacement("lk", "zone-1", "eu", "zone-2")
	if e == nil || e.Status != 400 {
		t.Fatalf("SamePlacement(both differ) = %v, want status 400 (region first)", e)
	}
}

// TestFamilyDeclarations pins the declared shape of each family so an accidental
// flip (e.g. Database → Child, which would change stamping) is caught.
func TestFamilyDeclarations(t *testing.T) {
	// Roots that default-stamp.
	for _, f := range []Family{VNet, KeyVault, Database, PrivateEndpoint} {
		if !f.Regional || f.Mode != Root {
			t.Errorf("%s should be Regional Root, got Regional=%v Mode=%v", f.Kind, f.Regional, f.Mode)
		}
	}
	// VNet alone takes region from the model.
	if !VNet.RegionFromModel {
		t.Error("VNet must be RegionFromModel")
	}
	if KeyVault.RegionFromModel || Database.RegionFromModel || PrivateEndpoint.RegionFromModel {
		t.Error("field-less roots must NOT be RegionFromModel")
	}
	// VM/Cluster/Bastion are Children of VNET.
	for _, f := range []Family{VM, Cluster, Bastion} {
		if !f.Regional || f.Mode != Child || f.ParentKind != "VNET" || f.RegionFromModel {
			t.Errorf("%s should be Regional Child of VNET, !RegionFromModel; got %+v", f.Kind, f)
		}
	}
	// Non-regional families.
	for _, f := range []Family{Subnet, RouteTable, NSG, NSGAttachment, Peering, PrivateDnsZone, DnsRecord, RouteTableAssoc} {
		if f.Regional {
			t.Errorf("%s should be non-regional", f.Kind)
		}
	}
}
