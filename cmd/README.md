# Commands

This directory is reserved for executable Go entry points.

The first command will provide the test runner invoked by Argo Workflows. Command
packages should contain argument parsing and process wiring only. Reusable API
clients, assertions, evidence collection, and capability execution logic belong
under `internal/`.

No command is implemented in the project-layout milestone.

