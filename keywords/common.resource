*** Settings ***
Documentation    Common Keywords
Library          ../libs/keywords/common_keywords.py
Resource         variables.resource


*** Keywords ***
Set up test environment
    [Documentation]    Initialize test environment
    Log    Setting up test environment
    common_keywords.init harvester api client    ${HARVESTER_ENDPOINT}    ${HARVESTER_USERNAME}    ${HARVESTER_PASSWORD}
    common_keywords.init k8s api client

Generate Unique Name
    [Arguments]    ${prefix1}=${EMPTY}    ${prefix2}=${EMPTY}    ${precise}=${TRUE}
    [Documentation]    Generate a name with timestamp suffix and optional prefixes.
    ...  The default `%m%d%H%M%S%f` timestamp has sub-second precision so
    ...  concurrent pabot suites never collide (colliding names also make their
    ...  local teardowns delete each other's resources). Pass precise=${FALSE}
    ...  to get the short `%m%d%H%M` form ONLY for resources with tight name
    ...  length limits (e.g. cluster networks).
    ...  Examples:
    ...  Generate Unique Name            -> '0618101519681156'
    ...  Generate Unique Name  foo       -> 'foo-0618101519681156'
    ...  Generate Unique Name  foo  bar  precise=${FALSE}  -> 'foo-bar-06181015'
    ${name}=    common_keywords.generate_name_with_suffix    ${prefix1}    ${prefix2}    ${precise}
    RETURN    ${name}

Cluster Version Is At Least
    [Arguments]    ${version}
    [Documentation]    True if the cluster release is >= the given version
    ...  (e.g. '1.9.0'). Versionless dev builds (master/commit-hash) are
    ...  treated as the newest release. Use this to pick version-dependent
    ...  expectations.
    ${result}=    common_keywords.cluster_version_is_at_least    ${version}
    RETURN    ${result}

Generate SSH Keypair
    [Documentation]    Generate a fresh RSA keypair. The public key can be
    ...    passed as `ssh_public_key` to `VM is created` (injected into the
    ...    VM's cloud-init ssh_authorized_keys); the private key can then be
    ...    passed as `pkey` to `Execute Command In VM`.
    ...    Returns: (public_key, private_key)
    ${public_key}    ${private_key}=    common_keywords.generate_ssh_keypair
    RETURN    ${public_key}    ${private_key}

Cleanup test resources
    [Documentation]    Clean up all resources created during tests
    Log    Cleaning up test resources
    Run Keyword And Ignore Error    common_keywords.cleanup_vms
    Run Keyword And Ignore Error    common_keywords.cleanup_volumes
    Run Keyword And Ignore Error    common_keywords.cleanup_images
    Run Keyword And Ignore Error    common_keywords.cleanup_storageclasses
    Run Keyword And Ignore Error    common_keywords.cleanup_networks
    Run Keyword And Ignore Error    common_keywords.cleanup_backups
    Run Keyword And Ignore Error    common_keywords.cleanup_namespaces
    Run Keyword And Ignore Error    common_keywords.cleanup_projects

Common Suite Teardown
    Run Keyword If All Tests Passed    Cleanup test resources
    Run Keyword If Any Tests Failed    Log Variables

Common Test Teardown
    Run Keyword If Test Failed    Log Variables

# Generic negative-result assertions (operate on a {success, code, message} dict
# returned by any "Try To ..." keyword, regardless of resource type).
Operation Should Be Rejected
    [Arguments]    ${result}    @{expected_messages}
    [Documentation]    Assert a {success, code, message} result was rejected with a client
    ...    error (400/403/409/422). If expected_messages are given, the message must
    ...    contain at least one of them (case-insensitive).
    Should Not Be True    ${result}[success]
    ...    msg=Operation was expected to fail but succeeded (code=${result}[code])
    Should Be True    ${result}[code] in (400, 403, 409, 422)
    ...    msg=Expected a client error 4xx but got ${result}[code]
    IF    ${expected_messages}
        ${msg_lower}=    Convert To Lower Case    ${result}[message]
        ${lowered}=    Evaluate    [m.lower() for m in $expected_messages]
        Should Contain Any    ${msg_lower}    @{lowered}
        ...    msg=Expected message to contain one of ${expected_messages}, got: ${result}[message]
    END

Operation Should Be Not Found
    [Arguments]    ${result}
    [Documentation]    Assert a {success, code, message} result was a 404 NotFound
    Should Not Be True    ${result}[success]
    ...    msg=Operation was expected to return NotFound but succeeded (code=${result}[code])
    Should Be Equal As Integers    ${result}[code]    404
    ...    msg=Expected 404 but got ${result}[code]
    ${msg_lower}=    Convert To Lower Case    ${result}[message]
    Should Contain    ${msg_lower}    notfound
    ...    msg=Expected a NotFound error, got: ${result}[message]

List Pods By Label
    [Arguments]    ${namespace}    ${label_selector}   ${status}=${None}
    [Documentation]    Get pods by label selector
    ${pods}=    common_keywords.list_pods_by_label    ${namespace}    ${label_selector}    ${status}
    RETURN    ${pods}

Count Pods By Label
    [Arguments]    ${namespace}    ${label_selector}   ${status}=${None}
    [Documentation]    Get pods by label selector
    ${pods}=    common_keywords.list_pods_by_label    ${namespace}    ${label_selector}    ${status}
    ${count}=   Get Length    ${pods}
    RETURN    ${count}
