*** Settings ***
Documentation    Project Keywords - Reusable keywords for project (tenancy) operations
Resource         variables.resource
Resource         common.resource
Library          ../libs/keywords/project_keywords.py

*** Keywords ***
Project is created
    [Arguments]    ${project_name}
    [Documentation]    Create a project without any resource limits.
    ...    Returns the generated project id (p-xxxxx).
    ${project_id}=    project_keywords.create project    ${project_name}
    RETURN    ${project_id}

Project is created with resource limits
    [Arguments]    ${project_name}    ${cpu_limit}=${NONE}    ${memory_limit}=${NONE}
    ...    ${ns_default_cpu}=${NONE}    ${ns_default_memory}=${NONE}
    [Documentation]    Create a project with CPU / memory limits.
    ...    The per-namespace default quota falls back to the project limit unless
    ...    ns_default_cpu / ns_default_memory are given (Rancher requires it to
    ...    cover the same keys as the project quota).
    ...    Returns the generated project id (p-xxxxx).
    Log    Creating project ${project_name} (cpu=${cpu_limit}, memory=${memory_limit})
    ${project_id}=    project_keywords.create project    ${project_name}
    ...    cpu_limit=${cpu_limit}    memory_limit=${memory_limit}
    ...    ns_default_cpu=${ns_default_cpu}    ns_default_memory=${ns_default_memory}
    RETURN    ${project_id}

Project should exist
    [Arguments]    ${project_name}
    [Documentation]    Assert the project exists
    ${exists}=    project_keywords.project exists    ${project_name}
    Should Be True    ${exists}    msg=Project ${project_name} does not exist

Project should not exist
    [Arguments]    ${project_name}
    [Documentation]    Assert the project does not exist
    ${exists}=    project_keywords.project exists    ${project_name}
    Should Not Be True    ${exists}    msg=Project ${project_name} still exists

Project should be active
    [Arguments]    ${project_name}    ${timeout}=${WAIT_TIMEOUT}
    [Documentation]    Wait until the project is fully provisioned (active)
    project_keywords.wait for project active    ${project_name}    ${timeout}

Get Project Resource Limits
    [Arguments]    ${project_name}
    [Documentation]    Get the CPU and memory limits configured on a project.
    ...    Returns: (cpu_limit, memory_limit) as strings
    ${cpu}    ${memory}=    project_keywords.get project resource limits    ${project_name}
    RETURN    ${cpu}    ${memory}

Get Project Id
    [Arguments]    ${project_name}
    [Documentation]    Get the generated project id (p-xxxxx) for a display name
    ${project_id}=    project_keywords.get project id    ${project_name}
    RETURN    ${project_id}

List Projects
    ${projects}=    project_keywords.list projects
    RETURN    ${projects}

Wait for project deleted
    [Arguments]    ${project_name}    ${timeout}=${WAIT_TIMEOUT}
    [Documentation]    Wait for the project to be fully removed
    project_keywords.wait for project deleted    ${project_name}    ${timeout}

Project is deleted
    [Arguments]    ${project_name}    ${timeout}=${WAIT_TIMEOUT}
    [Documentation]    Delete the project and wait until it is gone
    project_keywords.delete project    ${project_name}
    project_keywords.wait for project deleted    ${project_name}    ${timeout}

# negative
Try To Create Project
    [Arguments]    ${project_name}    ${cpu_limit}=${NONE}    ${memory_limit}=${NONE}
    ...    ${ns_default_cpu}=${NONE}    ${ns_default_memory}=${NONE}
    [Documentation]    Attempt to create a project expected to be rejected.
    ...    Returns a result dict {success, code, message}; never fails on rejection.
    ${result}=    project_keywords.try create project    ${project_name}
    ...    cpu_limit=${cpu_limit}    memory_limit=${memory_limit}
    ...    ns_default_cpu=${ns_default_cpu}    ns_default_memory=${ns_default_memory}
    RETURN    ${result}

Try To Delete Project
    [Arguments]    ${project_name}
    [Documentation]    Attempt to delete a project expected to be missing. Returns result dict.
    ${result}=    project_keywords.try delete project    ${project_name}
    RETURN    ${result}
