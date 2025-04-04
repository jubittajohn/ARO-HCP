package api

// Copyright (c) Microsoft Corporation.
// Licensed under the Apache License 2.0.

import (
	"fmt"

	azcorearm "github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"

	"github.com/Azure/ARO-HCP/internal/api/arm"
)

const (
	ProviderNamespace               = "Microsoft.RedHatOpenShift"
	ProviderNamespaceDisplay        = "Azure Red Hat OpenShift"
	ClusterResourceTypeName         = "hcpOpenShiftClusters"
	NodePoolResourceTypeName        = "nodePools"
	OperationResultResourceTypeName = "hcpOperationResults"
	OperationStatusResourceTypeName = "hcpOperationsStatus"
	ResourceTypeDisplay             = "Hosted Control Plane (HCP) OpenShift Clusters"
)

var (
	ClusterResourceType  = azcorearm.NewResourceType(ProviderNamespace, ClusterResourceTypeName)
	NodePoolResourceType = azcorearm.NewResourceType(ProviderNamespace, ClusterResourceTypeName+"/"+NodePoolResourceTypeName)
)

type VersionedHCPOpenShiftCluster interface {
	Normalize(*HCPOpenShiftCluster)
	ValidateStatic(current VersionedHCPOpenShiftCluster, updating bool, method string) *arm.CloudError
}

type VersionedHCPOpenShiftClusterNodePool interface {
	Normalize(*HCPOpenShiftClusterNodePool)
	ValidateStatic(current VersionedHCPOpenShiftClusterNodePool, updating bool, method string) *arm.CloudError
}

type Version interface {
	fmt.Stringer

	// Resource Types
	// Passing a nil pointer creates a resource with default values.
	NewHCPOpenShiftCluster(*HCPOpenShiftCluster) VersionedHCPOpenShiftCluster
	NewHCPOpenShiftClusterNodePool(*HCPOpenShiftClusterNodePool) VersionedHCPOpenShiftClusterNodePool

	// Response Marshaling
	MarshalHCPOpenShiftCluster(*HCPOpenShiftCluster) ([]byte, error)
	MarshalHCPOpenShiftClusterNodePool(*HCPOpenShiftClusterNodePool) ([]byte, error)
	MarshalHCPOpenShiftClusterAdminCredential(*HCPOpenShiftClusterAdminCredential) ([]byte, error)
}

// apiRegistry is the map of registered API versions
var apiRegistry = map[string]Version{}

func Register(version Version) {
	apiRegistry[version.String()] = version
}

func Lookup(key string) (version Version, ok bool) {
	version, ok = apiRegistry[key]
	return
}
