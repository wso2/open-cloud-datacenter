# Internal Packages

This directory will contain Go packages used by the test-suite commands. The
packages are internal implementation details rather than a public Go API.

Expected responsibilities include:

- Capability discovery, metadata validation, and generated catalog support.
- Rancher, Kubernetes, and Harvester clients.
- Bounded asynchronous assertions.
- Environment locking and run identity.
- Terraform lifecycle coordination.
- Evidence collection and redaction.
- Result and JUnit publication.
- Cleanup and janitor behavior.

Package boundaries will be introduced with executable code. This milestone does
not create empty package directories or commit speculative Go APIs.
