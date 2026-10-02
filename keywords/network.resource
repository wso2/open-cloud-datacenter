*** Settings ***
Documentation    Network Keywords - Reusable keywords for network operations
Library          ../libs/keywords/network_keywords.py
Resource         variables.resource

*** Keywords ***
# Cluster Network Operations
Create Cluster Network
    [Arguments]    ${name}
    [Documentation]    Create cluster network.
    ...               Args:
    ...                   name: Cluster network name
    ...               Returns:
    ...                   dict: Cluster network data
    Log    Creating cluster network: ${name}
    ${cn}=    network_keywords.create_cluster_network    ${name}
    RETURN    ${cn}

Delete Cluster Network
    [Arguments]    ${name}
    [Documentation]    Delete cluster network.
    ...               Args:
    ...                   name: Cluster network name
    Log    Deleting cluster network: ${name}
    network_keywords.delete_cluster_network    ${name}

Create VLAN Config
    [Arguments]    ${name}    ${cluster_network}    ${nic}
    [Documentation]    Create VLAN config to bind NIC to cluster network.
    ...               Args:
    ...                   name: VLAN config name
    ...                   cluster_network: Cluster network name
    ...                   nic: NIC name (e.g., eno50)
    ...               Returns:
    ...                   dict: VLAN config data
    Log    Creating VLAN config: ${name}
    ${vc}=    network_keywords.create_vlan_config    ${name}    ${cluster_network}    ${nic}
    RETURN    ${vc}

Delete VLAN Config
    [Arguments]    ${name}
    [Documentation]    Delete VLAN config.
    ...               Args:
    ...                   name: VLAN config name
    Log    Deleting VLAN config: ${name}
    network_keywords.delete_vlan_config    ${name}

Wait For Cluster Network Ready
    [Arguments]    ${name}    ${timeout}=${WAIT_TIMEOUT}
    [Documentation]    Wait for cluster network to become ready.
    ...               Args:
    ...                   name: Cluster network name
    ...                   timeout: Wait timeout in seconds
    ...               Returns:
    ...                   dict: Cluster network data when ready
    Log    Waiting for cluster network ${name} to be ready
    ${cn}=    network_keywords.wait_for_cluster_network_ready    ${name}    ${timeout}
    RETURN    ${cn}

# VLAN Network Operations
Create VLAN Network
    [Arguments]    ${name}    ${vlan_id}    ${cluster_network}
    [Documentation]    Create VLAN network.
    ...               Args:
    ...                   name: Network name
    ...                   vlan_id: VLAN ID
    ...                   cluster_network: Cluster network name
    ...               Returns:
    ...                   dict: Network data
    Log    Creating VLAN network: ${name}
    ${network}=    network_keywords.create_vlan_network    ${name}    ${vlan_id}    ${cluster_network}
    RETURN    ${network}

Delete VLAN Network
    [Arguments]    ${name}
    [Documentation]    Delete VLAN network.
    ...               Args:
    ...                   name: Network name
    Log    Deleting VLAN network: ${name}
    network_keywords.delete_vlan_network    ${name}

# IP Pool Operations
Get IP Pool
    [Arguments]    ${name}
    [Documentation]    Get IP pool by name, returns None if not found.
    ...               Args:
    ...                   name: IP pool name
    ...               Returns:
    ...                   dict or None: IP pool data
    ${pool}=    network_keywords.get_ip_pool    ${name}
    RETURN    ${pool}

Create IP Pool
    [Arguments]    ${name}    ${subnet}    ${start_ip}    ${end_ip}    ${network_id}    ${gateway}
    [Documentation]    Create IP pool.
    ...               Args:
    ...                   name: IP pool name
    ...                   subnet: Subnet CIDR
    ...                   start_ip: Start IP address
    ...                   end_ip: End IP address
    ...                   network_id: Network ID
    ...                   gateway: Gateway IP
    ...               Returns:
    ...                   dict: IP pool data
    Log    Creating IP pool: ${name}
    ${pool}=    network_keywords.create_ip_pool    ${name}    ${subnet}    ${start_ip}    ${end_ip}    ${network_id}    ${gateway}
    RETURN    ${pool}

Delete IP Pool
    [Arguments]    ${name}
    [Documentation]    Delete IP pool.
    ...               Args:
    ...                   name: IP pool name
    Log    Deleting IP pool: ${name}
    network_keywords.delete_ip_pool    ${name}
