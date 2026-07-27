# DC-API Overview

The uploaded file is an **OpenAPI Specification (Swagger)** for the **DC-API**.

Instead of the raw YAML, here's what it describes in plain English.

---

# What is DC-API?

**DC-API** is a cloud management API developed by WSO2.

It sits between users and the underlying infrastructure (Rancher + Harvester), providing an AWS/Azure-like REST API for managing cloud resources.

Users can manage:

- Projects
- Virtual Machines (VMs)
- Kubernetes (RKE2) clusters
- Virtual Networks
- Bastion Hosts
- Databases
- Key Vaults
- DNS
- Role-Based Access Control (RBAC)
- Service Accounts
- Images
- Quotas
- Regions

---

# Authentication

Every endpoint under `/v1/*` requires authentication.

Two authentication methods are supported.

## 1. User Login (OIDC JWT)

Users log in using Asgardeo.

```http
Authorization: Bearer <JWT Token>
```

The JWT must contain the appropriate tenant group.

---

## 2. Service Account Token

Automation tools can authenticate using a long-lived token.

```http
Authorization: Bearer dcapi_sa_xxxxxxxxx
```

---

# Long Running Operations

Some operations take several minutes.

Examples include:

- Create VM
- Create Cluster
- Create Virtual Network

Instead of waiting, the API immediately returns:

```http
202 Accepted
```

Example response:

```text
Status: PENDING
```

The client should periodically call the GET endpoint until:

```text
PENDING
   ↓
ACTIVE
```

or

```text
PENDING
   ↓
FAILED
```

Deletion follows this lifecycle:

```text
ACTIVE
   ↓
DELETING
   ↓
Deleted
```

---

# Error Format

All errors follow a simple format:

```json
{
  "error": "Human readable message"
}
```

Example:

```json
{
  "error": "Project already exists"
}
```

---

# Available Servers

## Development Cluster

```
https://dcapi.lk.internal.wso2.com
```

## Local Development

```
http://localhost:8080
```

---

# Main API Categories

| Category | Purpose |
|----------|---------|
| Health | Check if the API is running |
| Projects | Create and manage projects |
| Virtual Machines | Manage VMs |
| Clusters | Manage Kubernetes clusters |
| Images | VM Images |
| Networks | Legacy networking |
| VNets | Modern virtual networking |
| Bastions | SSH gateway VMs |
| Databases | PostgreSQL instances |
| Key Vaults | Secrets management |
| DNS | Private DNS |
| Service Accounts | API credentials |
| RBAC | Roles and permissions |
| Activity | Audit logs |
| Quotas | Resource limits |
| Regions | Datacenter information |
| Agent | Communication with datacenter agents |

---

# Typical Workflow

```text
Login
   ↓
List Tenants
   ↓
List Projects
   ↓
Create Project
   ↓
Create Network
   ↓
Create VM
   ↓
VM Status = PENDING
   ↓
Poll GET VM
   ↓
VM Status = ACTIVE
```

---

# Important Endpoints

## Health Check

```http
GET /healthz
```

Response:

```json
{
  "status": "ok"
}
```

No authentication required.

---

# Projects

## List Projects

```http
GET /v1/tenants/{tenant_id}/projects
```

Returns all projects within a tenant.

## Create Project

```http
POST /v1/tenants/{tenant_id}/projects
```

Creates a new project.

## Get Project

```http
GET /v1/tenants/{tenant_id}/projects/{project_id}
```

Returns project details.

## Update Project Quota

```http
PATCH /v1/tenants/{tenant_id}/projects/{project_id}
```

Updates:

- CPU
- Memory
- Storage

## Delete Project

```http
DELETE /v1/tenants/{tenant_id}/projects/{project_id}
```

Deletes the project if it contains no resources.

---

# Virtual Machines

## List VMs

```http
GET /virtual-machines
```

Returns all VMs.

## Create VM

```http
POST /virtual-machines
```

Returns:

```http
202 Accepted
```

Response:

```text
Status = PENDING
```

The response also includes:

- Generated SSH private key
- Console password

These credentials are shown **only once**.

## Get VM

```http
GET /virtual-machines/{id}
```

Used to monitor VM status.

## Delete VM

```http
DELETE /virtual-machines/{id}
```

Marks the VM as:

```text
DELETING
```

---

# Kubernetes Clusters

Supports RKE2 clusters.

Operations include:

- Create Cluster
- Delete Cluster
- List Clusters
- Download kubeconfig
- Manage Node Pools

Cluster creation is asynchronous.

```text
POST Cluster
      ↓
PENDING
      ↓
ACTIVE
```

---

# Node Pools

Worker nodes can be managed independently.

Supported operations:

- Create worker pool
- Scale worker pool
- Add labels
- Add taints
- Delete worker pool

---

# Images

Manage VM operating system images.

Examples:

- Ubuntu
- Rocky Linux
- Windows

Supported operations:

- List Images
- Register new image from URL

---

# Networks

Supports two networking models.

## Legacy Networking

Uses Harvester NetworkAttachmentDefinitions.

```text
network_name
```

## Modern Networking

Uses KubeOVN VNets.

```text
vnet_id
subnet_id
```

---

# Bastion Hosts

A Bastion is a small VM used to SSH into private VMs.

```text
Internet
      ↓
Bastion
      ↓
Private VM
```

---

# RBAC

Users can be granted permissions at different scopes:

- Tenant
- Project
- Virtual Machine
- Cluster

The API supports:

- Create Role Assignment
- Delete Role Assignment
- List Role Assignments
- Check Permissions

---

# Regions

Supports multi-region deployments.

```text
Region
   ↓
Zone
   ↓
Agent
```

Health is derived from agent heartbeats:

```text
UP
DEGRADED
DOWN
UNKNOWN
```

---

# Agent Communication

Each datacenter runs a **dc-agent**.

The agent connects to:

```http
GET /v1/agent/ws
```

using WebSockets.

Communication flow:

```text
Agent
   │
   ├── hello
   │
   ├── ping
   │
   └── ping
          │
Server
   │
   ├── hello_ack
   │
   └── pong
```

---

# Overall Architecture

```text
User
   │
   ▼
REST API (DC-API)
   │
   ├── Authentication
   ├── Authorization
   ├── Validation
   ├── Database
   │
   ├────────► Rancher
   ├────────► Harvester
   ├────────► OpenBao
   └────────► dc-agent (WebSocket)
```

---

# Summary

DC-API is a cloud management platform that exposes REST APIs for managing cloud infrastructure. It provides operations for projects, virtual machines, Kubernetes clusters, networking, databases, key vaults, and RBAC while abstracting the complexity of the underlying Rancher, Harvester, and OpenBao infrastructure. The API is designed similarly to AWS and Azure, featuring bearer-token authentication, asynchronous provisioning, and fine-grained resource-based access control.