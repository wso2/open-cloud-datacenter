# Linter Configuration and Suppressions

This directory contains configuration files for the linters used by [super-linter](https://github.com/super-linter/super-linter).

## Table of Contents

- [Checkov (Terraform Security)](#checkov-terraform-security)
- [TFLint (Terraform Linting)](#tflint-terraform-linting)
- [Terrascan (Terraform Security)](#terrascan-terraform-security)
- [Terraform Format](#terraform-format)
- [YAML (yamllint)](#yaml-yamllint)
- [ShellCheck (Bash/Shell)](#shellcheck-bashshell)
- [actionlint (GitHub Actions)](#actionlint-github-actions)
- [Markdown (markdownlint)](#markdown-markdownlint)
- [Global Exclusions](#global-exclusions)

---

## Checkov (Terraform Security)

**Config file:** `.github/linters/.checkov.yaml`

### Global suppressions (config file)

```yaml
# .github/linters/.checkov.yaml
framework:
  - terraform

skip-check:
  - CKV_TF_1 # Ensure Terraform module sources use a commit hash
  - CKV_AWS_18 # Ensure S3 bucket has access logging enabled

skip-path:
  - examples/
  - tests/
```

### Inline suppressions (per resource)

```hcl
resource "aws_s3_bucket" "example" {
  bucket = "my-bucket"
  # checkov:skip=CKV_AWS_18:Access logging not needed for dev bucket
}
```

### Suppress multiple checks inline

```hcl
resource "aws_instance" "example" {
  # checkov:skip=CKV_AWS_79:IMDSv2 not required for this use case
  # checkov:skip=CKV_AWS_88:Public IP needed for bastion
  ami           = "ami-12345"
  instance_type = "t3.micro"
}
```

### Documentation

- [Checkov CLI Reference](https://www.checkov.io/2.Basics/CLI%20Command%20Reference.html)
- [Checkov Suppressions](https://www.checkov.io/2.Basics/Suppressing%20and%20Skipping%20Policies.html)

---

## TFLint (Terraform Linting)

**Config file:** `.github/linters/.tflint.hcl` or `.tflint.hcl` in repository root

### Global suppressions (config file)

```hcl
# .github/linters/.tflint.hcl
config {
  module = true
}

rule "terraform_naming_convention" {
  enabled = false
}

rule "terraform_unused_declarations" {
  enabled = false
}

# Disable specific AWS rules
rule "aws_instance_invalid_type" {
  enabled = false
}
```

### Inline suppressions

```hcl
# tflint-ignore: terraform_naming_convention
resource "aws_s3_bucket" "MyBucket" {
  bucket = "my-bucket"
}

# tflint-ignore-file: terraform_required_providers
# (Place at top of file to ignore for entire file)
```

### Suppress multiple rules inline

```hcl
# tflint-ignore: rule1, rule2, rule3
resource "aws_instance" "example" {
  ami = "ami-12345"
}
```

### Documentation

- [TFLint Configuration](https://github.com/terraform-linters/tflint/blob/master/docs/user-guide/config.md)
- [TFLint Annotations](https://github.com/terraform-linters/tflint/blob/master/docs/user-guide/annotations.md)

---

## Terrascan (Terraform Security)

**Config file:** `.github/linters/.terrascan.toml` or `terrascan-config.toml`

### Global suppressions (config file)

```toml
# .github/linters/.terrascan.toml
[severity]
level = "high"

[rules]
skip-rules = [
  "AC_AWS_0214",  # S3 bucket encryption
  "AC_AWS_0497",  # Another rule to skip
]

[scan-rules]
# Scan only specific rules (whitelist approach)
# scan-rules = ["AC_AWS_0001"]
```

### Inline suppressions

```hcl
resource "aws_s3_bucket" "example" {
  bucket = "my-bucket"
  #ts:skip=AC_AWS_0214 Encryption handled by bucket policy
}
```

### Documentation

- [Terrascan Config](https://runterrascan.io/docs/usage/config_options/)
- [Terrascan Skip Rules](https://runterrascan.io/docs/usage/in_file_instrumentation/)

---

## Terraform Format

**No config file** - `terraform fmt` has no suppression mechanism.

### Handling format issues

1. Run locally before committing:

   ```bash
   terraform fmt -recursive
   ```

2. Disable in super-linter (not recommended):
   ```yaml
   # In workflow
   env:
     VALIDATE_TERRAFORM_FMT: false
   ```

---

## YAML (yamllint)

**Config file:** `.github/linters/.yamllint.yml` or `.yamllint.yaml`

### Global suppressions (config file)

```yaml
# .github/linters/.yamllint.yml
extends: default

rules:
  line-length:
    max: 120
    level: warning

  comments:
    min-spaces-from-content: 1

  truthy:
    check-keys: false

  # Disable specific rules
  document-start: disable
  indentation:
    spaces: 2
    indent-sequences: consistent
```

### Inline suppressions

```yaml
# yamllint disable-line rule:line-length
very_long_line: "This is a very long line that exceeds the maximum length but we need it this way"

# yamllint disable rule:truthy
some_key: yes
another_key: no
# yamllint enable rule:truthy
```

### File-level suppression

```yaml
# yamllint disable
# (Disables all rules for the rest of the file)

# Or at the top of file:
# yamllint disable-file
```

### Documentation

- [yamllint Configuration](https://yamllint.readthedocs.io/en/stable/configuration.html)
- [yamllint Rules](https://yamllint.readthedocs.io/en/stable/rules.html)

---

## ShellCheck (Bash/Shell)

**Config file:** `.github/linters/.shellcheckrc` or `.shellcheckrc` in repository root

### Global suppressions (config file)

```bash
# .github/linters/.shellcheckrc
# Disable specific checks globally
disable=SC2034,SC2086,SC1091

# Set default shell dialect
shell=bash

# Enable optional checks
enable=require-variable-braces
```

### Inline suppressions

```bash
#!/bin/bash

# Suppress single check on next line
# shellcheck disable=SC2086
echo $unquoted_var

# Suppress multiple checks
# shellcheck disable=SC2086,SC2034
```

### Block suppression

```bash
#!/bin/bash

# shellcheck disable=SC2034
unused_var="value"
another_unused="value"
# shellcheck enable=SC2034
```

### File-level suppression

```bash
#!/bin/bash
# shellcheck disable=SC2034,SC2086
# (Place at top of file, after shebang)
```

### Documentation

- [ShellCheck Wiki](https://www.shellcheck.net/wiki/)
- [ShellCheck Directives](https://www.shellcheck.net/wiki/Directive)

---

## actionlint (GitHub Actions)

**Config file:** `.github/linters/.actionlint.yaml` or `.actionlint.yaml`

### Global suppressions (config file)

```yaml
# .github/linters/.actionlint.yaml
self-hosted-runner:
  labels:
    - self-hosted
    - linux
    - custom-runner

# Ignore specific rules
# (actionlint doesn't support rule-level suppression in config)
```

### Inline suppressions

```yaml
# In workflow file
steps:
  - name: Run something
    # actionlint-ignore: SC2086
    run: echo $UNQUOTED

  - name: Another step
    run: |
      # shellcheck disable=SC2086
      echo $var
```

### Documentation

- [actionlint Configuration](https://github.com/rhysd/actionlint/blob/main/docs/config.md)

---

## Markdown (markdownlint)

**Config file:** `.github/linters/.markdownlint.json` or `.markdownlint.yaml`

### Global suppressions (config file)

```json
{
  "default": true,
  "MD013": false,
  "MD033": false,
  "MD041": false,
  "line-length": {
    "line_length": 120,
    "code_blocks": false,
    "tables": false
  }
}
```

Or YAML format:

```yaml
# .github/linters/.markdownlint.yaml
default: true
MD013: false # Line length
MD033: false # Inline HTML
MD041: false # First line heading
```

### Inline suppressions

```markdown
<!-- markdownlint-disable MD013 -->

This is a very long line that would normally trigger the line length warning but we want to allow it here.

<!-- markdownlint-enable MD013 -->

<!-- markdownlint-disable-next-line MD041 -->

This paragraph doesn't start with a heading.
```

### File-level suppression

```markdown
<!-- markdownlint-disable -->

(Entire file content ignored)

<!-- Or at top of file: -->
<!-- markdownlint-disable-file MD013 MD033 -->
```

### Documentation

- [markdownlint Rules](https://github.com/DavidAnson/markdownlint/blob/main/doc/Rules.md)
- [markdownlint Configuration](https://github.com/DavidAnson/markdownlint#configuration)

---

## Global Exclusions

### Exclude files/paths from all linters

In your workflow file:

```yaml
- name: Lint Code Base
  uses: super-linter/super-linter@v7
  env:
    VALIDATE_ALL_CODEBASE: false
    DEFAULT_BRANCH: main
    GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}

    # Exclude paths (regex)
    FILTER_REGEX_EXCLUDE: .*examples/.*|.*vendor/.*|.*node_modules/.*

    # Or include only specific paths
    FILTER_REGEX_INCLUDE: .*modules/.*|.*\.github/.*
```

### Disable specific linters entirely

```yaml
env:
  VALIDATE_CHECKOV: false
  VALIDATE_TERRAFORM_TFLINT: false
  VALIDATE_TERRAFORM_TERRASCAN: false
  VALIDATE_YAML: false
```

### Use .gitignore patterns

Super-linter respects `.gitignore` by default. Files in `.gitignore` are not linted.

---

## Quick Reference

| Linter       | Config File          | Inline Suppression                    |
| ------------ | -------------------- | ------------------------------------- |
| Checkov      | `.checkov.yaml`      | `# checkov:skip=CKV_XXX:reason`       |
| TFLint       | `.tflint.hcl`        | `# tflint-ignore: rule_name`          |
| Terrascan    | `.terrascan.toml`    | `#ts:skip=AC_XXX reason`              |
| yamllint     | `.yamllint.yml`      | `# yamllint disable-line rule:name`   |
| ShellCheck   | `.shellcheckrc`      | `# shellcheck disable=SCXXXX`         |
| markdownlint | `.markdownlint.json` | `<!-- markdownlint-disable MDXXX -->` |
| actionlint   | `.actionlint.yaml`   | Limited support                       |

All config files go in `.github/linters/` for super-linter to auto-detect them.
