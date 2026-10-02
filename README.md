# Harvester Cluster Test Suite

This branch contains an automated test suite for validating the day-to-day operations of a Harvester cluster. The tests are written with [Robot Framework](https://robotframework.org/) and are intended to exercise common workflows through a repeatable, readable test interface.

The suite covers:

- Creating projects and namespaces.
- Virtual machine lifecycle operations and storage volume mounts.
- Creating Kubernetes clusters with single-node and multi-node configurations.
- Read-write-many (RWX) volume behavior in Kubernetes clusters.

This project provides an abstraction over the original Harvester test suite: it organizes cluster workflows as reusable Robot Framework tests and keywords, with options for filtering and running suites. It is focused on validating the operational workflows listed above against a Harvester environment.

## Quick Start

### Prerequisites

Ensure you have:

- Python 3.8 or later.
- `kubectl` installed and configured.
- Access to a Harvester cluster.
- A kubeconfig file for the Harvester cluster.

### Install Dependencies

From the repository root, create and activate a virtual environment, then install the test dependencies:

```bash
cd tests/harvester_robot_tests
python3 -m venv venv
source venv/bin/activate  # Windows: venv\Scripts\activate
pip install -r requirements.txt
```

### Configure the Environment

Copy the example environment file and edit it with your cluster credentials and paths:

```bash
cp .env.example .env
```

At minimum, configure `HARVESTER_ENDPOINT`, `HARVESTER_USERNAME`, and `HARVESTER_PASSWORD`. Set `KUBECONFIG` if you are not using the default `~/.kube/config`.

Example `.env` values:

```dotenv
KUBECONFIG=/home/user/.kube/config
export HARVESTER_OPERATION_STRATEGY=crd

HARVESTER_ENDPOINT=https://harvester.example.com
export RANCHER_ENDPOINT="https://rancher.example.com"
export RANCHER_API_KEY="rancher-api-key"

//Need for vm testing
export OPENSUSE_IMAGE_URL="locally-host-opensuse-iso-file-url"
export UBUNTU_IMAGE_URL="locally-host-ubuntu 24-iso-file-url"

//Need for cluster testing
export EXISTING_HARVESTER_NAME="harvester-cluster-id"
export EXISTING_IMAGE="harvester-iso-image"
export EXISTING_NETWORK="test-cluster-provisioning-network"


ROBOT_OUTPUT_DIR=./results

export WAIT_TIMEOUT=600

```

### Verify Kubernetes Access

Confirm that `kubectl` can reach the cluster and list its nodes:

```bash
kubectl get nodes
```

If this fails, verify the kubeconfig path and that the cluster is reachable.

### Run Tests

Make the runner executable, then run all tests or select a test file or suite category:

```bash
chmod +x run.sh

# Run all tests
./run.sh

# Run a specific test file
./run.sh -f tests/regression/vm/test_vm.robot

# Run a category (for example: vm, volume, image, addon, rancher, or host)
./run.sh -s vm
```


## Test Results

By default, results are written to `./results/`:

```bash
# Linux
xdg-open results/report.html
xdg-open results/log.html

# macOS
open results/report.html
open results/log.html

# Windows
start results/report.html
start results/log.html
```

Use the Robot Framework report for a summary and the log for detailed keyword-level execution information.

Tests are also grouped by component subdirectory. For example, `./run.sh -s vm` or `./run.sh -s rancher` runs a category by suite name.

## Troubleshooting

**`kubectl: command not found`**

Install `kubectl` using the [Kubernetes installation guide](https://kubernetes.io/docs/tasks/tools/).

**Unable to connect to the server**

- Verify the kubeconfig path and current context.
- Confirm the Harvester cluster is reachable from your machine.
- Check that the cluster certificate is valid and trusted.

**Robot Framework or the Kubernetes Python package is missing**

Activate the virtual environment and install the project dependencies:

```bash
source venv/bin/activate
pip install -r requirements.txt
```

On Windows, activate with `venv\Scripts\activate`.

**`.env` file not found**

Create it from the example and set the connection details:

```bash
cp .env.example .env
```

## Further Information

- Browse the [`tests/`](https://github.com/harvester/tests) repo for the available suites.
- Consult the [Harvester documentation](https://docs.harvesterhci.io/) for cluster setup and administration.

## License

See [LICENSE](LICENSE) for the project license and [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) for community guidelines.