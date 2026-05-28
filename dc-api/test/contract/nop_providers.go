//go:build contract

// Package contract holds the schemathesis-driven OpenAPI conformance
// test. The nop providers here let us boot dc-api without a real
// Harvester / Rancher / KubeOVN cluster — every backend operation
// returns "nop" so the handlers translate that into the documented
// 5xx/4xx response shape, and schemathesis only validates that the
// shape matches the spec.
package contract

import (
	"context"
	"errors"

	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers"
)

var errNop = errors.New("nop: provider disabled in contract test mode")

// ── Compute ─────────────────────────────────────────────────────────────────
type nopCompute struct{}

func (nopCompute) Name() string { return "nop" }
func (nopCompute) CreateVM(context.Context, string, string, models.VMSpec) (*models.Resource, error) {
	return nil, errNop
}
func (nopCompute) GetVM(context.Context, string) (*models.Resource, error)         { return nil, errNop }
func (nopCompute) DeleteVM(context.Context, string) error                          { return errNop }
func (nopCompute) ListVMs(context.Context, string, string) ([]*models.Resource, error) { return nil, errNop }
func (nopCompute) ListNetworks(context.Context) ([]*models.Network, error)    { return nil, errNop }
func (nopCompute) ListImages(context.Context) ([]*models.Image, error)        { return nil, errNop }
func (nopCompute) CreateImage(context.Context, string, string) (*models.Image, error) {
	return nil, errNop
}

// ── Cluster ─────────────────────────────────────────────────────────────────
type nopCluster struct{}

func (nopCluster) Name() string { return "nop" }
func (nopCluster) CreateCluster(context.Context, string, string, models.ClusterSpec) (*models.Resource, error) {
	return nil, errNop
}
func (nopCluster) GetCluster(context.Context, string) (*models.Resource, error) { return nil, errNop }
func (nopCluster) DeleteCluster(context.Context, string) error                  { return errNop }
func (nopCluster) GetKubeconfig(context.Context, string) (string, error)        { return "", errNop }
func (nopCluster) AddNodePool(context.Context, string, *models.NodePool, string, string, string, string) error {
	return errNop
}
func (nopCluster) ScaleNodePool(context.Context, string, string, int) error { return errNop }
func (nopCluster) UpdateNodePoolTaintsLabels(context.Context, string, string, []models.NodePoolTaint, map[string]string) error {
	return errNop
}
func (nopCluster) RemoveNodePool(context.Context, string, string, string) error { return errNop }
func (nopCluster) GetNodePoolStatuses(context.Context, string) (map[string]models.NodePoolStatus, error) {
	return nil, errNop
}

// ── Network ─────────────────────────────────────────────────────────────────
type nopNetwork struct{}

func (nopNetwork) Name() string { return "nop" }
func (nopNetwork) CreateVNet(context.Context, string, string, models.VNetSpec) (*models.VNetResource, error) {
	return nil, errNop
}
func (nopNetwork) GetVNet(context.Context, string) (*models.VNetResource, error) { return nil, errNop }
func (nopNetwork) DeleteVNet(context.Context, string) error                      { return errNop }
func (nopNetwork) CreateSubnet(context.Context, string, models.SubnetSpec) (*models.SubnetResource, error) {
	return nil, errNop
}
func (nopNetwork) GetSubnet(context.Context, string) (*models.SubnetResource, error) {
	return nil, errNop
}
func (nopNetwork) DeleteSubnet(context.Context, string) error { return errNop }
func (nopNetwork) CreateRouteTable(context.Context, string, models.RouteTableSpec) (*models.RouteTableResource, error) {
	return nil, errNop
}
func (nopNetwork) UpdateRouteTableRoutes(context.Context, string, []models.RouteRule) error {
	return errNop
}
func (nopNetwork) DeleteRouteTable(context.Context, string) error              { return errNop }
func (nopNetwork) AssociateRouteTable(context.Context, string, string) error   { return errNop }
func (nopNetwork) DisassociateRouteTable(context.Context, string, string) error { return errNop }
func (nopNetwork) CreateNSG(context.Context, string, string, models.NSGSpec) (*models.NSGResource, error) {
	return nil, errNop
}
func (nopNetwork) UpdateNSGRules(context.Context, string, []models.NSGRule) error { return errNop }
func (nopNetwork) DeleteNSG(context.Context, string) error                        { return errNop }
func (nopNetwork) AttachNSGToSubnet(context.Context, string, string) error        { return errNop }
func (nopNetwork) DetachNSGFromSubnet(context.Context, string, string) error      { return errNop }
func (nopNetwork) CreatePeering(context.Context, string, string, models.PeeringSpec) (*models.PeeringResource, error) {
	return nil, errNop
}
func (nopNetwork) DeletePeering(context.Context, string, []string, []string) error { return errNop }
func (nopNetwork) CreatePrivateDnsZone(context.Context, string, models.DnsZoneSpec) (*models.DnsZoneResource, error) {
	return nil, errNop
}
func (nopNetwork) DeletePrivateDnsZone(context.Context, string) error          { return errNop }
func (nopNetwork) UpsertDnsRecord(context.Context, string, models.DnsRecord) error { return errNop }
func (nopNetwork) DeleteDnsRecord(context.Context, string, string) error       { return errNop }

// Compile-time interface checks.
var (
	_ providers.ComputeProvider = nopCompute{}
	_ providers.ClusterProvider = nopCluster{}
	_ providers.NetworkProvider = nopNetwork{}
)
