#!/bin/bash
set -e

# Harvester Robot Framework Test Runner
# Usage: ./run.sh [options]

# Color codes
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# Load .env if exists
if [ -f .env ]; then
    echo -e "${GREEN}Loading .env file...${NC}"
    # Export variables from .env, skip comments and empty lines
    set -a
    source .env
    set +a
    echo -e "${GREEN}Environment variables loaded from .env${NC}"
else
    echo -e "${YELLOW}Warning: .env file not found${NC}"
    echo -e "${YELLOW}Copy .env.example to .env and configure: cp .env.example .env${NC}"
fi

# Default values
TEST_CASE=""
TEST_SUITE=""
TEST_FILE=""
PROCESSES=""
ORDERING=""
STRATEGY=""
INCLUDE_TAG=""
EXCLUDE_TAG=""
VARIABLES=""
LOG_LEVEL=${ROBOT_LOG_LEVEL:-INFO}
OUTPUT_DIR=${ROBOT_OUTPUT_DIR:-/tmp/harvester-test-report}

show_help() {
    cat << EOF
Harvester Robot Framework Test Runner

Usage: $0 [options]

Options:
    -t "test name"      Run specific test case by name
    -s "suite name"     Run specific test suite by name
    -f "file path"      Run specific test file (e.g., tests/regression/vm/test_vm.robot)
    -i "tag"           Include tests with tag
    -e "tag"           Exclude tests with tag
    -v VAR:value       Set Robot Framework variable
    -L log_level       Set log level (TRACE|DEBUG|INFO|WARN|ERROR)
    -d output_dir      Set output directory
    -p N               Run suites in parallel with pabot (N processes)
    -o orderfile       Pabot ordering file (implies --testlevelsplit); use with -p
                       to stage a shared setup/teardown suite around parallel ones
    -S strategy        Operation strategy: crd (default) or rest (sets HARVESTER_OPERATION_STRATEGY)
    -W                 Skip virtual environment check
    -h                 Show this help

Examples:
    $0                                    # Run all tests
    $0 -t "Test VM Basic Lifecycle"       # Run specific test case
    $0 -s "test_vm"                       # Run a single test suite by name
    $0 -s vm                              # Run a whole category (vm/volume/image/addon/rancher)
    $0 -f tests/regression/vm/test_vm.robot  # Run specific test file
    $0 -i coretest                        # Run with tag
    $0 -i p0 -e backup                    # Include/exclude tags
    $0 -v WAIT_TIMEOUT:1200               # Set variable
    $0 -L DEBUG                           # Debug logging
    $0 -p 3 -i volume                     # Run volume suites in parallel (3 processes)
    $0 -p 3 -o tests/regression/rancher/rancher-ordering.txt -f tests/regression/rancher/  # Shared setup, parallel rancher suites
    $0 -S rest -i volume                  # Run volume suites against the REST API
    $0 -i pr-baseline -p 8                # Run the PR baseline (image+VM+volume) in parallel

Available Tags:
    Priority: p0, p1, p2
    Type: coretest, regression, negative, smoke, sanity
    Component: virtualmachines, images, volumes, networks, backup, ha
    Suite set: pr-baseline (all image + basic VM + volume suites for per-PR checks)
EOF
}

# Parse arguments
while getopts "t:s:f:i:e:v:L:d:p:o:S:Wh" opt; do
    case $opt in
        t) TEST_CASE="--test \"$OPTARG\"" ;;
        s) TEST_SUITE="--suite \"$OPTARG\"" ;;
        f) TEST_FILE="$OPTARG" ;;
        i) INCLUDE_TAG="--include $OPTARG" ;;
        e) EXCLUDE_TAG="--exclude $OPTARG" ;;
        v) VARIABLES="$VARIABLES --variable $OPTARG" ;;
        L) LOG_LEVEL=$OPTARG ;;
        d) OUTPUT_DIR=$OPTARG ;;
        p) PROCESSES=$OPTARG ;;
        o) ORDERING=$OPTARG ;;
        S) STRATEGY=$OPTARG ;;
        W) SKIP_VENV_CHECK=true ;;
        h) show_help; exit 0 ;;
        \?) echo "Invalid option: -$OPTARG" >&2; show_help; exit 1 ;;
    esac
done

# Create output directory
mkdir -p "$OUTPUT_DIR"

# Check Robot Framework
if ! command -v robot &> /dev/null; then
    echo -e "${RED}Error: Robot Framework not installed${NC}"
    echo "Install with: pip install -r requirements.txt"
    exit 1
fi

# Check pabot when parallel execution is requested
if [ -n "$PROCESSES" ] && ! command -v pabot &> /dev/null; then
    echo -e "${RED}Error: pabot not installed (required for -p)${NC}"
    echo "Install with: pip install -r requirements.txt"
    exit 1
fi

# Check virtual environment (skip if -W is set)
if [[ "$SKIP_VENV_CHECK" != "true" ]] && [[ -z "$VIRTUAL_ENV" ]]; then
    echo -e "${YELLOW}Warning: Virtual environment not activated${NC}"
    read -p "Continue? (y/N) " -n 1 -r
    echo
    [[ ! $REPLY =~ ^[Yy]$ ]] && exit 1
fi

# Check required variables
[[ -z "$HARVESTER_ENDPOINT" ]] && echo -e "${YELLOW}Warning: HARVESTER_ENDPOINT not set (using default)${NC}"

# Pass existing infrastructure variables from .env to Robot Framework
[ -n "$EXISTING_HARVESTER_NAME" ] && VARIABLES="$VARIABLES -v EXISTING_HARVESTER_NAME:$EXISTING_HARVESTER_NAME"
[ -n "$EXISTING_IMAGE" ] && VARIABLES="$VARIABLES -v EXISTING_IMAGE:$EXISTING_IMAGE"
[ -n "$EXISTING_NETWORK" ] && VARIABLES="$VARIABLES -v EXISTING_NETWORK:$EXISTING_NETWORK"


# Set Python path (../apiclient provides the shared harvester_api package)
export PYTHONPATH="${PYTHONPATH}:$(pwd)/libs:$(pwd)/apiclient:$(pwd)/../apiclient"

# Operation strategy (crd|rest). Read at library import time, so export before robot starts.
[ -n "$STRATEGY" ] && export HARVESTER_OPERATION_STRATEGY="$STRATEGY"

# Build command
# Use pabot for suite-level parallel execution when -p is given, otherwise plain robot.
# pabot-specific options (e.g. --processes) must precede the shared robot options.
if [ -n "$PROCESSES" ]; then
    ROBOT_CMD="pabot --processes $PROCESSES"
    [ -n "$ORDERING" ] && ROBOT_CMD="$ROBOT_CMD --testlevelsplit --ordering $ORDERING"
else
    ROBOT_CMD="robot"
fi
ROBOT_CMD="$ROBOT_CMD --outputdir $OUTPUT_DIR"
ROBOT_CMD="$ROBOT_CMD --loglevel $LOG_LEVEL"
ROBOT_CMD="$ROBOT_CMD --timestampoutputs"
ROBOT_CMD="$ROBOT_CMD --consolecolors on"

[ -n "$TEST_CASE" ] && ROBOT_CMD="$ROBOT_CMD $TEST_CASE"
[ -n "$TEST_SUITE" ] && ROBOT_CMD="$ROBOT_CMD $TEST_SUITE"
[ -n "$INCLUDE_TAG" ] && ROBOT_CMD="$ROBOT_CMD $INCLUDE_TAG"
[ -n "$EXCLUDE_TAG" ] && ROBOT_CMD="$ROBOT_CMD $EXCLUDE_TAG"
[ -n "$VARIABLES" ] && ROBOT_CMD="$ROBOT_CMD $VARIABLES"

# Add test path - if TEST_FILE is specified, use it; otherwise use tests/ directory
if [ -n "$TEST_FILE" ]; then
    ROBOT_CMD="$ROBOT_CMD $TEST_FILE"
else
    ROBOT_CMD="$ROBOT_CMD tests/"
fi

# Print info
echo -e "${GREEN}======================================${NC}"
echo -e "${GREEN}Harvester Robot Framework Test Runner${NC}"
echo -e "${GREEN}======================================${NC}"
echo "Output: $OUTPUT_DIR"
echo "Log level: $LOG_LEVEL"
echo -e "${GREEN}======================================${NC}"
echo

# Run tests
echo -e "${GREEN}Running tests...${NC}"
eval $ROBOT_CMD
EXIT_CODE=$?

# Print results
echo
echo -e "${GREEN}======================================${NC}"
echo -e "${GREEN}Test Execution Completed${NC}"
echo -e "${GREEN}======================================${NC}"
echo "Exit code: $EXIT_CODE"
echo "Reports:"
echo "  - HTML Report: $OUTPUT_DIR/report.html"
echo "  - HTML Log: $OUTPUT_DIR/log.html"
echo "  - XML Output: $OUTPUT_DIR/output.xml"
echo

if [ $EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}✓ All tests passed${NC}"
else
    echo -e "${RED}✗ Some tests failed${NC}"
fi

exit $EXIT_CODE
