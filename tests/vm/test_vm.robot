*** Settings ***
Documentation    VM Test Cases: basic lifecycle, create-stopped, and volume hot-plug.
...    All groups share one image (created once in suite setup); each group uses
...    its own VM so they stay independent.
Test Tags        regression    virtualmachines    pr-baseline

Resource         ../../keywords/variables.resource
Resource         ../../keywords/common.resource
Resource         ../../keywords/image.resource
Resource         ../../keywords/volume.resource
Resource         ../../keywords/virtualmachine.resource


Suite Setup       Local Suite Setup
Test Teardown     Common Test Teardown
Suite Teardown    Local Suite Teardown


*** Variables ***
${HP_DISK}       disk-hot-plug
# Dynamic Variables
${IMAGE_NAME}    ${EMPTY}
${VM_BASIC}      ${EMPTY}
${VM_STOPPED}    ${EMPTY}
${VM_HP}         ${EMPTY}
${DATA_VOL}      ${EMPTY}


*** Test Cases ***
Test VM Basic Lifecycle
    [Tags]    coretest    p0
    [Documentation]    Test basic VM creation, start, stop, delete operations
    When VM is created    ${VM_BASIC}    ${IMAGE_NAME}
    Then VM should be running    ${VM_BASIC}
    And VM should have IP addresses    ${VM_BASIC}    ${DEFAULT_NAMESPACE}
    When VM is stopped    ${VM_BASIC}
    Then VM should be stopped    ${VM_BASIC}
    When VM is started    ${VM_BASIC}
    Then VM should be running    ${VM_BASIC}
    When VM is deleted    ${VM_BASIC}
    Then VM should be deleted    ${VM_BASIC}

Create Stopped VM Stays Stopped
    [Tags]    p0    sanity
    [Documentation]    A VM created with runStrategy=Halted must not boot
    Stopped VM is created    ${VM_STOPPED}    ${IMAGE_NAME}
    VM should be stopped    ${VM_STOPPED}

Start Stopped VM
    [Tags]    p0
    [Documentation]    Starting the stopped VM brings it to running
    VM is started    ${VM_STOPPED}
    VM should be running    ${VM_STOPPED}

Hot-Plug Volume Into Running VM
    [Tags]    p0    smoke    volumes
    [Documentation]    Hot-plug an existing volume into a running VM; it must
    ...    appear Ready in the VMI volumeStatus.
    Given VM is created    ${VM_HP}    ${IMAGE_NAME}
    And VM should be running    ${VM_HP}
    And Create Volume    ${DATA_VOL}    1Gi
    And Wait Until Volume Is Active    ${DATA_VOL}
    When Add Volume To VM    ${VM_HP}    ${HP_DISK}    ${DATA_VOL}
    Then Volume Should Be Hotplugged    ${VM_HP}    ${HP_DISK}

Hot-Unplug Volume From Running VM
    [Tags]    p0    smoke    volumes
    [Documentation]    Hot-unplug the disk; it must disappear from the VMI volumeStatus.
    When Remove Volume From VM    ${VM_HP}    ${HP_DISK}
    Then Volume Should Be Unplugged    ${VM_HP}    ${HP_DISK}


*** Keywords ***
Local Suite Setup
    ${suffix}=    Generate Unique Name
    Set Suite Variable    ${IMAGE_NAME}    image-${suffix}
    Set Suite Variable    ${VM_BASIC}      vm-basic-${suffix}
    Set Suite Variable    ${VM_STOPPED}    vm-stopped-${suffix}
    Set Suite Variable    ${VM_HP}         vm-hp-${suffix}
    Set Suite Variable    ${DATA_VOL}      vol-${suffix}
    Set up test environment
    # One shared image for all VM groups
    Image is available for VM creation    ${IMAGE_NAME}    ${OPENSUSE_IMAGE_URL}

Local Suite Teardown
    # Parallel-safe: only delete this suite's own named resources (never a global
    # label sweep, which would remove VMs/images owned by sibling suites under pabot).
    Run Keyword If All Tests Passed    Delete Suite Resources
    Run Keyword If Any Tests Failed    Log Variables

Delete Suite Resources
    Run Keyword And Ignore Error    VM is deleted    ${VM_BASIC}
    Run Keyword And Ignore Error    VM is deleted    ${VM_STOPPED}
    Run Keyword And Ignore Error    VM is deleted    ${VM_HP}
    Run Keyword And Ignore Error    Delete Volume    ${DATA_VOL}
    Run Keyword And Ignore Error    Delete image by name    ${IMAGE_NAME}
