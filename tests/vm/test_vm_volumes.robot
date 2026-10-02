*** Settings ***
Documentation    VM With Multiple Disk Volumes Test Cases
...    Port of harvester/tests test_3_vm_functions.py TestVMWithVolumes
...    (two volumes, existing volume). Verified via VM spec disk count (CRD-only).
Test Tags        virtualmachines    volumes    pr-baseline

Resource    ../../keywords/variables.resource
Resource    ../../keywords/common.resource
Resource    ../../keywords/image.resource
Resource    ../../keywords/volume.resource
Resource    ../../keywords/virtualmachine.resource

Suite Setup       Local Suite Setup
Suite Teardown    Local Suite Teardown
Test Teardown     Common Test Teardown


*** Variables ***
# Dynamic Variables
${IMG_NAME}    ${EMPTY}
${VM_TWO}      ${EMPTY}
${VM_EXIST}    ${EMPTY}
${DATA_VOL}    ${EMPTY}


*** Test Cases ***
Create VM With Two Volumes
    [Tags]    p0    smoke
    [Documentation]    A VM created with two extra data disks must run and report
    ...    3 disks total (boot + 2 data).
    VM is created with extra disks    ${VM_TWO}    ${IMG_NAME}    1Gi    2Gi
    VM should be running    ${VM_TWO}
    VM Disk Count Should Be    ${VM_TWO}    3

Create VM With Existing Volume
    [Tags]    p0
    [Documentation]    A VM that attaches a pre-existing PVC must run and report
    ...    2 disks total (boot + existing data).
    Create Volume    ${DATA_VOL}    5Gi
    Wait Until Volume Is Active    ${DATA_VOL}
    VM is created with existing volume    ${VM_EXIST}    ${IMG_NAME}    ${DATA_VOL}
    VM should be running    ${VM_EXIST}
    VM Disk Count Should Be    ${VM_EXIST}    2


*** Keywords ***
Local Suite Setup
    ${suffix}=    Generate Unique Name
    Set Suite Variable    ${IMG_NAME}    img-${suffix}
    Set Suite Variable    ${VM_TWO}      vm-two-${suffix}
    Set Suite Variable    ${VM_EXIST}    vm-exist-${suffix}
    Set Suite Variable    ${DATA_VOL}    vol-${suffix}
    Set up test environment
    Image is available for VM creation    ${IMG_NAME}    ${OPENSUSE_IMAGE_URL}

Local Suite Teardown
    # Parallel-safe: only delete this suite's own named resources.
    Run Keyword If All Tests Passed    Delete Suite Resources
    Run Keyword If Any Tests Failed    Log Variables

Delete Suite Resources
    Run Keyword And Ignore Error    VM is deleted    ${VM_TWO}
    Run Keyword And Ignore Error    VM is deleted    ${VM_EXIST}
    Run Keyword And Ignore Error    Delete Volume    ${DATA_VOL}
    Run Keyword And Ignore Error    Delete image by name    ${IMG_NAME}
