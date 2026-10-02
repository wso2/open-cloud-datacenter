*** Settings ***
Documentation    Namespace creation and resource-limit tests
Test Tags        namespaces    regression

Resource    ../../../keywords/variables.resource
Resource    ../../../keywords/common.resource
Resource    ../../../keywords/project.resource
Resource    ../../../keywords/namespace.resource

Suite Setup       Local Suite Setup
Suite Teardown    Local Suite Teardown
Test Teardown     Local Test Teardown


*** Variables ***
${PROJECT_NAME}      ${EMPTY}
${NAMESPACE_NAME}    ${EMPTY}
${CPU_LIMIT}          4
${MEMORY_LIMIT}       8Gi


*** Test Cases ***
Create Namespace In Project With Resource Limits
    [Tags]    p0
    [Documentation]    Creates a project prerequisite, then creates and validates a namespace.
    Project is created with resource limits    ${PROJECT_NAME}
    ...    cpu_limit=${CPU_LIMIT}    memory_limit=${MEMORY_LIMIT}
    Project should be active    ${PROJECT_NAME}

    Namespace is created in project    ${NAMESPACE_NAME}    ${PROJECT_NAME}
    ...    cpu_limit=${CPU_LIMIT}    memory_limit=${MEMORY_LIMIT}
    Namespace should exist    ${NAMESPACE_NAME}
    Namespace should be active    ${NAMESPACE_NAME}
    Namespace should belong to project    ${NAMESPACE_NAME}    ${PROJECT_NAME}
    ${actual_cpu}    ${actual_memory}=    Get Namespace Resource Limits    ${NAMESPACE_NAME}
    Should Be Equal As Strings    ${actual_cpu}    ${CPU_LIMIT}
    Should Be Equal As Strings    ${actual_memory}    ${MEMORY_LIMIT}


*** Keywords ***
Local Suite Setup
    ${suffix}=    Generate Unique Name
    Set Suite Variable    ${PROJECT_NAME}    project-${suffix}
    Set Suite Variable    ${NAMESPACE_NAME}    ns-${suffix}
    Set up test environment

Local Suite Teardown
    Run Keyword And Ignore Error    Namespace is deleted    ${NAMESPACE_NAME}
    Run Keyword And Ignore Error    Project is deleted    ${PROJECT_NAME}

Local Test Teardown
    Run Keyword If Test Failed    Log    Test failed: ${TEST_MESSAGE}    WARN