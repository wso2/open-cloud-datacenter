# syntax=docker/dockerfile:1
FROM alpine:3.22

ARG TERRAFORM_VERSION=1.15.8
ARG TARGETARCH

RUN apk add --no-cache ca-certificates git jq unzip wget \
    && wget -q "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_${TARGETARCH}.zip" \
    && wget -q "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_SHA256SUMS" \
    && grep "terraform_${TERRAFORM_VERSION}_linux_${TARGETARCH}.zip" "terraform_${TERRAFORM_VERSION}_SHA256SUMS" | sha256sum -c - \
    && unzip "terraform_${TERRAFORM_VERSION}_linux_${TARGETARCH}.zip" -d /usr/local/bin \
    && rm "terraform_${TERRAFORM_VERSION}_linux_${TARGETARCH}.zip" "terraform_${TERRAFORM_VERSION}_SHA256SUMS" \
    && addgroup -S -g 65532 runner \
    && adduser -S -D -H -u 65532 -G runner runner \
    && mkdir -p /home/runner \
    && chown 65532:65532 /home/runner

COPY --chown=65532:65532 capabilities/tenant-space/fixtures/terraform/ \
    /opt/testsuite/capabilities/tenant-space/terraform/
COPY --chown=65532:65532 capabilities/tenant-space/scripts/ \
    /opt/testsuite/capabilities/tenant-space/scripts/

ENV HOME=/home/runner \
    TF_IN_AUTOMATION=1 \
    TF_INPUT=0

USER 65532:65532
WORKDIR /home/runner

ENTRYPOINT []
CMD ["/bin/sh"]
