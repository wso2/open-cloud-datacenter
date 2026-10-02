*** Settings ***
Documentation    Project creation and resource-limit tests
Test Tags        projects    regression

Resource    ../../../keywords/variables.resource
Resource    ../../../keywords/common.resource
Resource    ../../../keywords/project.resource

Suite Setup       Local Suite Setup
Suite Teardown    Local Suite Teardown
Test Teardown     Local Test Teardown


*** Variables ***
${PROJECT_NAME}      ${EMPTY}
${CPU_LIMIT}          4
${MEMORY_LIMIT}       8Gi


*** Test Cases ***
Create Project With Resource Limits
    [Tags]    p0
    [Documentation]    Creates a project and validates its resource limits.
    Project is created with resource limits    ${PROJECT_NAME}
    ...    cpu_limit=${CPU_LIMIT}    memory_limit=${MEMORY_LIMIT}
    Project should exist    ${PROJECT_NAME}
    Project should be active    ${PROJECT_NAME}
    ${actual_cpu}    ${actual_memory}=    Get Project Resource Limits    ${PROJECT_NAME}
    Should Be Equal As Strings    ${actual_cpu}    ${CPU_LIMIT}
    Should Be Equal As Strings    ${actual_memory}    ${MEMORY_LIMIT}


*** Keywords ***
Local Suite Setup
    ${suffix}=    Generate Unique Name
    Set Suite Variable    ${PROJECT_NAME}    project-${suffix}
    Set up test environment

Local Suite Teardown
    Run Keyword And Ignore Error    Project is deleted    ${PROJECT_NAME}

Local Test Teardown
    Run Keyword If Test Failed    Log    Test failed: ${TEST_MESSAGE}    WARN