*** Settings ***
Documentation    Namespace Keywords - Reusable keywords for namespace (tenancy) operations
Resource         variables.resource
Resource         common.resource
Library          ../libs/keywords/namespace_keywords.py
Library          ../libs/keywords/project_keywords.py

*** Keywords ***
Namespace is created
    [Arguments]    ${namespace_name}
    [Documentation]    Create a namespace that is not bound to any project.
    ${name}=    namespace_keywords.create namespace    ${namespace_name}
    RETURN    ${name}

Namespace is created in project
    [Arguments]    ${namespace_name}    ${project_name}    ${cpu_limit}=${NONE}    ${memory_limit}=${NONE}
    [Documentation]    Create a namespace inside a project, optionally with its own
    ...    CPU / memory limits (must not exceed the project quota).
    Log    Creating namespace ${namespace_name} in project ${project_name} (cpu=${cpu_limit}, memory=${memory_limit})
    ${name}=    namespace_keywords.create namespace    ${namespace_name}
    ...    project_name=${project_name}    cpu_limit=${cpu_limit}    memory_limit=${memory_limit}
    RETURN    ${name}

Namespace should exist
    [Arguments]    ${namespace_name}
    [Documentation]    Assert the namespace exists
    ${exists}=    namespace_keywords.namespace exists    ${namespace_name}
    Should Be True    ${exists}    msg=Namespace ${namespace_name} does not exist

Namespace should not exist
    [Arguments]    ${namespace_name}
    [Documentation]    Assert the namespace does not exist
    ${exists}=    namespace_keywords.namespace exists    ${namespace_name}
    Should Not Be True    ${exists}    msg=Namespace ${namespace_name} still exists

Namespace should be active
    [Arguments]    ${namespace_name}    ${timeout}=${WAIT_TIMEOUT}
    [Documentation]    Wait until the namespace phase is Active
    namespace_keywords.wait for namespace active    ${namespace_name}    ${timeout}

Get Namespace Resource Limits
    [Arguments]    ${namespace_name}
    [Documentation]    Get the CPU and memory limits configured on a namespace.
    ...    Returns: (cpu_limit, memory_limit) as strings
    ${cpu}    ${memory}=    namespace_keywords.get namespace resource limits    ${namespace_name}
    RETURN    ${cpu}    ${memory}

Namespace should belong to project
    [Arguments]    ${namespace_name}    ${project_name}
    [Documentation]    Assert the namespace is bound to the given project
    ${expected_id}=    project_keywords.get project id    ${project_name}
    ${actual_id}=    namespace_keywords.get namespace project    ${namespace_name}
    Should Be Equal As Strings    ${actual_id}    ${expected_id}
    ...    msg=Namespace ${namespace_name} is bound to '${actual_id}', expected '${expected_id}'

List Namespaces
    ${namespaces}=    namespace_keywords.list namespaces
    RETURN    ${namespaces}

Wait for namespace deleted
    [Arguments]    ${namespace_name}    ${timeout}=${WAIT_TIMEOUT}
    [Documentation]    Wait for the namespace to be fully removed
    namespace_keywords.wait for namespace deleted    ${namespace_name}    ${timeout}

Namespace is deleted
    [Arguments]    ${namespace_name}    ${timeout}=${WAIT_TIMEOUT}
    [Documentation]    Delete the namespace and wait until it is gone
    namespace_keywords.delete namespace    ${namespace_name}
    namespace_keywords.wait for namespace deleted    ${namespace_name}    ${timeout}

# negative
Try To Create Namespace
    [Arguments]    ${namespace_name}    ${project_name}=${NONE}    ${cpu_limit}=${NONE}    ${memory_limit}=${NONE}
    [Documentation]    Attempt to create a namespace expected to be rejected.
    ...    Returns a result dict {success, code, message}; never fails on rejection.
    ${result}=    namespace_keywords.try create namespace    ${namespace_name}
    ...    project_name=${project_name}    cpu_limit=${cpu_limit}    memory_limit=${memory_limit}
    RETURN    ${result}

Try To Delete Namespace
    [Arguments]    ${namespace_name}
    [Documentation]    Attempt to delete a namespace expected to be missing. Returns result dict.
    ${result}=    namespace_keywords.try delete namespace    ${namespace_name}
    RETURN    ${result}
