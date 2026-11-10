# Must-Gather — OpenShift Community Tools (OCT)

> **Community project. Not officially supported by Red Hat.**

Run `oc adm must-gather` on the local cluster from the OpenShift console and
download the archive, ready to attach to a support case.

## What it does

- Page at **Community Tools → Management → Must-Gather** (`/community-tools/management/must-gather`)
- Collect a **full**, **networking**, **storage**, **virtualization**, **serverless**, **GitOps** or **Service Mesh** must-gather.
  Operator must-gather images are detected from the installed operators (ClusterServiceVersion annotation
  `operators.openshift.io/must-gather-image`); you can override the image.
- Optionally collect **audit logs** (`gather_audit_logs`)
- Optionally **obfuscate** IP addresses, MAC addresses, domains and keywords with
  [must-gather-clean](https://github.com/openshift/must-gather-clean). The obfuscation report is a separate download and is never put in the archive.
- Packs the result as `[<case>-]must-gather-<MM-DD-YYYY-HH-MM-SS>-<cluster_id>.tar.gz` (Red Hat's recommended naming) and downloads it to your computer

Runs **as you**: the console passes your token to the runner, so you need the permissions `oc adm must-gather` needs (cluster-admin).

## Architecture

```
browser ──► console ──► /api/proxy/plugin/oct-must-gather/runner (UserToken)
                              │
                              ▼
               must-gather-runner (Go + oc + must-gather-clean)
                 oc adm must-gather ... (with your token)
                 must-gather-clean (optional)
                 tar.gz ──► download
```

| Component | Image |
| --- | --- |
| Console plugin (nginx) | `quay.io/dwaiting/oct-must-gather:0.1.1-ocp4.22` |
| Runner | `quay.io/dwaiting/oct-must-gather-runner:0.1.1-ocp4.22` |

## Build

```bash
yarn install
yarn verify
yarn test-runner

podman build -t quay.io/dwaiting/oct-must-gather:0.1.1-ocp4.22 -f Containerfile .
podman build -t quay.io/dwaiting/oct-must-gather-runner:0.1.1-ocp4.22 \
  --build-arg OCP_VERSION=4.22 -f runner/Containerfile runner/
```

## Deploy to OpenShift

```bash
oc apply -f deploy/install.yaml
oc patch consoles.operator.openshift.io cluster \
  --type=json --patch='[{"op":"add","path":"/spec/plugins/-","value":"oct-must-gather"}]'
```

## Conventions

| Convention | Value |
| --- | --- |
| Plugin ID | `oct-must-gather` |
| i18n namespace | `plugin__oct-must-gather` |
| CSS prefix | `mg-` |
| PatternFly | 6 (from `@patternfly/react-core`) |
| Image tag | `<semver>-ocp<major.minor>` (e.g. `0.1.0-ocp4.22`) |
| Route | `/community-tools/management/must-gather` |

See `AGENTS.md` for the full specification (security model, runner API) and `.cursor/rules/` for AI coding conventions.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
