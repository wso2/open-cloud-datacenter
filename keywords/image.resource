*** Settings ***
Documentation    Image Keywords
Resource         variables.resource
Resource         common.resource
Library          ../libs/keywords/image_keywords.py

*** Keywords ***
Create image from url with name
    [Arguments]    ${image_name}    ${image_url}    ${storage_class}=${EMPTY}    ${checksum}=    ${retry}=${NONE}
    [Documentation]    Create image from URL using provided name and optional storage class.
    ...    retry overrides spec.retry (import retry limit, cluster default 3);
    ...    retry=${0} makes a doomed import fail on the first attempt.
    image_keywords.create image from url    ${image_name}    ${image_url}    storage_class=${storage_class}    checksum=${checksum}    retry=${retry}

Wait for image downloaded by name
    [Arguments]    ${image_name}    ${timeout}=${WAIT_TIMEOUT}
    [Documentation]    Wait for image download to complete
    image_keywords.wait for image downloaded    ${image_name}    ${timeout}

Delete image by name
    [Arguments]    ${image_name}    ${namespace}=${DEFAULT_NAMESPACE}
    [Documentation]    Delete an image
    image_keywords.delete image    ${image_name}    ${namespace}

Get image status by name
    [Arguments]    ${image_name}    ${namespace}=${DEFAULT_NAMESPACE}
    [Documentation]    Get image status
    ${status}=    image_keywords.get image status    ${image_name}    ${namespace}
    RETURN    ${status}

Image is available for VM creation
    [Arguments]    ${image_name}    ${image_url}
    [Documentation]    Create an image from URL (delegates to Image module).
    Create image from url with name    ${image_name}    ${image_url}
    Wait for image downloaded by name    ${image_name}

Wait for image deleted by name
    [Arguments]    ${image_name}    ${timeout}=${WAIT_TIMEOUT}
    [Documentation]    Wait for the image to be fully removed
    image_keywords.wait for image deleted    ${image_name}    ${timeout}

List images
    [Arguments]    ${namespace}=${DEFAULT_NAMESPACE}
    ${images}=    image_keywords.list images    ${namespace}
    RETURN    ${images}

Image Should Be Listed
    [Arguments]    ${image_name}    ${namespace}=${DEFAULT_NAMESPACE}
    ${images}=    image_keywords.list images    ${namespace}
    ${names}=    Evaluate    [i['metadata']['name'] for i in $images]
    Should Contain    ${names}    ${image_name}

Image Should Exist
    [Arguments]    ${image_name}    ${namespace}=${DEFAULT_NAMESPACE}
    ${exists}=    image_keywords.image exists    ${image_name}    ${namespace}
    Should Be True    ${exists}

Image State Is ${state}
    [Arguments]    ${image_name}
    ${status}=    image_keywords.get image status    ${image_name}
    Should Be Equal As Strings    ${status["state"]}    ${state}

Image Size Is Greater Than Zero
    [Arguments]    ${image_name}
    ${status}=    image_keywords.get image status    ${image_name}
    ${size}=    Convert To Integer    ${status["size"]}
    Should Be True    ${size} > 0

Image Export Should Have Started
    [Arguments]    ${image_name}
    [Documentation]    Assert the image controller has begun processing the image
    ...    (its state is no longer Unknown, i.e. status conditions/progress exist)
    ${status}=    image_keywords.get image status    ${image_name}
    Should Not Be Equal As Strings    ${status["state"]}    Unknown
    ...    msg=Image ${image_name} has not been picked up by the image controller yet

Wait Until Image Export Has Started
    [Arguments]    ${image_name}
    [Documentation]    Wait until the image controller has started processing the
    ...    export image. The PVC webhook rejects deleting an exporting volume by
    ...    looking the image up in its informer cache, so a delete issued right
    ...    after creating the image can race the cache and incorrectly succeed.
    ...    Once the controller has stamped the image status, the image has
    ...    propagated through the informer caches and the protection is in effect.
    Wait Until Keyword Succeeds    ${WAIT_TIMEOUT}    ${RETRY_INTERVAL}
    ...    Image Export Should Have Started    ${image_name}

# negative
Try To Create Image
    [Arguments]    ${image_name}    ${image_url}=${EMPTY}    ${source_type}=download    ${checksum}=${EMPTY}
    [Documentation]    Attempt to create an image expected to be rejected.
    ...    Returns a result dict {success, code, message}; never fails on rejection.
    ${result}=    image_keywords.try_create_image    ${image_name}    ${image_url}    ${source_type}    ${checksum}
    RETURN    ${result}

Try To Get Image
    [Arguments]    ${image_name}
    [Documentation]    Attempt to get an image expected to be missing. Returns result dict.
    ${result}=    image_keywords.try_get_image    ${image_name}
    RETURN    ${result}

Try To Delete Image
    [Arguments]    ${image_name}
    [Documentation]    Attempt to delete an image expected to be missing. Returns result dict.
    ${result}=    image_keywords.try_delete_image    ${image_name}
    RETURN    ${result}

# update
Update Image Metadata
    [Arguments]    ${image_name}    ${metadata}
    [Documentation]    Patch an image's metadata (a dict with labels/annotations)
    image_keywords.update_image    ${image_name}    ${metadata}

Image Label Should Be
    [Arguments]    ${image_name}    ${key}    ${expected}
    ${metadata}=    image_keywords.get_image_metadata    ${image_name}
    Dictionary Should Contain Item    ${metadata}[labels]    ${key}    ${expected}

Image Annotation Should Be
    [Arguments]    ${image_name}    ${key}    ${expected}
    ${metadata}=    image_keywords.get_image_metadata    ${image_name}
    Dictionary Should Contain Item    ${metadata}[annotations]    ${key}    ${expected}

Get Image By Name
    [Arguments]    ${image_name}
    [Documentation]    Get a single image's metadata and assert its name matches
    ${metadata}=    image_keywords.get_image_metadata    ${image_name}
    Should Be Equal As Strings    ${metadata}[name]    ${image_name}