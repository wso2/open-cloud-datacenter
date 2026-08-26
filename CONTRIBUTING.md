# Contributing to the Harvester Upgrade Test Suite

Thank you for helping build a reusable acceptance-test suite for Harvester and
Rancher upgrades. Contributions can include capability tests, pipeline and
infrastructure improvements, documentation, bug reports, and design feedback.

This project is maintained on the `testsuite` branch of the
[`wso2/open-cloud-datacenter`](https://github.com/wso2/open-cloud-datacenter)
repository. Changes for this project must target the `testsuite` branch, not
`main`.

## Before contributing

- Read the project overview and architecture in [README.md](README.md).
- Review the proposal in
  [GitHub Discussion #242](https://github.com/wso2/open-cloud-datacenter/discussions/242).
- Search existing issues and discussions before proposing duplicate work.
- Discuss substantial architecture, security, dependency, or capability-contract
  changes before implementation.
- Follow the project [Code of Conduct](CODE_OF_CONDUCT.md).

## Development workflow

1. Fork the repository.
2. Fetch the `testsuite` branch and create a focused working branch from it.
3. Make the smallest coherent change that addresses the issue or proposal.
4. Add or update tests and documentation where applicable.
5. Run the relevant local validation commands introduced by the project.
6. Commit the change with a clear, descriptive message.
7. Push the working branch to your fork.
8. Open a pull request targeting `wso2/open-cloud-datacenter:testsuite`.

For example:

```bash
git clone --branch testsuite --single-branch \
  https://github.com/<your-username>/open-cloud-datacenter.git \
  harvester-upgrade-test-suite
cd harvester-upgrade-test-suite

git switch -c feat/CAP-XXX-short-description
```

Do not develop directly on your fork's `testsuite` branch. Keeping it aligned
with upstream makes later updates and pull requests easier to manage.

## Types of contributions

### Capability modules

Each capability must be independently addable under:

```text
capabilities/descriptive-name/
├── capability.yaml
├── workflow/
│   └── workflow-template.yaml
├── fixtures/
├── tests/
└── evidence.yaml
```

The directory uses a readable capability name such as `tenant-space`, while the
metadata retains its stable ID such as `CAP-002`. A capability contribution
should:

- Use a stable capability ID and a clear, user-facing name.
- Declare its inputs, labels, timeout, lock scope, workflow reference, and
  expected outputs.
- Provide an independently runnable capability `WorkflowTemplate`.
- Create uniquely named fixtures associated with a run ID.
- Use bounded waits and explicit deadlines for asynchronous behavior.
- Test observable behavior rather than only successful provisioning.
- Collect useful diagnostics without exposing credentials or sensitive state.
- Clean up every resource it creates, including after assertion failures.
- Produce results using the shared JUnit, JSON, evidence, and log contract.

Adding a capability should not require changes to another capability workflow.
If shared behavior must change, update the versioned shared contract and explain
its compatibility impact in the pull request.

### Go code

- Keep Kubernetes and Harvester API operations context-aware and bounded.
- Prefer typed clients where stable APIs are available and the dynamic client
  where Harvester CRDs require it.
- Use Ginkgo v2 and Gomega for capability acceptance tests.
- Use Go's standard `testing` package, optionally with Gomega, for focused unit
  tests.
- Return actionable errors and collect diagnostics at the point of failure.
- Format Go code with `gofmt`.

### Terraform fixtures and infrastructure

- Use Terraform for fixture lifecycle, not behavioral assertions.
- Pin provider and external module versions intentionally.
- Keep environment-specific values in documented inputs or overlays.
- Ensure destroy operations are safe to repeat after partial failures.
- Do not commit `.terraform/`, state files, saved plans, variable files, or
  generated credentials.
- Keep development defaults small while making production differences explicit.

### Argo Workflows and Kubernetes manifests

- Keep each capability's independently runnable `WorkflowTemplate` inside its
  capability module.
- Reuse pipeline-wide templates for common lifecycle behavior instead of copying
  credential, result-publication, or cleanup steps.
- Define resource requests, limits, deadlines, retries, and exit handling.
- Apply least-privilege RBAC to workflow service accounts.
- Keep credentials in Kubernetes Secrets or the configured Host secret store.
- Do not include live-cluster metadata such as UIDs, resource versions, managed
  fields, or creation timestamps in committed manifests.

### Documentation

- Keep instructions reproducible for contributors outside the original
  development environment.
- Clearly distinguish development examples from production recommendations.
- Use placeholders for hostnames, tenant IDs, credentials, VLANs, storage
  classes, and other environment-specific values.
- Update related documentation whenever behavior or configuration changes.

## Security and sensitive data

Never commit:

- Rancher, Harvester, Kubernetes, MinIO, registry, or identity-provider
  credentials.
- Kubeconfig files or tokens.
- Terraform state, plans, or populated `.tfvars` files.
- Secret values embedded in workflow parameters or exported manifests.
- Test evidence containing credentials, private keys, or sensitive workload
  data.

Use redacted examples and secret references. If sensitive information is
committed accidentally, do not only remove it in a later commit. Stop sharing
the branch and notify the maintainers so the credential can be revoked and the
history handled appropriately.

## Commits

Keep commits focused and use concise, imperative messages. Conventional-style
prefixes are encouraged:

```text
feat(capability): add CAP-XXX acceptance test
fix(pipeline): always publish cleanup evidence
docs(setup): document development MinIO configuration
chore(infra): pin Argo Workflows chart version
```

Avoid mixing generated files, formatting-only changes, and behavioral changes
in the same commit unless they are inseparable.

## Pull requests

A pull request should include:

- The problem being solved and the intended behavior.
- The capability IDs or infrastructure components affected.
- How the change was tested and the environment used.
- Relevant logs or redacted evidence for behavioral changes.
- Cleanup, compatibility, security, and operational considerations.
- Documentation updates required by the change.

Keep pull requests reviewable. Large features should be split into coherent
vertical slices when possible.

## Reporting problems and proposing changes

Use GitHub issues for actionable defects and scoped implementation work. Use
GitHub Discussions for architecture proposals, capability-contract changes, or
questions that need broader design agreement.

When reporting a test-suite failure, include the suite version, Host and Target
versions, capability ID, redacted result output, and whether automatic cleanup
succeeded. Never attach unredacted Terraform state, kubeconfigs, or credentials.

## License

By contributing, you agree that your contributions will be licensed under the
terms in [LICENSE](LICENSE).
