import { TFunction } from 'i18next';
import { consoleFetchJSON, K8sResourceCommon } from '@openshift-console/dynamic-plugin-sdk';

export const I18N = 'plugin__oct-must-gather';
export const PLUGIN_VERSION = '0.1.1';

/** Console proxy to the runner Service (ConsolePlugin spec.proxy alias "runner"). */
export const RUNNER_BASE = '/api/proxy/plugin/oct-must-gather/runner';

export type GatherTypeId =
  | 'full'
  | 'networking'
  | 'storage'
  | 'virtualization'
  | 'serverless'
  | 'gitops'
  | 'servicemesh';

export type GatherTypeDef = {
  id: GatherTypeId;
  label: string;
  description: string;
  /**
   * Operator-provided must-gather. The image is read from the operator's
   * ClusterServiceVersion; csvPrefixes are OLM package names, in order of
   * preference.
   */
  operator?: { product: string; csvPrefixes: string[] };
};

export const getGatherTypes = (t: TFunction): GatherTypeDef[] => [
  {
    id: 'full',
    label: t('Full (default)'),
    description: t(
      'Cluster-wide data from the default OpenShift must-gather image: cluster operators, nodes, core namespaces and events.',
    ),
  },
  {
    id: 'networking',
    label: t('Networking'),
    description: t(
      'Network logs (OVN-Kubernetes databases and node network data) using the gather_network_logs script of the default image.',
    ),
  },
  {
    id: 'storage',
    label: t('Storage'),
    description: t('OpenShift Data Foundation or LVM Storage data, using the must-gather image shipped with the operator.'),
    operator: {
      product: t('OpenShift Data Foundation / LVM Storage'),
      csvPrefixes: ['odf-operator', 'ocs-operator', 'lvms-operator'],
    },
  },
  {
    id: 'virtualization',
    label: t('Virtualization'),
    description: t('OpenShift Virtualization data, using the must-gather image shipped with the operator.'),
    operator: { product: t('OpenShift Virtualization'), csvPrefixes: ['kubevirt-hyperconverged-operator'] },
  },
  {
    id: 'serverless',
    label: t('Serverless'),
    description: t('OpenShift Serverless (Knative) data, using the must-gather image shipped with the operator.'),
    operator: { product: t('OpenShift Serverless'), csvPrefixes: ['serverless-operator'] },
  },
  {
    id: 'gitops',
    label: t('GitOps'),
    description: t('OpenShift GitOps (Argo CD) data, using the must-gather image shipped with the operator.'),
    operator: { product: t('OpenShift GitOps'), csvPrefixes: ['openshift-gitops-operator'] },
  },
  {
    id: 'servicemesh',
    label: t('Service Mesh'),
    description: t('OpenShift Service Mesh data, using the must-gather image shipped with the operator.'),
    operator: { product: t('OpenShift Service Mesh'), csvPrefixes: ['servicemeshoperator3', 'servicemeshoperator'] },
  },
];

export const MUST_GATHER_IMAGE_ANNOTATION = 'operators.openshift.io/must-gather-image';

export type CSVKind = K8sResourceCommon & {
  spec?: {
    displayName?: string;
    version?: string;
    relatedImages?: { name?: string; image: string }[];
  };
  status?: { phase?: string };
};

export type DetectedImage = { image: string; csv: string; displayName: string };

/** The operator must-gather image: CSV annotation (what `oc adm must-gather --all-images` uses), else relatedImages. */
export const mustGatherImageFromCSV = (csv: CSVKind): string | undefined =>
  csv.metadata?.annotations?.[MUST_GATHER_IMAGE_ANNOTATION] ||
  csv.spec?.relatedImages?.find(
    (ri) => /must-gather/i.test(ri.name || '') || /must-gather/i.test(ri.image),
  )?.image;

export const detectImage = (csvs: CSVKind[], prefixes: string[]): DetectedImage | undefined => {
  for (const prefix of prefixes) {
    const matches = csvs
      .filter((c) => (c.metadata?.name || '').startsWith(`${prefix}.`))
      .sort((a, b) => (a.status?.phase === 'Succeeded' ? -1 : 0) - (b.status?.phase === 'Succeeded' ? -1 : 0));
    for (const csv of matches) {
      const image = mustGatherImageFromCSV(csv);
      if (image) {
        return { image, csv: csv.metadata?.name || '', displayName: csv.spec?.displayName || prefix };
      }
    }
  }
  return undefined;
};

export type ObfuscateOptions = {
  ip: boolean;
  mac: boolean;
  domains: string[];
  keywords: string[];
  omitSecrets: boolean;
};

export type JobRequest = {
  type: GatherTypeId;
  image?: string;
  auditLogs: boolean;
  obfuscate?: ObfuscateOptions;
  caseNumber?: string;
  volumePercentage?: number;
};

export type JobPhase =
  | 'Pending'
  | 'Gathering'
  | 'Obfuscating'
  | 'Archiving'
  | 'Succeeded'
  | 'Failed'
  | 'Canceled';

export type JobStatus = {
  id: string;
  type: GatherTypeId;
  image?: string;
  auditLogs: boolean;
  obfuscated: boolean;
  caseNumber?: string;
  phase: JobPhase;
  message?: string;
  warning?: string;
  created: string;
  finished?: string;
  archiveName?: string;
  archiveSize?: number;
  hasReport: boolean;
  log: string[];
};

export type JobList = { jobs: JobStatus[]; busy: boolean; retainedFor: string };

export const isActive = (p: JobPhase) => !['Succeeded', 'Failed', 'Canceled'].includes(p);

export const listJobs = (): Promise<JobList> => consoleFetchJSON(`${RUNNER_BASE}/api/jobs`);
export const createJob = (req: JobRequest): Promise<JobStatus> =>
  consoleFetchJSON.post(`${RUNNER_BASE}/api/jobs`, req);
export const cancelJob = (id: string): Promise<JobStatus> =>
  consoleFetchJSON.post(`${RUNNER_BASE}/api/jobs/${encodeURIComponent(id)}/cancel`, {});
export const deleteJob = (id: string): Promise<unknown> =>
  consoleFetchJSON.delete(`${RUNNER_BASE}/api/jobs/${encodeURIComponent(id)}`);
export const archiveURL = (id: string) => `${RUNNER_BASE}/api/jobs/${encodeURIComponent(id)}/archive`;
export const reportURL = (id: string) => `${RUNNER_BASE}/api/jobs/${encodeURIComponent(id)}/report`;

/** Start a browser download (GET through the console proxy; no CSRF needed). */
export const triggerDownload = (url: string) => {
  const a = document.createElement('a');
  a.href = url;
  a.rel = 'noopener';
  a.style.display = 'none';
  document.body.appendChild(a);
  a.click();
  a.remove();
};

export const formatBytes = (n?: number): string => {
  if (!n) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1);
  return `${(n / 1024 ** i).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
};

/** Domains: one per line, or separated by commas/whitespace. */
export const splitList = (s: string): string[] =>
  s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean);

/** Keywords: one per line (keywords may contain spaces or commas). */
export const splitLines = (s: string): string[] =>
  s
    .split(/\r?\n/)
    .map((x) => x.trim())
    .filter(Boolean);

export const errorMessage = (e: unknown): string =>
  (e as { message?: string })?.message || String(e);
