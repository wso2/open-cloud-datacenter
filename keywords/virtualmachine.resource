*** Settings ***
Documentation    VM Keywords
Library          Collections
Resource         variables.resource
Resource         common.resource
Library          ../libs/keywords/vm_keywords.py

*** Keywords ***
List VMs by prefix
    [Arguments]    ${prefix}    ${namespace}=default
    [Documentation]    List VMs whose names start with the given prefix
    ${vms}=    vm_keywords.list vms    namespace=${namespace}    name_prefix=${prefix}
    RETURN    ${vms}

VM is created
    [Arguments]    ${vm_name}    ${image_id}    ${cpu}=2    ${memory}=4Gi    &{config}
    [Documentation]    Create a virtual machine with provided name
    vm_keywords.create vm    ${vm_name}    ${image_id}    ${cpu}    ${memory}    &{config}

VM is deleted
    [Arguments]    ${vm_name}
    [Documentation]    Delete a VM
    vm_keywords.delete vm    ${vm_name}

Stopped VM is created
    [Arguments]    ${vm_name}    ${image_id}    ${cpu}=2    ${memory}=4Gi
    [Documentation]    Create a VM that does not boot (runStrategy=Halted)
    vm_keywords.create vm    ${vm_name}    ${image_id}    ${cpu}    ${memory}    run_strategy=Halted

VM is created with extra disks
    [Arguments]    ${vm_name}    ${image_id}    @{sizes}
    [Documentation]    Create a VM with additional blank data disks of the given sizes
    @{disks}=    Create List
    FOR    ${size}    IN    @{sizes}
        ${disk}=    Create Dictionary    size=${size}
        Append To List    ${disks}    ${disk}
    END
    vm_keywords.create vm    ${vm_name}    ${image_id}    2    4Gi    extra_disks=${disks}

VM condition should be
    [Arguments]    ${vm_name}    ${condition}    ${status}    ${match_field}=type
    [Documentation]    Wait until the VM's status.conditions reaches the expected
    ...    state. ${condition} is compared against the condition's `type` by
    ...    default; pass match_field=reason to match reason-only conditions
    ...    (e.g. GuestNotRunning, Unschedulable).
    ${result}=    vm_keywords.wait_for_vm_condition    ${vm_name}    ${condition}    ${status}
    ...    match_field=${match_field}
    RETURN    ${result}

VM is created with existing volume
    [Arguments]    ${vm_name}    ${image_id}    ${pvc_name}
    [Documentation]    Create a VM that attaches an already-existing PVC as a data disk
    @{existing}=    Create List    ${pvc_name}
    vm_keywords.create vm    ${vm_name}    ${image_id}    2    4Gi    existing_volumes=${existing}

VM Disk Count Should Be
    [Arguments]    ${vm_name}    ${expected}
    [Documentation]    Assert the VM declares the expected number of disks
    ${names}=    vm_keywords.get_vm_disk_names    ${vm_name}
    Length Should Be    ${names}    ${expected}

Add Volume To VM
    [Arguments]    ${vm_name}    ${disk_name}    ${volume_name}
    [Documentation]    Hot-plug an existing volume into a running VM
    vm_keywords.add_volume_to_vm    ${vm_name}    ${disk_name}    ${volume_name}

Remove Volume From VM
    [Arguments]    ${vm_name}    ${disk_name}
    [Documentation]    Hot-unplug a disk from a running VM
    vm_keywords.remove_volume_from_vm    ${vm_name}    ${disk_name}

Volume Should Be Hotplugged
    [Arguments]    ${vm_name}    ${disk_name}
    [Documentation]    Wait until the hot-plugged disk is Ready in the VMI volumeStatus
    vm_keywords.wait_for_vm_volume_hotplugged    ${vm_name}    ${disk_name}

Volume Should Be Unplugged
    [Arguments]    ${vm_name}    ${disk_name}
    [Documentation]    Wait until the disk is gone from the VMI volumeStatus
    vm_keywords.wait_for_vm_volume_unplugged    ${vm_name}    ${disk_name}

VM is started
    [Arguments]    ${vm_name}
    [Documentation]    Start a VM
    vm_keywords.start vm    ${vm_name}

VM is restarted
    [Arguments]    ${vm_name}
    [Documentation]    Restart a VM (stop then start)
    vm_keywords.restart vm    ${vm_name}

VM is soft-rebooted
    [Arguments]    ${vm_name}
    [Documentation]    Soft-reboot a running VM (guest-level reboot via
    ...    qemu-guest-agent), unlike restart which tears down and recreates the VMI
    vm_keywords.softreboot vm    ${vm_name}

VM qemu-agent should be disconnected
    [Arguments]    ${vm_name}    &{config}
    [Documentation]    Wait until the VM's guest agent disconnects (i.e. the
    ...    AgentConnected condition is removed), meaning the guest is
    ...    rebooting/leaving the OS.
    vm_keywords.wait_for_vm_agent_disconnected    ${vm_name}    &{config}

VM qemu-agent should be connected
    [Arguments]    ${vm_name}    &{config}
    [Documentation]    Wait until the VM's guest agent connects (i.e. the
    ...    AgentConnected condition appears). Right after VM creation/start
    ...    only Ready/LiveMigratable/StorageLiveMigratable exist yet, so this
    ...    must be polled rather than checked once. Returns the condition
    ...    dict, which includes `lastProbeTime`.
    ${condition}=    vm_keywords.wait_for_vm_agent_connected    ${vm_name}    &{config}
    RETURN    ${condition}

Agent probe time should be updated
    [Arguments]    ${old_probe_time}    ${new_probe_time}
    [Documentation]    Assert the guest agent's lastProbeTime advanced,
    ...    confirming the agent actually reconnected after a soft-reboot
    ...    rather than never having disconnected.
    vm_keywords.verify_agent_probe_time_updated    ${old_probe_time}    ${new_probe_time}

VM is paused
    [Arguments]    ${vm_name}
    [Documentation]    Pause a running VM
    vm_keywords.pause vm    ${vm_name}

VM is unpaused
    [Arguments]    ${vm_name}
    [Documentation]    Unpause a paused VM
    vm_keywords.unpause vm    ${vm_name}

VM should be paused
    [Arguments]    ${vm_name}
    [Documentation]    Wait for VM to reach the paused state
    vm_keywords.wait for vm paused    ${vm_name}

Try To Get VM
    [Arguments]    ${vm_name}
    [Documentation]    Attempt to get a VM expected to be missing. Returns result dict.
    ${result}=    vm_keywords.try_get_vm    ${vm_name}
    RETURN    ${result}

Try To Delete VM
    [Arguments]    ${vm_name}
    [Documentation]    Attempt to delete a VM expected to be missing. Returns result dict.
    ${result}=    vm_keywords.try_delete_vm    ${vm_name}
    RETURN    ${result}

VM is stopped
    [Arguments]    ${vm_name}
    [Documentation]    Stop a VM
    vm_keywords.stop vm    ${vm_name}

VM should be running
    [Arguments]    ${vm_name}
    [Documentation]    Wait for VM to reach running state
    vm_keywords.wait for vm running    ${vm_name}

VM should be stopped
    [Arguments]    ${vm_name}
    [Documentation]    Wait for VM to stop
    vm_keywords.wait for vm stopped    ${vm_name}

VM should be deleted
    [Arguments]    ${vm_name}
    [Documentation]    Wait for VM to be deleted
    vm_keywords.wait for vm deleted    ${vm_name}

VM should have IP addresses
    [Arguments]    ${vm_name}    ${networks}=${None}
    [Documentation]    Wait for VM to obtain IP addresses
    vm_keywords.wait for vm ip addresses    ${vm_name}    ${networks}

Get VM node
    [Arguments]    ${vm_name}
    [Documentation]    Return the node currently running the VM (the VMI's
    ...    status.nodeName), or None when the VM is not running.
    ${node}=    vm_keywords.get_vm_node    ${vm_name}
    RETURN    ${node}

VM should be running on node
    [Arguments]    ${vm_name}    ${expected_node}
    [Documentation]    Assert the VM is currently running on ${expected_node}
    vm_keywords.verify_vm_on_node    ${vm_name}    ${expected_node}

VM should be moved off node
    [Arguments]    ${vm_name}    ${original_node}    ${timeout}=${WAIT_TIMEOUT}
    [Documentation]    Wait until the VM is Running on any node other than
    ...    ${original_node}, without requesting a specific destination. Used when
    ...    something else (e.g. the descheduler) decides where the VM lands.
    ...    Returns the node the VM ended up on.
    ${node}=    vm_keywords.wait_for_vm_migrated_away_from    ${vm_name}    ${original_node}
    ...    timeout=${timeout}
    RETURN    ${node}

Get VM annotations
    [Arguments]    ${vm_name}
    [Documentation]    Return the VM object's metadata.annotations as a dictionary
    ${annotations}=    vm_keywords.get_vm_annotations    ${vm_name}
    RETURN    ${annotations}

VM annotation should be
    [Arguments]    ${vm_name}    ${key}    ${value}
    [Documentation]    Assert the VM carries an annotation with the expected value.
    ...    Controllers set annotations asynchronously, so this polls.
    Wait Until Keyword Succeeds    ${WAIT_TIMEOUT}    ${RETRY_INTERVAL}
    ...    VM Annotation Value Is    ${vm_name}    ${key}    ${value}

VM Annotation Value Is
    [Arguments]    ${vm_name}    ${key}    ${value}
    [Documentation]    Single-shot assertion backing `VM annotation should be`
    ${annotations}=    Get VM annotations    ${vm_name}
    Dictionary Should Contain Item    ${annotations}    ${key}    ${value}
    ...    msg=VM ${vm_name} annotation ${key} should be '${value}'

VM annotation should be absent
    [Arguments]    ${vm_name}    ${key}
    [Documentation]    Assert the VM does not carry the given annotation.
    ...    Controllers remove annotations asynchronously, so this polls.
    Wait Until Keyword Succeeds    ${WAIT_TIMEOUT}    ${RETRY_INTERVAL}
    ...    VM Annotation Is Not Present    ${vm_name}    ${key}

VM Annotation Is Not Present
    [Arguments]    ${vm_name}    ${key}
    [Documentation]    Single-shot assertion backing `VM annotation should be absent`
    ${annotations}=    Get VM annotations    ${vm_name}
    Dictionary Should Not Contain Key    ${annotations}    ${key}
    ...    msg=VM ${vm_name} should not carry annotation ${key}

Data is written to VM
    [Arguments]    ${vm_name}    ${data_size_mb}=100
    [Documentation]    Write data to VM and return checksum
    ${checksum}=    vm_keywords.write vm data    ${vm_name}    ${data_size_mb}
    RETURN    ${checksum}

Get VM data checksum
    [Arguments]    ${vm_name}
    [Documentation]    Get checksum of data in VM
    ${checksum}=    vm_keywords.get vm data checksum    ${vm_name}
    RETURN    ${checksum}

Update Volume Size to ${new_size}Gi From VM
    [Arguments]    ${vm_name}    ${disk_name}
    [Documentation]    Update the size of a VM disk
    vm_keywords.update_vm_disk_size    ${vm_name}    ${disk_name}    ${new_size}Gi

Execute Command In VM
    [Arguments]    ${vm_name}    ${command}    ${username}    ${pkey}=${None}
    [Documentation]    Execute a command inside the VM's guest OS over SSH,
    ...    using the Harvester host node running the VM as a jump host
    ...    (see SSH_PRIVATE_KEY in variables.resource).
    ...    Returns the combined stdout and stderr as a single value.
    ${output}=    vm_keywords.exec_command_in_vm    ${vm_name}    ${command}
    ...    ${username}    pkey=${pkey}
    RETURN    ${output}

VM cloud-init should be done
    [Arguments]    ${vm_name}    ${username}    ${pkey}=${None}    ${timeout}=${WAIT_TIMEOUT}
    [Documentation]    Poll `cloud-init status` inside the VM guest over SSH
    ...    until it reports done.
    vm_keywords.wait_for_vm_cloudinit_done    ${vm_name}    ${username}
    ...    pkey=${pkey}    timeout=${timeout}

Get VM CPU Cores
    [Arguments]    ${vm_name}
    [Documentation]    Get the VM spec's requested CPU core count
    ${cores}=    vm_keywords.get_vm_cpu_cores    ${vm_name}
    RETURN    ${cores}

VM CPU is updated to ${cpu_cores}
    [Arguments]    ${vm_name}
    [Documentation]    Update the VM's CPU core count
    vm_keywords.update_vm_cpu_cores    ${vm_name}    ${cpu_cores}

