# AGENTS.md — Must-Gather (OCT extension)

This is **OpenShift Community Tools (OCT)**, a **community project**, not an official Red Hat supported product. Do not describe it as official Red Hat software.

This repository is the **Must-Gather** ConsolePlugin: run `oc adm must-gather` against the local cluster from the console and download the archive. It is **not** the OCT storefront. Catalog hubs live in `oct-storefront`.

## Identifiers

| | Value |
| --- | --- |
| Plugin ID / ConsolePlugin / `package.json` `consolePlugin.name` | **`oct-must-gather`** |
| Plugin image | `quay.io/dwaiting/oct-must-gather:0.1.1-ocp4.22` (`<semver>-ocp<major.minor>`) |
| Companion image (runner) | `quay.io/dwaiting/oct-must-gather-runner:0.1.1-ocp4.22` |
| i18n | `plugin__oct-must-gather` |
| Route | `/community-tools/management/must-gather` |
| Proxy | `/api/proxy/plugin/oct-must-gather/runner` → Service `must-gather-runner:8443` |

Display name is **Must-Gather**. CSS prefix `mg-`. No PVC (the runner uses an `emptyDir`).

**Current version:** `0.1.1` (package.json / consolePlugin.version).

## What this plugin owns

- One page at `/community-tools/management/must-gather` (`src/components/MustGatherPage.tsx`, `JobCard.tsx`, helpers in `mustGather.ts`).
- The **runner** (`runner/`, Go, stdlib only): a small HTTPS service that runs `oc adm must-gather`, `must-gather-clean` and builds the archive.

### Must-gather types

| Type | How it runs |
| --- | --- |
| Full | `oc adm must-gather` (cluster default image, `openshift/must-gather` ImageStream) |
| Networking | `oc adm must-gather -- /usr/bin/gather_network_logs` |
| Storage, Virtualization, Serverless, GitOps, Service Mesh | `oc adm must-gather --image=<operator image>` |

Operator images are **read from the cluster**, never hardcoded: the UI watches ClusterServiceVersions (skipping OLM copies, label `olm.copiedFrom`) and uses the `operators.openshift.io/must-gather-image` annotation (what `oc adm must-gather --all-images` uses), falling back to a `spec.relatedImages` entry containing `must-gather`. CSV name prefixes per type are in `getGatherTypes()` (`mustGather.ts`). The user can override the image.

### Options

- **Audit logs**: a second pass, `oc adm must-gather -- /usr/bin/gather_audit_logs`, into the same directory.
- **Obfuscation**: `must-gather-clean` with a generated config (IP, MAC, Domain, Keywords; optional omit of Secrets/CSRs). Domains default to `dns.config.openshift.io/cluster` `spec.baseDomain`. The raw copy is deleted before archiving. The `report.yaml` (original → replacement map) is kept **outside** the archive and offered as a separate download with a warning not to share it.
- **Node disk check**: must-gather's disk usage checker kills the gather script when the node filesystem backing the gather pod's `emptyDir` is more than its limit used (oc `--volume-percentage`, default 70); oc still copies what was collected and exits 0. The page (`nodeDisk.ts`) reads kubelet `stats/summary` (`/api/v1/nodes/<node>/proxy/stats/summary`, `node.fs`) for the nodes oc may schedule on (Ready, schedulable control plane nodes, mirroring oc's `getCandidateNodeNames`) and computes usage like `df` (`used / (used + available)`, rounded up). Rechecked every 60 s and on **Check again**; if stats cannot be read it warns but does not block.
  - All candidates ≤ 70%: normal run, oc default limit.
  - Fullest node above 70% and below 90%: warning, and the button becomes **Run anyway (disk limit N%)**, which sends `volumePercentage` = fullest node + 10, capped at **90** (`runAnywayLimit`). The runner passes it as `--volume-percentage` to both passes and rejects anything above 90 (`maxVolumePercentage`).
  - Fullest node ≥ 90%: error, run disabled (the gather would stop immediately at the 90% cap).
  - Safety net: the runner scans each `*/gather.logs` for `[disk usage checker] Disk usage exceeds` and **fails** the job instead of shipping a near-empty archive.
- **Support case number** (8 digits): prefixed to the file name; Red Hat SFTP auto-attaches files whose name starts with a case number.

### Archive

`[<case>-]must-gather-<MM-DD-YYYY-HH-MM-SS>-<cluster_id>.tar.gz` (UTC; cluster ID from `ClusterVersion` `spec.clusterID`), one top-level directory `must-gather.local.<digits>/`, as in Red Hat's documented `tar cvaf must-gather-$(date +"%m-%d-%Y-%H-%M-%S")-<cluster_id>.tar.gz <must_gather_local_dir>`. The browser downloads it automatically when a job started in this session succeeds, and from the job card any time until the TTL expires.

## Security model

- ConsolePlugin `spec.proxy` uses `authorization: UserToken`: the console adds the signed-in user's bearer token. The runner uses **only that token** (kubeconfig written 0600 per job, deleted after gathering, token dropped when the job ends). The runner ServiceAccount has **no RBAC** and `automountServiceAccountToken: false`; it trusts the API server via the `kube-root-ca.crt` ConfigMap.
- So a user can only gather what `oc adm must-gather` would let them gather (needs to create namespaces, cluster role bindings and pods: in practice cluster-admin). The UI shows a warning from `useAccessReview` when that is missing.
- Jobs belong to the user that created them (`users/~`); other users get 404.
- One job at a time (cluster-wide). Jobs and archives live in memory/`emptyDir`; deleted after `JOB_TTL` (default `4h`), on Delete, and on pod restart.
- Cancel sends SIGINT to `oc`, which deletes its temporary `openshift-must-gather-*` namespace (`terminationGracePeriodSeconds: 120` for the same reason).

### Runner API (behind the proxy)

`GET /api/jobs` · `POST /api/jobs` · `GET /api/jobs/{id}` · `POST /api/jobs/{id}/cancel` · `DELETE /api/jobs/{id}` · `GET /api/jobs/{id}/archive` · `GET /api/jobs/{id}/report` · `GET /healthz` (no auth). Requests are validated server-side (type allowlist, image/domain/case regexes); the gather command is chosen by the server, never by the client.

## Companion images

| Image | Contents | Version |
| --- | --- | --- |
| `oct-must-gather-runner` | `mg-runner`, `oc` (mirror.openshift.com `stable-<OCP_VERSION>`, sha256-verified), `must-gather-clean` (GitHub release `v0.0.5`, sha256-verified), `rsync` on UBI 9 minimal | `0.1.1` |

Build the runner per OCP minor so `oc` matches: `--build-arg OCP_VERSION=4.22` for `-ocp4.22`, `4.21` for `-ocp4.21`. Both images get both tags (see `oct-semver.mdc`).

## OpenShift and extension versions

Two axes in the catalog: git tag **`v0.x.x`** (semver) and optional branch **`ocp-X.Y`** when PatternFly or APIs diverge. Image tags **always** `<semver>-ocp<major.minor>`. **Always publish both** `<semver>-ocp4.21` and `<semver>-ocp4.22`. `main` tracks **4.22**, PatternFly 6.

## Navigation

This plugin **does not** register the Community Tools section or hubs. Open from the storefront **Management** tile or `/community-tools/management/must-gather`.

## No environment-specific hardcoding

Never bake in lab hostnames, domains, StorageClasses or must-gather image versions. Domains and images come from the cluster. See `.cursor/rules/oct-no-env-hardcoding.mdc`.

## Catalog tile

Storefront `catalog/community.yaml`: `metadata.name: oct-must-gather`, `consolePlugin: oct-must-gather`, `spec.href: /community-tools/management/must-gather`, `category: management`. The bundle must be `catalog/deploy/oct-must-gather.yaml` (copy of `deploy/install.yaml`): the generated Namespace/Deployment/Service/ConsolePlugin is **not** enough, it needs the runner Deployment, Service, ServiceAccount and the ConsolePlugin `proxy`. **Do not list `versions[]` until both combined public image tags exist.**

## Add must go Ready

Follow **oct-storefront** `docs/extension-standard.md`. Confirm **both** Deployments (`oct-must-gather` and `must-gather-runner`) are Running.

## PatternFly 6

Import from `@patternfly/react-core` ^6. Do **not** import PatternFly CSS. Prefix CSS `mg-`. Include `CommunityDisclaimer`.

## Do-not-break list

- Route: `/community-tools/management/must-gather`
- Plugin ID `oct-must-gather`, proxy alias `runner`

## Verify

```bash
yarn install
yarn verify        # ts-check, check-i18n, check-repo, build (same as CI)
yarn test-runner   # go vet + go test for runner/
```

Do not `oc apply` or push images unless asked.
