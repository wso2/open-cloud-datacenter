*** Settings ***
Documentation    Volume Keywords
Resource         variables.resource
Resource         common.resource
Library          ../libs/keywords/volume_keywords.py

*** Keywords ***
# size
Get Volume Size
    [Arguments]    ${vol_name}
    ${status}=    volume_keywords.get_volume_status    ${vol_name}
    RETURN    ${status["capacity"]["storage"]}

Volume Size Is ${size_gi}Gi
    [Arguments]    ${vol_name}
    ${actual_size}=    Get Volume Size    ${vol_name}
    Should Be Equal As Strings    ${actual_size}    ${size_gi}Gi

Update Volume Size to ${new_size}Gi
    [Arguments]    ${vol_name}
    volume_keywords.expand_volume    ${vol_name}    ${new_size}Gi

# status
Volume Phase Is ${phase}
    [Arguments]    ${vol_name}
    ${status}=    volume_keywords.get_volume_status    ${vol_name}
    Should Be Equal As Strings    ${status["phase"]}    ${phase}

Volume State Is Ready/In-use
    [Arguments]    ${vol_name}
    ${status}=    volume_keywords.get_volume_status    ${vol_name}
    Should Be Empty    ${status["conditions"]}

Wait Until Volume Is Active
    [Arguments]    ${vol_name}
    Sleep    ${SLEEP_TIMEOUT}s    reason=Buffer time for volume changes
    Wait Until Keyword Succeeds    ${WAIT_TIMEOUT}    ${RETRY_INTERVAL}
        ...    Volume Phase Is Bound    ${vol_name}
    Wait Until Keyword Succeeds    ${WAIT_TIMEOUT}    ${RETRY_INTERVAL}
        ...    Volume State Is Ready/In-use    ${vol_name}

# lifecycle
Create Volume
    [Arguments]    ${vol_name}    ${size}=${DEFAULT_VOLUME_SIZE}    ${storage_class}=${DEFAULT_STORAGE_CLASS}    ${replicas}=${DEFAULT_VM_REPLICAS}    ${access_mode}=ReadWriteMany
    [Documentation]    Create a volume (PVC) backed by the given storage class.
    ...    access_mode defaults to ReadWriteMany, which is what Harvester uses for VM
    ...    volumes. Pass ReadWriteOnce to produce a volume that makes any VM attaching
    ...    it non-live-migratable.
    volume_keywords.create_volume    ${vol_name}    ${size}    ${replicas}    blockdev    storage_class=${storage_class}    access_mode=${access_mode}

Create ${size_gi}Gi Volume
    [Arguments]    ${vol_name}
    Create Volume    ${vol_name}    ${size_gi}Gi

Create Volumes Concurrently
    [Arguments]    ${vol_names}    ${size}=10Gi
    [Documentation]    Create the given volumes in parallel and wait for each to bind.
    ...    Returns a list of {name, success, error} result dicts.
    ${results}=    volume_keywords.create_volumes_concurrently    ${vol_names}    ${size}
    RETURN    ${results}

Delete Volumes Concurrently
    [Arguments]    ${vol_names}
    [Documentation]    Delete the given volumes in parallel and wait for each to be gone.
    ...    Returns a list of {name, success, error} result dicts.
    ${results}=    volume_keywords.delete_volumes_concurrently    ${vol_names}
    RETURN    ${results}

All Volume Operations Should Succeed
    [Arguments]    ${results}
    [Documentation]    Assert every concurrent volume operation result succeeded
    FOR    ${r}    IN    @{results}
        Should Be True    ${r}[success]    msg=Volume ${r}[name] failed: ${r}[error]
    END

Create Volume From Image
    [Arguments]    ${vol_name}    ${image_name}    ${size}=${None}
    [Documentation]    Create a volume (PVC) backed by a VM image. Size defaults to the
    ...    image's virtual size (rounded up to whole GiB) when not given.
    volume_keywords.create_volume_from_image    ${vol_name}    ${image_name}    ${size}

Volume Image Id Should Be
    [Arguments]    ${vol_name}    ${expected_image_id}
    [Documentation]    Assert the volume's harvesterhci.io/imageId annotation matches
    ${image_id}=    volume_keywords.get_volume_image_id    ${vol_name}
    Should Be Equal As Strings    ${image_id}    ${expected_image_id}
    ...    msg=Expected volume ${vol_name} to be backed by ${expected_image_id}, got '${image_id}'

Delete Volume
    [Arguments]    ${vol_name}
    volume_keywords.delete_volume    ${vol_name}

Wait Until Volume Is Deleted
    [Arguments]    ${vol_name}
    volume_keywords.wait_for_volume_deleted    ${vol_name}

# negative
Try To Create Volume
    [Arguments]    ${vol_name}    ${size}    ${replicas}=${DEFAULT_VM_REPLICAS}
    [Documentation]    Attempt to create a volume that is expected to be rejected.
    ...    Returns a result dict {success, code, message}; never fails on rejection.
    ${result}=    volume_keywords.try_create_volume    ${vol_name}    ${size}    ${replicas}
    RETURN    ${result}

Volume Should Not Exist
    [Arguments]    ${vol_name}
    [Documentation]    Assert the volume is absent (e.g. after a rejected creation)
    ${exists}=    volume_keywords.volume_exists    ${vol_name}
    Should Not Be True    ${exists}
    ...    msg=Volume ${vol_name} should not exist after a failed creation

Try To Get Volume
    [Arguments]    ${vol_name}
    [Documentation]    Attempt to get a volume expected to be missing.
    ...    Returns a result dict {success, code, message}; never fails on 404.
    ${result}=    volume_keywords.try_get_volume    ${vol_name}
    RETURN    ${result}

Try To Delete Volume
    [Arguments]    ${vol_name}
    [Documentation]    Attempt to delete a volume expected to be missing.
    ...    Returns a result dict {success, code, message}; never fails on 404.
    ${result}=    volume_keywords.try_delete_volume    ${vol_name}
    RETURN    ${result}

Try To Resize Volume
    [Arguments]    ${vol_name}    ${new_size}
    [Documentation]    Attempt to resize a volume (e.g. a shrink expected to be rejected).
    ...    Returns a result dict {success, code, message}; never fails on rejection.
    ${result}=    volume_keywords.try_expand_volume    ${vol_name}    ${new_size}
    RETURN    ${result}

Export Volume To Image
    [Arguments]    ${vol_name}    ${image_name}    ${target_storage_class}=${DEFAULT_STORAGE_CLASS}
    [Documentation]    Start exporting a volume to a VM image (sourceType=export-from-volume).
    ...    The export runs asynchronously; the source volume is in use while it proceeds.
    volume_keywords.export_volume_to_image    ${vol_name}    ${image_name}    ${target_storage_class}

Volume Is Present
    [Arguments]    ${vol_name}
    ${status}=    volume_keywords.get_volume_status    ${vol_name}
    Should Not Be Empty    ${status}

Volume Should Be Listed
    [Arguments]    ${vol_name}
    ${volumes}=    volume_keywords.list_volumes
    ${names}=    Evaluate    [v['metadata']['name'] for v in $volumes]
    Should Contain    ${names}    ${vol_name}

# snapshot
Create Volume Snapshot
    [Arguments]    ${vol_name}    ${snapshot_name}    ${snapshot_class}=longhorn-snapshot
    volume_keywords.create_volume_snapshot    ${vol_name}    ${snapshot_name}    ${snapshot_class}

Wait Until Snapshot Is Ready
    [Arguments]    ${snapshot_name}
    volume_keywords.wait_for_snapshot_ready    ${snapshot_name}

Delete Volume Snapshot
    [Arguments]    ${vol_name}    ${snapshot_name}
    volume_keywords.delete_volume_snapshot    ${vol_name}    ${snapshot_name}

Restore Volume From Snapshot
    [Arguments]    ${vol_name}    ${snapshot_name}    ${new_vol_name}
    volume_keywords.restore_volume_from_snapshot    ${vol_name}    ${snapshot_name}    ${new_vol_name}
