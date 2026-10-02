*** Settings ***
Documentation    Rancher Guest Cluster Integration Test Cases (RKE2)
...             Covers single/multi-node RKE2 cluster creation, workload deployment
...             (CSI, LoadBalancer), scaling, upgrade, and custom cluster scenarios.
Test Tags        rancher    rke2    regression
Resource        ../../keywords/rancher.resource

Suite Setup      Get Or Create Rancher Test Environment
Suite Teardown   Teardown Rancher Test Suite

*** Test Cases ***
Test Create Single Node RKE2 Cluster with Basic Workloads
    [Tags]    smoke    p0
    [Documentation]    Create a single-node RKE2 cluster and verify basic workloads
    ...               (CSI, Whoami, LB DHCP, LB Pool) are functional.
    Given Single node RKE2 cluster is created
    Then Harvester deployments should be ready    ${SINGLE_CLUSTER_ID}
    When Basic workloads are deployed on single node cluster
    Then Basic workloads should be active on single node cluster

Test RWX Volume On Single Node Cluster
    [Tags]    p1    rwx
    [Documentation]    Enable storage network on the single-node cluster (requires
    ...               stopping and restarting the VM), create an RWX StorageClass,
    ...               PVC, and StatefulSet with 2 replicas, then verify data written
    ...               by one pod is readable from the other. Validates Harvester CSI
    ...               driver RWX volume support end-to-end including the storage
    ...               network lifecycle.
    Skip    Reason: Requires two network interfaces, currently hangs during creation. Needs revisit.
    Given Single node cluster is available
    And Storage network is enabled for RWX
    And Single node cluster should be ready
    And Basic workloads should be active on single node cluster
    When RWX volume is created on single node cluster
    And RWX StatefulSet with 2 replicas is deployed on single node cluster
    Then RWX StatefulSet should be ready on single node cluster
    And RWX shared data should be accessible across pods on single node cluster
    # [Teardown]    Cleanup RWX test resources on single node cluster

Test Create Multi Node RKE2 Cluster
    [Tags]    p0    cloudprovider    csi
    [Documentation]    Create and verify a 3-node RKE2 cluster.
    When Multi node RKE2 cluster is created
    Then Harvester deployments should be ready    ${MULTI_CLUSTER_ID}

Test CSI Deployment
    [Tags]    p0    csi
    [Documentation]    Deploy a CSI workload with PVC on the multi-node cluster.
    Given Multi node cluster is available
    When CSI workload with PVC is deployed
    Then CSI deployment and PVC should be active

Test Load Balancer DHCP Mode
    [Tags]    p0    cloudprovider
    [Documentation]    Deploy a Whoami workload and create a LoadBalancer service in DHCP mode.
    Given Multi node cluster is available
    And Whoami workload is deployed    dhcp
    And Whoami deployment should be active    dhcp
    When Load balancer is created in DHCP mode    dhcp
    Then DHCP load balancer should be serving traffic

Test Load Balancer Pool Mode
    [Tags]    p0    cloudprovider
    [Documentation]    Deploy a Whoami workload and create a LoadBalancer service in IP Pool mode.
    Given Multi node cluster is available
    And Whoami workload is deployed    pool
    And Whoami deployment should be active    pool
    When Load balancer is created in pool mode    pool
    Then Pool load balancer should be serving traffic

Test Scale Up RKE2 Cluster
    [Tags]    p0
    [Documentation]    Scale up the multi-node cluster by adding a worker node.
    Given Multi node cluster is available
    When Worker pool with 1 node is added
    Then Multi node cluster should be ready

Test Verify Workloads After Scale Up
    [Tags]    p0
    [Documentation]    Verify existing workloads and Harvester deployments still running after scale up.
    Given Multi node cluster is available
    Then All existing workloads should be active

Test New Workloads After Scale Up
    [Tags]    p0
    [Documentation]    Create new CSI workload after scale up to verify cluster health.
    Given Multi node cluster is available
    When New CSI workload is deployed    scaleup
    Then New CSI workload should be active    scaleup
    And Harvester deployments should be ready    ${MULTI_CLUSTER_ID}
    [Teardown]    Cleanup temporary workloads    scaleup

Test Scale Down RKE2 Cluster
    [Tags]    p0
    [Documentation]    Scale down the multi-node cluster by removing the worker pool.
    Given Multi node cluster is available
    When Worker pool is removed
    Then Multi node cluster should be ready

Test Upgrade RKE2 Cluster
    [Tags]    p1
    [Documentation]    Upgrade the multi-node RKE2 cluster to the next available
    ...               patch version, verify workloads survive, scale up, and create
    ...               new workloads on the upgraded cluster.
    Given Multi node cluster is available
    And Next RKE2 version is available
    When Multi node cluster is upgraded to next version
    Then Multi node cluster should be ready
    And All existing workloads should be active
    When Worker pool with 1 node is added
    Then Multi node cluster should be ready
    When New CSI workload is deployed    upgrade
    Then New CSI workload should be active    upgrade
    And Harvester deployments should be ready    ${MULTI_CLUSTER_ID}
    [Teardown]    Cleanup upgrade test resources

Test Cleanup Workloads
    [Tags]    p0
    [Documentation]    Delete all workloads from the multi-node cluster.
    Given Multi node cluster is available
    When All test workloads are removed from cluster
    Then Workloads should be cleaned up

Test Delete RKE2 Clusters
    [Tags]    p0    smoke
    [Documentation]    Delete both single-node and multi-node clusters.
    When Single node RKE2 cluster is deleted
    And Multi node RKE2 cluster is deleted
    Then Clusters should be deleted

Test Create Single Node Custom RKE2 Cluster with Basic Workloads
    [Tags]     custom    p2
    [Documentation]    Create a single-node custom RKE2 cluster with Harvester
    ...               cloud provider and verify basic workloads (CSI, Whoami, LB)
    ...               are functional.
    Given Single node custom RKE2 cluster is created
    Then Harvester deployments should be ready    ${CUSTOM_CLUSTER_ID}
    When Basic workloads are deployed on single node custom cluster
    Then Basic workloads should be active on single node custom cluster
    [Teardown]    Cleanup custom cluster test resources
