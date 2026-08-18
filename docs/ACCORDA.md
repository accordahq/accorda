# Accorda

GitOps doesn’t require Kubernetes.

Working product name: Accorda
Status: Provisional name — final trademark/package/domain clearance required before commercial branding.

## 1. Project Intro

### 1.1 Vision

Accorda is a lightweight, provider-independent GitOps reconciliation system for deploying and continuously reconciling workloads from Git.

Its fundamental principle is:

Git defines the desired state. Accorda makes the runtime converge toward it.

Kubernetes should not be a prerequisite for adopting GitOps.

A developer should be able to start with:

Git
 ↓
Accorda
 ↓
Linux VPS
 ↓
Docker Compose

and later move to:

Git
 ↓
Accorda
 ↓
Kubernetes

without replacing their deployment philosophy, secrets workflow, deployment history, health model, or governance system.

The long-term progression is:

One VPS
   ↓
Several VPSs
   ↓
Docker Compose fleet
   ↓
K3s / Kubernetes
   ↓
Mixed infrastructure

while maintaining:

Git
 ↓
Desired State
 ↓
Accorda
 ↓
Reconciliation
 ↓
Actual State

## 2. Product Philosophy

Accorda should not become:

- a PaaS;
- a VPS provisioning service;
- a Terraform replacement;
- a Kubernetes distribution;
- a container registry;
- a secret-management implementation;
- a CI system;
- a Docker UI;
- another Kubernetes-only Argo CD clone.

Accorda owns one primary responsibility:

Reconcile workloads described in Git against a deployment target and prove what was actually deployed.

Infrastructure provisioning remains the responsibility of tools such as Terraform, OpenTofu, Pulumi, Ansible, cloud APIs, etc.

Accorda starts operating after infrastructure exists.

Terraform / OpenTofu / Cloud
             │
             │ creates
             ▼
     Infrastructure
             │
             ▼
          Accorda
             │
             │ reconciles
             ▼
         Workloads

## 3. Fundamental Architecture

Accorda Core must not fundamentally know about GitHub, Docker Compose, AWS, Hetzner, or any particular infrastructure provider.

The architecture should be adapter-driven.

                         ┌─────────────────────┐
                         │        Git          │
                         │                     │
                         │ GitHub              │
                         │ GitLab              │
                         │ Gitea / Forgejo     │
                         │ Bitbucket           │
                         │ Generic Git server  │
                         └──────────┬──────────┘
                                    │
                               Git Source
                                    │
                                    ▼
                    ┌───────────────────────────┐
                    │       Accorda CORE        │
                    │                           │
                    │ Desired State             │
                    │ Planning / Diff           │
                    │ Reconciliation            │
                    │ Deployment State          │
                    │ Health Verification       │
                    │ Drift Detection           │
                    │ Deployment History        │
                    │ Rollback                  │
                    │ Secret Resolution         │
                    │ Events                    │
                    └─────────────┬─────────────┘
                                  │
                             Target Driver
                     ┌────────────┴────────────┐
                     │                         │
                     ▼                         ▼
               Linux / VPS                Kubernetes
                     │                         │
              Docker Compose              Kubernetes API
                     │                         │
          ┌──────────┴─────────┐      ┌────────┴─────────┐
          │                    │      │                  │
       Hetzner              AWS VPS  EKS                K3s
       DigitalOcean         Bare     GKE                RKE2
       OVH                  Metal    AKS                On-prem
       Home server                   OpenShift          etc.

Accorda should not require a special integration for Hetzner, DigitalOcean, EC2, bare metal, or another VPS provider.

For the Compose target, prerequisites should ideally remain:

- Linux
- Docker Engine
- Docker Compose v2
- Accorda Agent

For Kubernetes:

- Kubernetes API access
- Accorda Agent

Cloud-provider-specific Kubernetes integrations should not be necessary.

## 4. Accorda OSS

Accorda OSS should be a genuinely useful standalone product.

It must remain functional indefinitely without Accorda Cloud.

The commercial strategy should not rely on deliberately crippling the OSS agent.

The OSS proposition is:

> Deploy and reconcile your own infrastructure.

The future Cloud proposition is:

> Understand, govern, and authorize deployments across your infrastructure.

## 5. Core Reconciliation Model

Accorda should distinguish between three separate concepts.

### 5.1 Desired State

What Git currently declares.

Example:

Repository: acme/infra
Branch: production
Commit: a84fd21

### 5.2 Deployed State

What Accorda successfully deployed.

Deployed commit: a84fd21
Deployment ID: dep_01K...

### 5.3 Runtime State

What is actually running right now.

For example:

api       running   healthy
worker    running
postgres  running   healthy

This distinction allows Accorda to identify situations such as:

Desired SHA     abc123
Deployed SHA    abc123
Configuration   SYNCED
Runtime         DRIFTED

For example, someone manually executes:

docker compose stop api

Git has not changed.

A simple deployment script sees nothing wrong.

Accorda should detect:

api expected: running
api actual:   stopped
DRIFT DETECTED

Depending on configuration:

reconcile:
  drift: repair

Accorda can automatically restore the desired runtime.

## 6. Reconciliation Lifecycle

A deployment should not be considered successful merely because:

`docker compose up -d`

returned exit code 0.

The lifecycle should resemble:

DETECTED
   ↓
FETCHING
   ↓
VALIDATING
   ↓
PLANNING
   ↓
PULLING
   ↓
DEPLOYING
   ↓
VERIFYING
   ↓
HEALTHY
   ↓
SYNCED

Failure paths:

DEPLOYING
    │
    ├──────────► FAILED
    │               │
    │            rollback
    │               │
    │               ▼
    │         previous version
    │              healthy
    │
    ▼
VERIFYING
    │
    └──────────► FAILED

This makes it possible to report:

```bash
Deployment       FAILED
Production       HEALTHY
Desired          d71b2e4
Currently on     a01fd92
Sync             OUT_OF_SYNC
```

This is much more informative than simply reporting: `deployment failed`

## 7. Deployment Receipts

Every successful deployment should create a deployment receipt.

Example:
```json
{
  "deployment_id": "dep_01K...",
  "repository": "acme/backend",
  "environment": "production",
  "commit": "d71b2e4",
  "started_at": "2026-08-15T18:41:59Z",
  "completed_at": "2026-08-15T18:42:07Z",
  "services": {
    "api": {
      "image": "ghcr.io/acme/api:2.4.1",
      "digest": "sha256:91a..."
    },
    "worker": {
      "image": "ghcr.io/acme/worker:2.4.1",
      "digest": "sha256:a42..."
    }
  }
}
```

The digest is important.

Git may say: `ghcr.io/acme/api:latest`

but Accorda should record: `ghcr.io/acme/api@sha256:82af...`

Accorda should therefore be capable of answering:

> Exactly which commit and container image digest was running on target X at time Y?

## 8. Docker Compose Target

Docker Compose should be the first production-quality target.

Example configuration:

```yaml
source:
  type: git
  url: git@github.com:acme/infra.git
  branch: production
  path: services/api
target:
  type: compose
  file: compose.yaml
sync:
  interval: 30s
images:
  pull: changed
reconcile:
  drift: repair
  remove_orphans: true
health:
  timeout: 120s
```

## 9. Image Pull Policies

Accorda should support explicit image pull strategies.

```yaml
images:
  pull: changed
```

Possible values:

- changed
- missing
- always
- never

### missing

Pull an image only when it is not available locally.

### always

Check/pull images on every deployment.

### never

Do not automatically pull images.

### changed

Accorda calculates which services changed and pulls only those required images.

Example:

```yaml
services:
  api:
-   image: ghcr.io/acme/api:1.8
+   image: ghcr.io/acme/api:1.9
  redis:
    image: redis:8
  postgres:
    image: postgres:17
```

Instead of effectively doing:

```shell
docker compose pull
docker compose up -d
```

Accorda can determine that only api requires reconciliation:

api       CHANGED
redis     UNCHANGED
postgres  UNCHANGED

and perform the equivalent of:

```shell
docker compose pull api
docker compose up -d api
```

where safe and semantically correct.

## 10. Service Hashing

Accorda should eventually compare normalized service configuration rather than relying exclusively on textual Git diffs.

Conceptually:

service:
  image
  command
  environment
  ports
  volumes
  networks
  labels
  healthcheck
  dependencies
       │
       ▼
 canonical representation
       │
       ▼
 SHA-256 configuration hash

Then:

Desired service hash
         │
         ▼
       compare
         ▲
         │
Deployed service hash

This helps determine whether a service actually requires recreation.

## 11. CLI

The CLI should be a major part of the OSS experience.

Binary: `accorda`

### Core commands:

`accorda init`
`accorda connect`
`accorda status`
`accorda diff`
`accorda plan`
`accorda sync`
`accorda history`
`accorda inspect`
`accorda logs`
`accorda auth`
`accorda doctor`
`accorda version`

### Accorda init

Create a Accorda project/target.

### Accorda status

Example:
```bash
Environment   production
Repository    acme/backend
Branch        main
Git HEAD      d71b2e4
Deployed      d71b2e4
Sync          SYNCED
Runtime       HEALTHY
Last deploy   2026-08-15 18:42:07
SERVICE      STATE       HEALTH      IMAGE
api          running     healthy     api:2.4.1
worker       running     -           worker:2.4.1
postgres     running     healthy     postgres:17
```

### Accorda diff

Example:

```yaml
api
  image
    deployed: ghcr.io/acme/api:2.4.0
    desired:  ghcr.io/acme/api:2.4.1
  environment
    LOG_LEVEL
      deployed: info
      desired:  warning
```

### Accorda plan

Shows exactly what Accorda intends to do without performing the deployment.

```bash
Deployment plan
Commit: a84fd21
api
  pull image      YES
  recreate        YES
  health check    YES
worker
  recreate        NO
postgres
  recreate        NO
```

### Accorda history

```bash
TIME                 COMMIT     RESULT      CHANGES
18:42                d71b2e4    ✓ healthy   api
17:13                a01fd92    ✓ healthy   worker
15:51                87bc110    ✗ failed    api
15:48                719db23    ✓ healthy   api
```

### Accorda inspect

`accorda inspect d71b2e4` could show:

```bash
api
  previous digest    sha256:abc...
  deployed digest    sha256:def...
  recreated          yes
  health             passed
postgres
  unchanged
```

## 12. Core Abstractions

The architecture should be designed around interfaces from the beginning.

Do not implement every provider immediately.

The purpose is to prevent GitHub/Docker-specific assumptions from leaking into Core.

A conceptual target interface:

```golang
type Target interface {
    Validate(ctx context.Context) error
    Current(ctx context.Context) (*State, error)
    Plan(ctx context.Context, desired *State) (*Plan, error)
    Apply(ctx context.Context, plan *Plan) error
    Health(ctx context.Context) (*Health, error)
}
```

Potential code organization:

```bash
Accorda/
├── cmd/
├── internal/
│   ├── core/
│   │   ├── state/
│   │   ├── plan/
│   │   ├── reconcile/
│   │   ├── health/
│   │   ├── history/
│   │   └── events/
│   │
│   ├── sources/
│   │   └── git/
│   │
│   ├── providers/
│   │   ├── github/
│   │   ├── gitlab/
│   │   └── gitea/
│   │
│   ├── targets/
│   │   ├── compose/
│   │   └── kubernetes/
│   │
│   ├── secrets/
│   │   └── sops/
│   │
│   └── notifications/
│       └── webhook/
│
└── docs/
```

## 13. Git Abstraction

Generic Git should be the foundation.

GitHub must not be required.

Example:

```yaml
source:
  type: git
  url: ssh://git@git.internal/acme/prod.git
  branch: production
  path: deploy/
```

Authentication:

```yaml
auth:
  type: ssh
  key: /etc/Accorda/git.key
```

Therefore this must work:

On-prem Git server
       │
       ▼
    Accorda
       │
       ▼
Bare-metal Linux
       │
       ▼
Docker Compose

with zero SaaS dependency.

## 14. Git Provider Integrations

Provider integrations add optional capabilities on top of generic Git.

```bash
Generic Git
│
├── clone
├── fetch
├── checkout
└── commit information
GitHub Provider
│
├── GitHub App authentication
├── installation tokens
├── deployment status
├── Checks
└── webhooks
GitLab Provider
│
├── authentication
├── deployment/status reporting
└── webhooks
Gitea / Forgejo
│
├── authentication
├── statuses
└── webhooks
```

Git provider integrations should be enhancements, never fundamental dependencies.

## 15. Authentication

### Generic Git

Support:

- SSH keys
- HTTPS credentials/tokens

### GitHub

Preferred architecture:

GitHub App
+
Device Flow for interactive onboarding

User experience:

Accorda connect github

Accorda displays:

Open GitHub device authentication.
Code:
F7AD-91BC

The user authenticates through their browser.

The interactive user identity and the daemon identity should be separated.

DEVICE FLOW
     │
     ▼
User authentication
     │
     ├── identify user
     ├── discover installations
     └── onboarding
GitHub App installation
     │
     ▼
short-lived installation token
     │
     ├── fetch repository
     ├── deployment status
     ├── checks
     └── statuses

Long-lived personal access tokens should not be the preferred production mechanism.

## 16. Authorization

Authorization should follow least privilege.

For GitHub App installations, users should preferably select:

Only selected repositories

instead of:

All repositories

Permissions should be limited to what Accorda requires.

For example:

Contents          Read
Metadata          Read
Deployments       Write
Checks            Write
Statuses          Write

depending on enabled functionality.

Provider permissions should remain modular.

A user using generic Git should not need GitHub permissions at all.

## 17. Secrets and SOPS

SOPS support should arrive early—before mature Kubernetes support.

Example repository:

repo/
├── compose.yaml
├── production.env.sops
└── .sops.yaml

Flow:

Git
 │
 │ encrypted secret
 ▼
Accorda
 │
 ▼
SOPS
 │
 │ temporary plaintext
 ▼
Target renderer
 │
 ▼
Docker Compose
 │
 ▼
plaintext destroyed

Accorda must not implement its own encryption algorithm.

SOPS performs cryptographic operations.

Accorda manages:

encrypted input
       ↓
controlled decryption
       ↓
deployment
       ↓
plaintext cleanup

Supported key mechanisms can eventually include those supported by SOPS, particularly:

age
cloud KMS systems
other SOPS-supported mechanisms

For initial implementation, prioritize:

SOPS + age

## 18. Secret Handling Security

Plaintext secrets should ideally never be written to persistent storage.

If temporary files are unavoidable, use locations such as:

/run/Accorda/...

with restrictive permissions:

0600

Prefer memory-backed/tmpfs locations where practical.

Plaintext should be deleted immediately after use.

Logs must never include:

environment secret values
SOPS plaintext
tokens
private keys
authorization credentials

Accorda should have explicit secret-redaction utilities shared across the codebase.

## 19. Health Verification

Accorda should understand health as part of deployment success.

For Compose services with health checks:

healthcheck:
  test: ["CMD", "curl", "-f", "http://localhost:8080/health"]
  interval: 5s
  timeout: 2s
  retries: 10

Accorda waits for the service to become healthy before declaring the deployment successful.

State should distinguish:

DEPLOYED
HEALTHY
SYNCED

These are not synonyms.

## 20. Rollback

Rollback should use known previous deployment state.

If:

commit B

fails health verification, Accorda should be able to restore:

commit A

where safely possible.

Result:

Attempted        B
Result           FAILED
Active           A
Runtime          HEALTHY
Git sync         OUT_OF_SYNC

Rollback events must be recorded in deployment history.

## 21. Events

Core should expose generic events rather than provider-specific callbacks.

Examples:

DeploymentDetected
DeploymentStarted
DeploymentSucceeded
DeploymentFailed
DeploymentRolledBack
DriftDetected
DriftReconciled
HealthChanged
AuthorizationRequired
AuthorizationGranted
AuthorizationRejected

Consumers can include:

local journal
generic webhook
GitHub
GitLab
Slack
Discord
ntfy
Accorda Cloud

## 22. GitHub Deployment Reporting

GitHub should be an optional high-quality integration.

Accorda can report:

Accorda / staging      ✓
Accorda / production   ✓

and attach deployment information to exact commits.

A report could contain:

Accorda / production
Deployment successful
Commit
d71b2e4
Changes
api
  2.4.0 → 2.4.1
worker
  unchanged
postgres
  unchanged
Verification
✓ Compose validated
✓ API image pulled
✓ API recreated
✓ Health check passed
Duration: 8.4s
Target: prod-eu-01

Equivalent concepts should be implementable for other Git providers.

## 23. Distribution

The OSS agent should be extremely easy to install.

Primary distribution should eventually include:

Linux static binary
GitHub/Git provider releases
installation script
Docker image
package managers where appropriate

Possible installation UX:

curl ... | sudo sh

followed by:

Accorda init

For Linux, Accorda should provide a systemd service.

Conceptually:

Accorda.service
     │
     ├── starts on boot
     ├── restarts on failure
     └── runs reconciliation loop

For Kubernetes:

helm install Accorda ...

should eventually install the agent.

## 24. Kubernetes Target

Kubernetes should come after the Compose implementation proves the reconciliation engine.

Accorda should communicate through the Kubernetes API.

It should not care whether the cluster is:

EKS
GKE
AKS
K3s
RKE2
OpenShift
on-prem Kubernetes

Architecture:

EKS ───────┐
GKE ───────┤
AKS ───────┤
K3s ───────┼── Kubernetes API ── Accorda
RKE2 ──────┤
On-prem ───┘

Initial Kubernetes support should intentionally remain narrow:

plain manifests
namespaces
apply
prune
SOPS
rollout health
deployment receipts

Do not initially attempt to recreate the entire feature set of Flux or Argo CD.

Defer:

complex Helm workflows
Kustomize integration
custom Accorda CRDs
advanced multi-cluster orchestration
complex progressive delivery
large plugin ecosystems

The Kubernetes implementation initially exists primarily to prove:

Accorda Core is truly target-independent.

## 25. Unified Project Format

Long-term, a Accorda project should ideally use the same concepts regardless of target.

Compose:

version: 1
environment: production
source:
  branch: main
target:
  type: compose
  path: deploy/compose.yaml
secrets:
  - deploy/prod.env.sops
health:
  timeout: 120s
notifications:
  github: true

Kubernetes:

version: 1
environment: production
source:
  branch: main
target:
  type: kubernetes
  path: deploy/kubernetes
secrets:
  provider: sops
health:
  timeout: 300s

Eventually:

target:
  type: helm

could become possible without changing Core concepts.

## 26. Accorda Cloud

Accorda Cloud should be an optional control plane, not a dependency for deployment.

Fundamental split:

Accorda OSS
"How do I deploy this safely?"
          +
Accorda CLOUD
"Should this deployment be allowed,
and what is running everywhere?"

Or more technically:

OSS   = execution + reconciliation
Cloud = visibility + governance + authorization

## 27. Cloud Architecture

Git
 │
 ▼
Accorda Cloud
 │
 ├── Deployment Plan
 ├── Vulnerability Analysis
 ├── Policies
 ├── Approvals
 ├── Deployment Windows
 ├── Audit
 └── Authorization
 │
 ▼
signed deployment authorization
 │
 ▼
Accorda Agent
 │
 ├── fetch
 ├── SOPS decrypt
 ├── reconcile
 ├── verify health
 └── report result
 │
 ▼
Compose / Kubernetes

## 28. Cloud Must Not Hold Infrastructure Credentials

This should become a major security property.

Avoid:

Accorda Cloud
      │
      │ SSH credentials
      │ Docker socket access
      │ kubeconfig
      ▼
Production

Prefer:

Accorda Cloud
      ▲
      │
      │ outbound authenticated channel
      │
Accorda Agent
      │
      ▼
Local infrastructure

Only the agent needs access to:

Docker socket
Kubernetes credentials
SOPS private keys
runtime secrets
local filesystem

Cloud should primarily receive metadata:

commit
deployment ID
image digest
deployment plan
health state
CVE metadata
approval state
timestamps
target identity

## 29. Cloud Should Not Store Application Secrets

A major architectural principle:

Accorda Cloud should not require access to decrypted application secrets.

SOPS decryption occurs locally.

Encrypted Git
     │
     ▼
Accorda Agent
     │
     ▼
local SOPS key
     │
     ▼
plaintext locally
     │
     ▼
runtime

Cloud never needs:

DATABASE_PASSWORD
JWT_SECRET
API keys
SOPS private keys
kubeconfig
Docker credentials

There may eventually be narrowly scoped exceptions for optional integrations, but the default architecture should avoid secret custody.

## 30. Outbound Agent Connectivity

Cloud connectivity should ideally require no inbound port on production servers.

Private VPS
    │
    │ outbound TLS
    ▼
Accorda Cloud

This avoids:

Internet
   │
   ▼
open Accorda agent port
   │
   ▼
production

Cloud outage must not inherently prevent OSS reconciliation.

Ideally:

Cloud unavailable
       │
       ▼
Agent continues Git polling
       │
       ▼
Local policies permitting
       │
       ▼
deployment continues

For environments explicitly configured to require Cloud authorization, behavior should be configurable and documented.

## 31. Deployment Plans

The deployment plan should become a central object shared between OSS and Cloud.

Example:

deployment: dep_01K...
environment: production
commit: a84fd21
changes:
  api:
    image:
      from: api@sha256:123
      to: api@sha256:456
    environment:
      LOG_LEVEL:
        from: info
        to: warning
  postgres:
    unchanged: true
security:
  vulnerabilities:
    critical: 0
    high: 2
    medium: 7
policy:
  status: approval_required
approvals:
  required: 2
  received: 1

This object should be deterministic enough that it can eventually be hashed and signed.

## 32. Signed Deployment Authorization

Cloud should not need the ability to remotely execute arbitrary shell commands.

Instead, after all gates pass, Cloud can issue a signed authorization.

Conceptually:

Accorda Cloud
deployment:
  commit: abc123
  target: production
  plan_hash: sha256:xyz
  expires: 02:30 UTC
          │
          ▼
        SIGN
          │
          ▼
Signed Deployment Authorization

Agent verifies:

✓ signature valid
✓ authorization not expired
✓ target matches this agent
✓ commit matches
✓ deployment plan hash matches

Only then:

DEPLOY

Cloud therefore says:

Deployment X is authorized.

It does not say:

Run arbitrary command X on production.

This materially reduces the blast radius of a Cloud compromise.

## 33. Deployment Gates

Cloud should model governance through composable deployment gates.

Git Change
    ↓
Deployment Plan
    ↓
GATES
    │
    ├── Vulnerability
    ├── Policy
    ├── Approval
    ├── Schedule
    ├── Signature
    └── Custom Integration
    │
    ▼
AUTHORIZED
    ↓
DEPLOY

Example:

gates:
  - type: vulnerability
    scanner: trivy
  - type: approval
    required: 2
  - type: schedule
    allowed: "Mon-Fri 08:00-18:00"
  - type: webhook
    url: https://policy.example.com/check

## 34. Vulnerability Scanning

Accorda should not reinvent vulnerability databases/scanners.

Integrate established scanners.

The value provided by Accorda is:

scan result
    ↓
deployment context
    ↓
policy decision

Example policy:

policies:
  vulnerabilities:
    critical:
      action: block
    high:
      max: 3
      action: approval
    medium:
      action: warn

Result:

Critical   0 ✓
High       2 ⚠
Medium     7
Decision:
MANUAL APPROVAL REQUIRED

Critical vulnerability example:

Critical   2
Decision:
BLOCKED

## 35. Vulnerability Exceptions

Organizations should eventually be able to accept known risk.

Example:

CVE-2026-12345
Severity:
HIGH
Exception:
Feature responsible for vulnerability
is disabled in this deployment.
Approved by:
security@example
Expires:
2026-09-15

Exceptions must:

have an owner
have a reason
have an expiration
be auditable

## 36. Approval System

Approval requirements should be configurable per environment.

Example:

environments:
  development:
    approvals: 0
  staging:
    approvals: 0
  production:
    approvals:
      required: 2
      groups:
        - engineering
        - sre

Risk-dependent policies should eventually be possible.

policies:
  - when:
      environment: production
    require:
      approvals: 1
  - when:
      vulnerabilities.critical: "> 0"
    block: true
  - when:
      changes.services: "> 5"
    require:
      approvals: 2
  - when:
      changes.secrets: true
    require:
      group: security

## 37. Risk-Based Deployments

Accorda Cloud can eventually assign deployment risk.

Example:

api
2.3.1 → 2.3.2
No configuration changes
No critical vulnerabilities
No infrastructure changes
Risk: LOW
AUTO-AUTHORIZED

Versus:

postgres
17 → 18
Database major version changed
Persistent volume attached
Risk: HIGH
MANUAL APPROVAL REQUIRED

The risk engine should remain transparent: users should be able to understand why a deployment was classified a certain way.

## 38. Fleet Visibility

One of Cloud’s strongest commercial features should be answering:

What is actually running everywhere?

Example:

Production
TARGET          DESIRED    DEPLOYED    HEALTH
api-eu          a18fc2     a18fc2      ✓
api-us          a18fc2     a18fc2      ✓
worker-eu       a18fc2     a18fc2      ✓
k8s-prod        a18fc2     91bd22      ⚠ DRIFT

And:

Release a18fc2
17 targets
16 healthy
1 failed

This provides value that becomes increasingly important as users move from one server to many.

## 39. Cloud Notifications

Cloud can centralize notifications for events such as:

deployment started
deployment succeeded
deployment failed
rollback occurred
approval required
approval granted
vulnerability detected
drift detected
target unhealthy

Destinations can eventually include:

email
Slack
Discord
Microsoft Teams
generic webhook
Git providers
PagerDuty-style incident systems

## 40. Audit

Commercial/enterprise customers will eventually want a durable audit trail.

Example:

Deployment dep_01K...
Commit:
a18fc2
Requested:
14:31
Security scan:
14:32
Approval:
Andrei — 14:35
Alice — 14:37
Authorization issued:
14:37
Deployment started:
14:37
Deployment healthy:
14:38

Audit records should include policy decisions and exceptions.

## 41. Future Enterprise Features

Do not build these for MVP, but architecture should not preclude:

Organizations
Teams
RBAC
SSO
SAML/OIDC
SCIM
long-term audit retention
approval groups
deployment policies
compliance exports
support/SLA
private networking options
self-hosted Cloud/control plane
custom policy integrations

## 42. OSS vs Cloud Boundary

Keep powerful deployment functionality OSS.

OSS

✓ Generic Git
✓ Docker Compose
✓ Kubernetes
✓ SOPS
✓ health checks
✓ drift detection
✓ reconciliation
✓ rollback
✓ local history
✓ deployment receipts
✓ generic webhooks
✓ provider integrations
✓ unlimited deployments

Avoid artificial limitations such as:

✗ three deployments per day
✗ one-server maximum
✗ SOPS only in Pro
✗ Kubernetes only in Pro
✗ rollback only in Pro
✗ mandatory license-server check

Cloud

Monetize coordination and governance:

Fleet dashboard
central deployment history
approvals
policies
vulnerability governance
deployment gates
signed authorization
central notifications
audit retention
RBAC
SSO
compliance
team workflows

The principle:

OSS lets you deploy anything yourself. Cloud helps you safely manage all those deployments together.

## 43. Potential Pricing Philosophy

Do not charge per deployment.

Deployment-based billing discourages users from deploying frequently, which is contrary to good delivery practices.

Potential future structure:

Community
$0
Accorda Cloud Pro
individual developers / small teams
Accorda Cloud Team
larger fleets and teams
Enterprise
security/compliance/support

A better billing dimension may eventually be:

connected targets
active nodes
organization size
governance features

rather than deployment count.

Exact pricing should be decided only after observing actual user behavior.

## 44. Roadmap

Phase 0 — Architecture & Prototype

Objective:

Prove the reconciliation loop.

Implement:

Git fetch
commit detection
Compose parsing
basic diff
docker compose execution
deployment state persistence
basic CLI

Dogfood immediately.

Do not build Cloud.

## 45. Phase 1 — Docker Compose OSS MVP

Build the first genuinely usable product.

Sources

Generic Git SSH
Generic Git HTTPS
branch tracking
polling

Polling should come before webhooks because it is simpler and works behind NAT/firewalls.

Compose

validate compose
detect changed services
image pull policies
deploy
remove orphans
health checks
deployment receipts

CLI

init
status
diff
plan
sync
history
inspect
logs
doctor

State

desired SHA
deployed SHA
runtime health
service hashes
image digests
deployment history

## 46. Phase 2 — Security & SOPS

Add:

SOPS
age
encrypted dotenv
encrypted YAML
safe temporary handling
secret redaction

Goal:

A production GitOps workflow should no longer require users to manually maintain .env files on servers.

## 47. Phase 3 — Reconciliation Hardening

Add:

drift detection
automatic repair
rollback
crash recovery
restart recovery
deployment locking
concurrent change handling
failure semantics
idempotency

This is where Accorda evolves from a deployment script into a reconciliation system.

## 48. Phase 4 — Provider Integrations

Implement the provider abstraction properly.

Start with GitHub because it provides good dogfooding opportunities.

Add:

GitHub App
Device Flow onboarding
installation tokens
deployment statuses
Checks
webhooks

Then validate abstraction through another provider, probably:

GitLab
or
Gitea/Forgejo

Do not add providers simply to create a long compatibility list.

Use them to prove that Core is provider-independent.

## 49. Phase 5 — Multi-Project / Multi-Target Compose

Support:

multiple Compose projects
multiple repositories
multiple environments
one agent managing several workloads

Example:

Accorda Agent
    │
    ├── api
    ├── worker
    ├── monitoring
    └── internal-tools

This is an important step toward fleet concepts.

## 50. Phase 6 — Kubernetes Experimental Target

Implement only enough Kubernetes functionality to validate the target abstraction.

Start with:

plain YAML
namespaces
apply
prune
SOPS
health/rollout verification
deployment receipts

Do not immediately compete feature-for-feature with Flux or Argo CD.

## 51. Phase 7 — OSS v1.0

Before calling OSS stable:

stable configuration format
migration strategy
strong documentation
security documentation
reliable upgrades
signed releases
systemd installation
Docker distribution
Helm installation
recovery behavior
backward compatibility policy
testing matrix

At this point the product should be trustworthy enough that users can run it unattended.

## 52. Phase 8 — Accorda Cloud MVP

Do not build a giant SaaS platform.

Start with three fundamental values.

See

What is running where?

Fleet visibility.

Control

Is this deployment allowed?

Approvals and policies.

Understand Risk

What are we about to deploy?

Vulnerability scanning and deployment plans.

Cloud MVP:

agent registration
outbound agent connection
fleet status
deployment plans
vulnerability scan integration
manual approvals
signed authorization
basic audit

## 53. Phase 9 — Team / Enterprise Cloud

After actual customer demand:

RBAC
teams
SSO
policy engine
approval groups
long-term audit
vulnerability exceptions
compliance features
deployment windows
enterprise support

## 54. Execution Strategy

The most important execution rule is:

Dogfood before expanding scope.

Accorda should deploy real workloads as early as possible.

A useful progression:

prototype
    ↓
deploy personal application
    ↓
find operational failures
    ↓
fix reconciliation
    ↓
deploy more workloads
    ↓
publish OSS
    ↓
external users
    ↓
Kubernetes proof
    ↓
Cloud

Do not spend a year building theoretical abstractions before running the agent in production.

Interfaces should be designed for extensibility, but implementation should remain narrow.

## 55. Testing Strategy

Testing should eventually cover several layers.

Unit

Git diff parsing
service normalization
hashing
planning
state transitions
policy evaluation
secret redaction

Integration

Run actual:

Git repository
Docker daemon
Docker Compose
SOPS

Test:

initial deployment
no-op reconciliation
image update
configuration update
failed health check
rollback
manual drift
Git force push
network failure
agent restart during deployment

Kubernetes

Later use ephemeral test clusters such as local lightweight Kubernetes environments.

End-to-End

Simulate:

Git commit
   ↓
agent detects
   ↓
plan generated
   ↓
deployment
   ↓
health verification
   ↓
receipt
   ↓
provider status

Cloud E2E:

commit
 ↓
scan
 ↓
approval
 ↓
signed authorization
 ↓
agent verifies
 ↓
deployment
 ↓
health
 ↓
audit

## 56. Security Principles

Security should be treated as part of the architecture rather than a later feature.

Core principles:

1. Least privilege.
2. Short-lived credentials where possible.
3. No unnecessary inbound agent ports.
4. Cloud does not require infrastructure credentials.
5. Cloud does not require application secrets.
6. SOPS handles encryption rather than Accorda inventing cryptography.
7. Never log secrets.
8. Record immutable image digests.
9. Deployment authorization should eventually be cryptographically verifiable.
10. Every deployment should be attributable to an exact Git state.

## 57. Threat Model Areas to Document

Before stable release, explicitly document threats around:

malicious Git commits
compromised Git credentials
compromised Accorda agent
compromised Accorda Cloud
stolen SOPS keys
Docker socket privileges
Kubernetes RBAC
supply-chain attacks
malicious container images
rollback attacks
replayed deployment authorizations
webhook spoofing
repository force-pushes
secret leakage through logs

The Docker socket in particular effectively gives extremely powerful access to the host and must be treated accordingly.

## 58. Marketing Positioning

The strongest positioning discovered so far is:

GitOps doesn’t require Kubernetes.

Alternative formulation:

Kubernetes shouldn’t be a prerequisite for GitOps.

This explains the project’s reason for existing much better than:

Docker Compose Git watcher.

## 59. Core Marketing Story

Homepage direction:

Accorda

GitOps doesn’t require Kubernetes.

Keep your workloads synchronized with Git—from a single Docker Compose VPS to Kubernetes clusters.

Git
 │
 │ desired state
 ▼
Accorda
 │
 │ reconcile
 ▼
Your infrastructure

Potential supporting statement:

Start with a VPS. Scale to Kubernetes. Keep the same GitOps workflow.

## 60. Accorda Brand Concept

Accorda communicates movement toward Git-defined state in the same linguistic pattern as:

forward
homeward
northward

The conceptual brand meaning becomes:

Move infrastructure Accorda.

Potential lines:

Keep production pointed at Git.

Git defines it. Accorda makes it so.

From desired state to actual state.

The primary slogan should nevertheless remain:

GitOps doesn’t require Kubernetes.

## 61. README Positioning

A potential opening:

# Accorda
GitOps doesn't require Kubernetes.
Accorda is a lightweight, provider-agnostic reconciliation
agent that keeps workloads synchronized with Git.
Start with Docker Compose on a single VPS.
Move to Kubernetes when you need it.
Keep the same GitOps workflow.
- Generic Git
- Docker Compose
- Kubernetes
- SOPS
- Drift detection
- Health verification
- Deployment history
- No control plane required

## 62. Competitive Positioning

Accorda should not market itself primarily as:

"better Argo CD"

or:

"Flux replacement"

Argo CD and Flux are mature Kubernetes-native GitOps systems.

Accorda’s differentiation should instead be:

                    GitOps
              Accorda philosophy
                     │
          ┌──────────┴──────────┐
          │                     │
      Linux/VPS             Kubernetes
          │                     │
    Docker Compose          K8s workloads

The message:

Kubernetes is one possible deployment target—not the prerequisite for getting GitOps semantics.

Existing Compose GitOps/deployment tools, particularly Doco-CD, Komodo and Portainer-style Git deployments, should be continuously studied because they overlap more directly with the initial Compose use case.

Differentiation should eventually come from the combination of:

target-independent reconciliation
deployment receipts
drift
health
SOPS
provider abstraction
Compose + Kubernetes continuity
Cloud governance
signed authorization

rather than simply Git polling.

## 63. Open-Source Strategy

The repository should preferably begin as an independent public project rather than being hidden inside another product.

A clean relationship is:

Application / existing product
            │
            │ consumer
            ▼
         Accorda

Accorda should know nothing about any particular consuming application.

Using a real private application as the first consumer is excellent dogfooding because it proves:

Accorda owner/account
       ≠
application organization

and therefore forces authentication, authorization, repository access and deployment architecture to work like a genuine external product.

## 64. GitHub Organization Strategy

Accorda is stored at [Accord GitHub Org](https://github.com/accordahq/accorda), because:

- multiple maintainers exist
- multiple repositories appear
- public GitHub App branding matters
- website/docs become separate projects
- contributors expect neutral ownership
- commercial identity develops

## 65. Naming and Trademark Warning

Accorda should currently be treated as a working name, not a legally cleared trademark.

Before investing materially in branding:

- [x] GitHub exact-name search
- [ ] package registries
- [x] container registries
- [ ] domain names
- [ ] search engines
- [ ] EU trademark databases
- [ ] German trademark database
- [ ] USPTO
- [ ] relevant international databases
- [ ] company registers where appropriate
- [ ] existing DevOps products

should be checked.

A professional trademark search becomes worthwhile before:

- paid launch
- significant advertising
- company formation around the brand
- trademark filing
- major design expenditure

Do not assume that availability of:

Accorda.dev

or:

github.com/.../Accorda

means the trademark is legally safe.

## 66. License

The exact OSS license needs a deliberate decision.

Possible philosophy:

Permissive

Examples include Apache-2.0-style licensing.

Advantages:

- easy adoption
- easy enterprise use
- easy integrations
- strong OSS credibility

Potential disadvantage:

- A third party may commercially host/fork the software.

Copyleft

Can provide stronger requirements around redistribution/modifications but may create adoption concerns for some companies.

Because the intended commercial boundary is:

OSS execution
+
paid hosted governance

a permissive license may align well with adoption, but the decision should be made after considering commercial strategy and obtaining appropriate legal advice if the project becomes material.

Do not casually change licenses after significant outside contributions without understanding contributor rights.

## 67. Contributor Governance

Before accepting substantial external contributions, define:

- [ ] LICENSE
- [ ] CONTRIBUTING.md
- [ ] Code of Conduct if desired
- [ ] security reporting policy
- [ ] copyright policy
- [ ] release process
- [ ] maintainer responsibilities

Decide whether the project needs DCO or CLA before a large contributor community forms.

Do not introduce a CLA merely because other projects have one; choose the contribution model intentionally.

## 68. Security Disclosure

Before significant adoption, publish:

- [ ] `SECURITY.md` containing:
  - supported versions
  - private vulnerability reporting method
  - expected response process
  - what information reporters should include

Infrastructure software will eventually attract security research.

Make responsible disclosure easy.

## 69. Legal / Business Disclaimer

The legal and company-formation sections here are product-planning guidance, not jurisdiction-specific legal, tax, trademark, employment, or corporate advice.

Before taking significant commercial steps, consult appropriate professionals in the jurisdiction where the business will operate.

## 70. When a Company Is Probably Not Necessary

During early experimentation, a separate company may not be worth creating immediately if the activity is essentially:

- open-source development
- prototype
- no revenue
- no employees
- no contracts
- no significant liabilities

It may be reasonable to validate whether anyone actually wants Accorda first.

Do not create corporate complexity solely because the repository exists.

## 71. When Company Formation Becomes Worth Considering

Reevaluate when one or more of these become real:

- Accorda Cloud accepts payments
- customers sign contracts
- business liabilities increase
- employees/contractors contribute
- multiple founders own the project
- external investment is considered
- enterprise customers require an entity
- commercial trademarks/IP become valuable
- meaningful recurring revenue exists

Cloud is likely the natural point where a legal entity becomes materially more important.

The company could own:

- Accorda trademark
- Accorda Cloud
- domains
- commercial contracts
- Cloud infrastructure
- commercial IP

while the OSS repository remains publicly available under its chosen license.

## 72. Trademark Timing

Do not necessarily file trademarks before validating the name.

But do a basic clearance before becoming emotionally or financially attached to the brand.

A more serious trademark strategy becomes sensible before:

- Cloud launch
- paid marketing
- conference sponsorship
- significant customer acquisition
- international expansion

Trademark rights and filing rules vary by jurisdiction, so professional advice is appropriate when the brand acquires meaningful value.

## 73. Privacy and Cloud Legal Requirements

The OSS agent can have a very small privacy footprint.

Cloud is different.

Once Accorda Cloud receives data such as:

- user accounts
- email addresses
- organization membership
- repository metadata
- deployment metadata
- IP addresses
- audit records
- security scan information

privacy obligations become relevant.

Before commercial Cloud launch, plan for:

- Privacy Policy
- Terms of Service
- Data Processing Agreement where needed
- subprocessor documentation
- data retention/deletion
- account deletion
- security incident process
- backup policy
- regional privacy requirements

European customers may create additional GDPR-related obligations.

Professional legal review is appropriate before a serious commercial launch.

## 74. Security Claims and Marketing

Avoid absolute statements such as:

> "Accorda Cloud cannot be hacked."

Prefer technically precise claims:

> Accorda Cloud does not require your Docker socket, kubeconfig, SOPS private keys, or decrypted application secrets.

That is both more defensible and more meaningful.

Similarly:

> Agents establish outbound connections to Accorda Cloud; production hosts do not need to expose an inbound Accorda management port.

Concrete architecture is better marketing than vague security adjectives.

## 75. Product Metrics

During OSS development, don’t optimize for vanity metrics alone.

Useful signals:

- active installations
- number of real targets
- repeat deployments
- upgrade retention
- GitHub stars
- external issues
- external contributors
- number of users running >1 target
- percentage using SOPS
- percentage using drift reconciliation

The strongest Cloud signal is likely:

Users have enough targets that understanding and controlling deployments has become painful.

That is the moment Cloud becomes valuable naturally.

## 76. Major Product Risks

Scope explosion

Going from Compose to Kubernetes to Cloud to security scanning can easily become several products.

Mitigation:

- Compose first
- dogfood
- generic core
- minimal Kubernetes
- Cloud only after OSS works

Competing with Flux/Argo CD

Do not attempt to win by recreating every Kubernetes feature.

Win through:

- VPS-first GitOps
- target independence
- simple agent
- consistent workflow

Competing with existing Compose tools

Git polling alone is insufficient differentiation.

Accorda must build toward:

- real reconciliation
- drift detection
- deployment proof
- security model
- cross-target consistency
- governance

Cloud becoming mandatory

Avoid architecture that makes users distrust OSS.

The agent must remain independently useful.

Security complexity

Accorda touches extremely privileged systems.

Treat security work as core engineering rather than polish.

## 77. North-Star Architecture

The eventual system should look approximately like this:

                           GIT PROVIDERS
             GitHub      GitLab      Gitea      Generic
                \          |           |          /
                 \         |           |         /
                  └────────┴───────────┴────────┘
                              │
                              ▼
                         Desired State
                              │
                              ▼
                    ┌──────────────────┐
                    │                  │
                    │   Accorda CORE   │
                    │                  │
                    │ Fetch            │
                    │ Resolve Secrets  │
                    │ Plan             │
                    │ Reconcile        │
                    │ Verify           │
                    │ Detect Drift     │
                    │ Record Receipt   │
                    │ Emit Events      │
                    │                  │
                    └────────┬─────────┘
                             │
                 ┌───────────┴────────────┐
                 │                        │
                 ▼                        ▼
         DOCKER COMPOSE              KUBERNETES
                 │                        │
                 ▼                        ▼
             Linux VPS               K8s API
                 │                        │
        any infrastructure         any provider
                  OPTIONAL CONTROL PLANE
                    ┌──────────────────┐
                    │  Accorda CLOUD   │
                    │                  │
                    │ Fleet            │
                    │ Vulnerabilities  │
                    │ Policies         │
                    │ Approvals        │
                    │ Audit            │
                    │ Authorization    │
                    └────────┬─────────┘
                             │
                    signed authorization
                             │
                             ▼
                       Accorda Agent
                             │
                             ▼
                          Runtime

## 78. The Product in One Sentence

Accorda is a provider-agnostic GitOps reconciler that keeps workloads synchronized with Git—from Docker Compose on a single Linux server to Kubernetes clusters.

The commercial extension:

> Accorda Cloud adds visibility, vulnerability intelligence, policies, approvals and auditable authorization without requiring access to your application secrets or infrastructure credentials.

And the core belief remains:

GitOps doesn’t require Kubernetes.

That should guide both the architecture and the marketing.

## 79. Immediate Execution Plan

The next engineering steps should stay intentionally small.

Step 1 — Validate the name

Before branding:

Accorda collision search
GitHub
domains
package registries
EU/German/US trademark reconnaissance

Step 2 — Create the public repository

Start independently from any consuming application.

Step 3 — Write architecture boundaries

Document:

Source
Provider
Target
SecretResolver
EventSink
State
Plan
Receipt

before implementing provider-specific integrations.

Step 4 — Implement generic Git polling

clone
fetch
branch
commit detection
checkout

No GitHub API required.

Step 5 — Implement Compose reconciliation

parse
validate
normalize
diff
plan
pull
deploy
verify
record

Step 6 — Implement the minimum CLI

Accorda init
Accorda status
Accorda diff
Accorda plan
Accorda sync
Accorda history

Step 7 — Dogfood on a real private application

Use Accorda exactly like an unrelated external user would.

Step 8 — Add SOPS + age

Make encrypted Git configuration a first-class workflow.

Step 9 — Add drift + rollback

At this point Accorda becomes a real reconciler rather than a deployment watcher.

Step 10 — Add GitHub as the first rich provider

GitHub App
Device Flow
installation auth
Checks
Deployments
webhooks

Step 11 — Publish an early OSS release

Get real external feedback before Kubernetes scope expands.

Step 12 — Implement minimal Kubernetes target

Use it primarily to validate the architecture.

Step 13 — Stabilize OSS

Aim for boring reliability.

Step 14 — Build Cloud protocol

Design:

agent identity
target identity
event transport
deployment plans
signed authorization
Cloud/agent failure behavior

Step 15 — Build the smallest sellable Cloud

Only:

fleet visibility
vulnerability gates
approvals
signed authorization
audit

Then let actual users determine what comes next.

## 80. Final Product Principle

Whenever a new feature is proposed, ask:

Does this help Accorda reconcile desired state, prove actual state, or safely govern the transition between them?

If not, it probably belongs in another tool.

The long-term product should remain understandable as:

Git defines what should run.
Accorda determines what changed.
Accorda safely makes it run.
Accorda verifies what is actually running.
Accorda records exactly what happened.
Accorda Cloud decides whether production changes are authorized.

That is the product.