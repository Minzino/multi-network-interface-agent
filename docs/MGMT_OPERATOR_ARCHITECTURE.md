# Multinic MGMT Operator and BIZ CR Workflow

This document describes the end-to-end architecture, how MultiNicNodeConfig CRs are created, and how an optional middle API can be structured. It is written for the MGMT cluster operator implementation and the BIZ cluster controller/agent behavior.

## 1. Goal and Constraints

- MGMT cluster collects NIC data from OpenStack and drives configuration on BIZ cluster nodes.
- BIZ cluster runs multinic-agent/controller and only needs CRs to act.
- CR name MUST equal nodeName to match current controller lookup logic.
- instanceId is recorded in spec and label; it is NOT used as the CR name.
- status is controller-owned; MGMT/Viola should not write status.

## 2. Components

- OpenStack API
  - Source of instance UUID, ports, MAC, IP, MTU, subnet.
- MGMT cluster
  - multinic-operator (to build and submit CRs)
  - OpenstackConfig CR (input for operator)
- Middle API (optional)
  - Service that accepts batched node configs and upserts CRs in BIZ
- BIZ cluster
  - MultiNicNodeConfig CRD
  - multinic-agent-controller (creates per-node Jobs)
  - multinic-agent (applies NIC settings on node)

## 3. Data Flow (Two Options)

Option A: MGMT -> Middle API (Viola) -> BIZ

1) Operator watches OpenstackConfig in MGMT.
2) Operator calls OpenStack and builds a list of node configs.
3) Operator POSTs the list to Middle API.
4) Middle API maps providerId to BIZ cluster and upserts CRs.
5) BIZ controller watches CRs and creates per-node Jobs.
6) Agent applies NIC config and controller updates status.

Option B: MGMT -> BIZ directly

1) Operator watches OpenstackConfig in MGMT.
2) Operator calls OpenStack and builds a list of node configs.
3) Operator authenticates to BIZ kube-apiserver and upserts CRs.
4) BIZ controller/agent process as usual.

## 4. CRD and CR Format

### 4.1 CRD Summary

- Group: multinic.io
- Version: v1alpha1
- Kind: MultiNicNodeConfig
- Namespace: multinic-system
- Name: nodeName (required)

### 4.2 Spec Fields

- spec.nodeName (string, required)
- spec.instanceId (string, optional but recommended)
- spec.interfaces (array, required)
  - id (int, optional)
  - portId (string, optional)
  - macAddress (string, required)
  - address (string, optional)
  - cidr (string, optional)
  - mtu (int, optional)

### 4.3 Labels

- metadata.labels["multinic.io/instance-id"] = instanceId
- metadata.labels["multinic.io/node-name"] = nodeName (recommended)

### 4.4 Status (Controller-managed)

- status.state: Pending | InProgress | Configured | Failed
- status.interfaceStatuses: array with name field

Example of status.interfaceStatuses entry:

- name: multinic0
  interfaceIndex: 0
  id: 1
  macAddress: fa:16:3e:2f:45:3c
  address: 192.168.192.25
  cidr: 192.168.192.0/24
  mtu: 1450
  status: Configured
  reason: JobSucceeded
  lastUpdated: 2026-01-06T04:17:44Z

## 5. How to Create/Upsert CRs (MGMT -> BIZ)

### 5.1 Required Inputs

- nodeName (must match actual BIZ Kubernetes node name)
- instanceId (OpenStack instance UUID)
- interfaces[] with MAC and addressing info

### 5.2 Mapping from OpenStack to CR

Recommended mapping from OpenStack port to CR interface item:

- id: stable integer index (use list order or port index)
- portId: OpenStack port UUID (if needed later)
- macAddress: port MAC
- address: fixed IP
- cidr: subnet CIDR
- mtu: network MTU (if available)

### 5.3 Upsert Algorithm

1) Determine CR name = nodeName.
2) Check if CR exists in BIZ namespace.
3) If not exists: create with metadata.name=nodeName and spec fields.
4) If exists: update only spec (do not touch status).
5) Ensure labels are set/updated for instanceId and nodeName.

Notes:
- Keep spec.interfaces order stable to keep interfaceIndex and name stable.
- Do not reorder or drop entries unless intended; it will shift interface names.

### 5.4 Minimal CR Example (YAML)

apiVersion: multinic.io/v1alpha1
kind: MultiNicNodeConfig
metadata:
  name: test-cluster-24
  namespace: multinic-system
  labels:
    multinic.io/node-name: test-cluster-24
    multinic.io/instance-id: 098d1b84-e2eb-4164-b098-9e12b5fcfaaa
spec:
  nodeName: test-cluster-24
  instanceId: 098d1b84-e2eb-4164-b098-9e12b5fcfaaa
  interfaces:
  - id: 1
    name: multinic0
    macAddress: fa:16:3e:2f:45:3c
    address: 192.168.192.25
    cidr: 192.168.192.0/24
    mtu: 1450

## 6. Middle API (Viola) Design

### 6.1 Why a Middle API

- Centralized BIZ cluster access and credentials.
- Batch processing and auditing.
- Provider ID routing (one API to many clusters).

### 6.2 Endpoint (Suggested)

POST /v1/k8s/multinic/node-configs
Headers:
- x-provider-id: string (required)
- content-type: application/json

Body:
- array of node configs (MultiNicNodeConfig spec-like objects)

Request example:

[
  {
    "nodeName": "test-cluster-24",
    "instanceId": "098d1b84-e2eb-4164-b098-9e12b5fcfaaa",
    "interfaces": [
      {
        "id": 1,
        "name": "multinic0",
        "macAddress": "fa:16:3e:2f:45:3c",
        "address": "192.168.192.25",
        "cidr": "192.168.192.0/24",
        "mtu": 1450
      }
    ]
  }
]

### 6.3 Response (Suggested)

Return per-node results to make partial success visible:

{
  "results": [
    {
      "nodeName": "test-cluster-24",
      "status": "updated",
      "message": "CR updated",
      "resourceVersion": "4962542"
    }
  ],
  "errors": [
    {
      "nodeName": "worker-2",
      "code": "VALIDATION_ERROR",
      "message": "macAddress is required"
    }
  ]
}

### 6.4 Validation Rules (Suggested)

- nodeName required and non-empty
- interfaces must be non-empty array
- each interface must include macAddress
- macAddress must match pattern (xx:xx:xx:xx:xx:xx)
- mtu range 68..9000 (optional)

### 6.5 Idempotency and Upsert

- Upsert by name=nodeName
- If spec differs: update
- If identical: no-op
- Do not modify status

### 6.6 Error Codes (Suggested)

- 400: invalid request schema
- 401/403: auth failure
- 404: unknown provider ID
- 409: conflict (optional)
- 500: internal error

## 7. Direct BIZ API (MGMT -> BIZ)

If you skip the middle API, MGMT operator must manage:

- Kubeconfig per BIZ cluster or token-based access
- RBAC in BIZ allowing create/get/patch MultiNicNodeConfig in multinic-system
- Network reachability from MGMT to BIZ API server
- Secure secret storage (Kubernetes Secret in MGMT)

Suggested RBAC in BIZ:
- verbs: get, list, watch, create, patch, update on multinicnodeconfigs
- namespace: multinic-system

## 8. Operator Reconcile Logic (MGMT)

High-level reconcile steps:

1) Watch OpenstackConfig CRs.
2) For each config, fetch VM list from OpenStack.
3) For each VM:
   - resolve instance UUID
   - resolve k8s nodeName (must match BIZ node)
   - list ports and IPs
   - build interfaces array
4) Build request payload for all nodes (batch).
5) Submit to Middle API or BIZ API.
6) Log per-node result and retry on transient errors.

## 9. Sequence Diagram (ASCII)

OpenStack -> MGMT operator -> (Middle API) -> BIZ API -> Controller -> Agent

OpenStack: list servers/ports
MGMT: build node configs
Middle API: map providerId to kubeconfig
BIZ: create/update CRs
Controller: create Job per node
Agent: apply NIC config, exit summary
Controller: update status

## 10. Testing Checklist

- Create CR with 1 interface and verify Configured.
- Add second interface and verify status list has 2 entries with name.
- Validate preflight failure (in-use interface) returns FailedPartial.
- Confirm operator does not write status.
- Confirm name=nodeName and label instance-id are set.
