import React, { FC, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  K8sResourceCommon,
  ListPageHeader,
  useAccessReview,
  useK8sWatchResource,
} from '@openshift-console/dynamic-plugin-sdk';
import {
  ActionGroup,
  Alert,
  Breadcrumb,
  BreadcrumbItem,
  Button,
  Card,
  CardBody,
  CardTitle,
  Checkbox,
  Form,
  FormGroup,
  FormHelperText,
  HelperText,
  HelperTextItem,
  PageSection,
  Radio,
  Spinner,
  Stack,
  StackItem,
  TextArea,
  TextInput,
} from '@patternfly/react-core';
import CommunityDisclaimer from './CommunityDisclaimer';
import JobCard from './JobCard';
import { MUST_GATHER_DISK_LIMIT, MUST_GATHER_MAX_DISK_LIMIT, runAnywayLimit, useNodeDiskCheck } from './nodeDisk';
import {
  archiveURL,
  createJob,
  CSVKind,
  detectImage,
  errorMessage,
  formatBytes,
  getGatherTypes,
  GatherTypeId,
  I18N,
  isActive,
  JobList,
  JobRequest,
  listJobs,
  splitLines,
  splitList,
  triggerDownload,
} from './mustGather';
import './MustGatherPage.css';

type ClusterVersionKind = K8sResourceCommon & { spec?: { clusterID?: string } };
type DNSKind = K8sResourceCommon & { spec?: { baseDomain?: string } };

const AUTO_DOWNLOAD_KEY = 'oct-must-gather:auto-download';
const loadAutoDownload = (): string[] => {
  try {
    return JSON.parse(sessionStorage.getItem(AUTO_DOWNLOAD_KEY) || '[]');
  } catch {
    return [];
  }
};
const saveAutoDownload = (ids: string[]) => {
  try {
    sessionStorage.setItem(AUTO_DOWNLOAD_KEY, JSON.stringify(ids));
  } catch {
    // storage unavailable: auto-download only for this page view
  }
};

const MustGatherPage: FC = () => {
  const { t } = useTranslation(I18N);
  const gatherTypes = useMemo(() => getGatherTypes(t), [t]);

  // --- cluster data (read with the user's own credentials) ---
  const [cv] = useK8sWatchResource<ClusterVersionKind>({
    groupVersionKind: { group: 'config.openshift.io', version: 'v1', kind: 'ClusterVersion' },
    name: 'version',
    namespaced: false,
  });
  const [dns, dnsLoaded] = useK8sWatchResource<DNSKind>({
    groupVersionKind: { group: 'config.openshift.io', version: 'v1', kind: 'DNS' },
    name: 'cluster',
    namespaced: false,
  });
  // OLM copies AllNamespaces CSVs into every namespace; skip the copies.
  const [csvs, csvsLoaded, csvsError] = useK8sWatchResource<CSVKind[]>({
    groupVersionKind: { group: 'operators.coreos.com', version: 'v1alpha1', kind: 'ClusterServiceVersion' },
    isList: true,
    selector: { matchExpressions: [{ key: 'olm.copiedFrom', operator: 'DoesNotExist' }] },
  });

  const [canCreateNamespaces, nsCheckLoading] = useAccessReview({ resource: 'namespaces', verb: 'create' });
  const [canBindRoles, crbCheckLoading] = useAccessReview({
    group: 'rbac.authorization.k8s.io',
    resource: 'clusterrolebindings',
    verb: 'create',
  });
  const permissionsLoading = nsCheckLoading || crbCheckLoading;

  // must-gather stops collecting on a node whose disk is more than 70% used.
  // Between 70% and 90% the user can "Run anyway" with a raised limit (max 90%).
  const disk = useNodeDiskCheck();
  const maxUsed = Math.max(0, ...disk.full.map((n) => n.usedPercent));
  const raisedLimit = runAnywayLimit(maxUsed);
  const diskBlocked = disk.full.length > 0 && raisedLimit === undefined;
  const diskOK = disk.loaded && !diskBlocked;
  const hasPermissions = canCreateNamespaces && canBindRoles;

  // --- form state ---
  const [type, setType] = useState<GatherTypeId>('full');
  const [imageOverride, setImageOverride] = useState<Partial<Record<GatherTypeId, string>>>({});
  const [auditLogs, setAuditLogs] = useState(false);
  const [obfuscate, setObfuscate] = useState(false);
  const [obfIP, setObfIP] = useState(true);
  const [obfMAC, setObfMAC] = useState(true);
  const [omitSecrets, setOmitSecrets] = useState(true);
  const [domainsText, setDomainsText] = useState<string | undefined>(undefined);
  const [keywordsText, setKeywordsText] = useState('');
  const [caseNumber, setCaseNumber] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string>();

  const baseDomain = dnsLoaded ? dns?.spec?.baseDomain || '' : '';
  const domainsValue = domainsText ?? baseDomain;

  const detected = useMemo(() => {
    const out: Partial<Record<GatherTypeId, ReturnType<typeof detectImage>>> = {};
    if (csvsLoaded && !csvsError) {
      gatherTypes.forEach((g) => {
        if (g.operator) out[g.id] = detectImage(csvs || [], g.operator.csvPrefixes);
      });
    }
    return out;
  }, [csvs, csvsLoaded, csvsError, gatherTypes]);

  const selected = gatherTypes.find((g) => g.id === type) || gatherTypes[0];
  const image = selected.operator ? (imageOverride[type] ?? detected[type]?.image ?? '').trim() : '';
  const caseValid = caseNumber === '' || /^[0-9]{8}$/.test(caseNumber);
  const domains = splitList(domainsValue);
  const keywords = splitLines(keywordsText);
  const obfuscationEmpty = obfuscate && !obfIP && !obfMAC && !omitSecrets && !domains.length && !keywords.length;

  // --- jobs ---
  const [jobList, setJobList] = useState<JobList>();
  const [runnerError, setRunnerError] = useState<string>();
  const autoDownload = useRef<string[]>(loadAutoDownload());

  const refresh = useCallback(async () => {
    try {
      const list = await listJobs();
      setJobList(list);
      setRunnerError(undefined);
      // Download automatically once a job started from this browser session succeeds.
      list.jobs.forEach((j) => {
        if (j.phase === 'Succeeded' && autoDownload.current.includes(j.id)) {
          autoDownload.current = autoDownload.current.filter((id) => id !== j.id);
          saveAutoDownload(autoDownload.current);
          triggerDownload(archiveURL(j.id));
        }
      });
      const known = new Set(list.jobs.map((j) => j.id));
      const pending = autoDownload.current.filter(
        (id) => known.has(id) && isActive(list.jobs.find((j) => j.id === id)!.phase),
      );
      if (pending.length !== autoDownload.current.length) {
        autoDownload.current = pending;
        saveAutoDownload(pending);
      }
    } catch (e) {
      setRunnerError(errorMessage(e));
    }
  }, []);

  const anyActive = !!jobList?.jobs.some((j) => isActive(j.phase));
  const busy = !!jobList?.busy;

  useEffect(() => {
    refresh();
    const timer = window.setInterval(refresh, anyActive || busy ? 3000 : 15000);
    return () => window.clearInterval(timer);
  }, [refresh, anyActive, busy]);

  const canSubmit =
    !submitting &&
    !busy &&
    hasPermissions &&
    caseValid &&
    diskOK &&
    !obfuscationEmpty &&
    (!selected.operator || image !== '');

  const onSubmit = async () => {
    setSubmitError(undefined);
    setSubmitting(true);
    const req: JobRequest = {
      type,
      auditLogs,
      ...(selected.operator ? { image } : {}),
      ...(caseNumber ? { caseNumber } : {}),
      ...(raisedLimit !== undefined ? { volumePercentage: raisedLimit } : {}),
      ...(obfuscate
        ? { obfuscate: { ip: obfIP, mac: obfMAC, domains, keywords, omitSecrets } }
        : {}),
    };
    try {
      const job = await createJob(req);
      autoDownload.current = [...autoDownload.current, job.id];
      saveAutoDownload(autoDownload.current);
      await refresh();
    } catch (e) {
      setSubmitError(errorMessage(e));
    } finally {
      setSubmitting(false);
    }
  };

  const clusterID = cv?.spec?.clusterID || '<cluster_id>';
  const fileNamePreview = `${caseNumber && caseValid ? `${caseNumber}-` : ''}must-gather-<MM-DD-YYYY-HH-MM-SS>-${clusterID}.tar.gz`;

  const operatorHelp = () => {
    if (!selected.operator) return null;
    if (!csvsLoaded && !csvsError) {
      return (
        <HelperTextItem>
          <Spinner size="sm" aria-label={t('Detecting installed operators')} /> {t('Detecting installed operators')}
        </HelperTextItem>
      );
    }
    const d = detected[type];
    if (d && imageOverride[type] === undefined) {
      return (
        <HelperTextItem variant="success">
          {t('Detected from {{csv}} ({{name}}).', { csv: d.csv, name: d.displayName })}
        </HelperTextItem>
      );
    }
    if (d) {
      return (
        <HelperTextItem>
          {t('Custom image. Detected image: {{image}}', { image: d.image })}{' '}
          <Button variant="link" isInline onClick={() => setImageOverride({ ...imageOverride, [type]: undefined })}>
            {t('Use detected image')}
          </Button>
        </HelperTextItem>
      );
    }
    return (
      <HelperTextItem variant="warning">
        {t(
          'No installed {{product}} operator advertises a must-gather image. Install the operator, or enter the must-gather image that matches its version (see the product documentation).',
          { product: selected.operator.product },
        )}
      </HelperTextItem>
    );
  };

  return (
    <>
      <PageSection type="breadcrumb">
        <Breadcrumb>
          <BreadcrumbItem
            component="a"
            onClick={(e) => {
              e.preventDefault();
              window.location.href = '/community-tools/management';
            }}
          >
            {t('Management')}
          </BreadcrumbItem>
          <BreadcrumbItem isActive>{t('Must-Gather')}</BreadcrumbItem>
        </Breadcrumb>
      </PageSection>
      <ListPageHeader title={t('Must-Gather')} />
      <PageSection>
        <Stack hasGutter>
          <StackItem>
            <CommunityDisclaimer />
          </StackItem>

          {!permissionsLoading && !hasPermissions && (
            <StackItem>
              <Alert className="mg-card" variant="warning" isInline title={t('Cluster administrator access required')}>
                {t(
                  'must-gather creates a temporary namespace and a cluster role binding, and runs as you. Your account cannot create namespaces or cluster role bindings.',
                )}
              </Alert>
            </StackItem>
          )}

          {disk.full.length > 0 && (
            <StackItem>
              <Alert
                className="mg-card"
                variant={diskBlocked ? 'danger' : 'warning'}
                isInline
                title={
                  diskBlocked
                    ? t('Not enough free disk space to run must-gather')
                    : t('Node disk usage is above {{limit}}%', { limit: MUST_GATHER_DISK_LIMIT })
                }
                actionLinks={
                  <Button variant="link" isInline isLoading={disk.checking} onClick={disk.recheck}>
                    {t('Check again')}
                  </Button>
                }
              >
                <p>
                  {t(
                    'must-gather stops collecting when the disk of the node running the gather pod is more than {{limit}}% used. The gather pod can run on any of these nodes:',
                    { limit: MUST_GATHER_DISK_LIMIT },
                  )}
                </p>
                <ul className="mg-disk-list">
                  {disk.full.map((n) => (
                    <li key={n.name}>
                      {t('{{node}}: {{percent}}% used ({{free}} free of {{total}})', {
                        node: n.name,
                        percent: n.usedPercent,
                        free: formatBytes(n.availableBytes),
                        total: formatBytes(n.capacityBytes),
                      })}
                    </li>
                  ))}
                </ul>
                <p>
                  {diskBlocked
                    ? t(
                        'The disk limit cannot be raised above {{max}}%, so the gather would stop immediately. Free disk space on these nodes, then check again.',
                        { max: MUST_GATHER_MAX_DISK_LIMIT },
                      )
                    : t(
                        'Free disk space on these nodes, or use Run anyway: it raises the must-gather disk limit to {{raised}}% (at most {{max}}%). The gather then uses more of the node disk, and stops if usage passes {{raised}}%.',
                        { raised: raisedLimit, max: MUST_GATHER_MAX_DISK_LIMIT },
                      )}
                </p>
              </Alert>
            </StackItem>
          )}

          {disk.loaded && disk.error && disk.full.length === 0 && (
            <StackItem>
              <Alert className="mg-card" variant="warning" isInline isPlain title={t('Could not check node disk usage')}>
                {t(
                  'If a control plane node disk is more than {{limit}}% used, must-gather stops collecting early. Details: {{error}}',
                  { limit: MUST_GATHER_DISK_LIMIT, error: disk.error },
                )}
              </Alert>
            </StackItem>
          )}

          {runnerError && (
            <StackItem>
              <Alert className="mg-card" variant="danger" isInline title={t('The must-gather runner is not reachable')}>
                {runnerError}
              </Alert>
            </StackItem>
          )}

          <StackItem>
            <Card className="mg-card">
              <CardTitle>{t('Collect must-gather data')}</CardTitle>
              <CardBody>
                <Form
                  onSubmit={(e) => {
                    e.preventDefault();
                    if (canSubmit) onSubmit();
                  }}
                >
                  <FormGroup role="radiogroup" fieldId="mg-type" label={t('Must-gather type')} isStack isRequired>
                    {gatherTypes.map((g) => (
                      <Radio
                        key={g.id}
                        id={`mg-type-${g.id}`}
                        name="mg-type"
                        label={g.label}
                        description={g.description}
                        isChecked={type === g.id}
                        onChange={() => setType(g.id)}
                      />
                    ))}
                  </FormGroup>

                  {selected.operator && (
                    <FormGroup label={t('Must-gather image')} fieldId="mg-image" isRequired>
                      <TextInput
                        id="mg-image"
                        value={image}
                        onChange={(_e, v) => setImageOverride({ ...imageOverride, [type]: v })}
                        placeholder="registry.example.com/org/must-gather:tag"
                        validated={image ? 'default' : 'warning'}
                      />
                      <FormHelperText>
                        <HelperText>{operatorHelp()}</HelperText>
                      </FormHelperText>
                    </FormGroup>
                  )}

                  <FormGroup fieldId="mg-options" label={t('Options')} isStack>
                    <Checkbox
                      id="mg-audit"
                      label={t('Collect audit logs')}
                      description={t(
                        'Adds a second pass with the default image running gather_audit_logs (API server, OAuth and node audit logs). Audit logs can be large.',
                      )}
                      isChecked={auditLogs}
                      onChange={(_e, v) => setAuditLogs(v)}
                    />
                    <Checkbox
                      id="mg-obfuscate"
                      label={t('Obfuscate sensitive data with must-gather-clean')}
                      description={t(
                        'Replaces IP addresses, MAC addresses, domain names and keywords consistently before archiving. The un-obfuscated data is deleted.',
                      )}
                      isChecked={obfuscate}
                      onChange={(_e, v) => setObfuscate(v)}
                      body={
                        obfuscate && (
                          <Stack hasGutter className="mg-nested">
                            <StackItem>
                              <Checkbox
                                id="mg-obf-ip"
                                label={t('IP addresses (IPv4 and IPv6)')}
                                isChecked={obfIP}
                                onChange={(_e, v) => setObfIP(v)}
                              />
                            </StackItem>
                            <StackItem>
                              <Checkbox
                                id="mg-obf-mac"
                                label={t('MAC addresses')}
                                isChecked={obfMAC}
                                onChange={(_e, v) => setObfMAC(v)}
                              />
                            </StackItem>
                            <StackItem>
                              <Checkbox
                                id="mg-obf-secrets"
                                label={t('Omit Secrets and certificate signing requests')}
                                isChecked={omitSecrets}
                                onChange={(_e, v) => setOmitSecrets(v)}
                              />
                            </StackItem>
                            <StackItem>
                              <FormGroup label={t('Domains')} fieldId="mg-obf-domains">
                                <TextArea
                                  id="mg-obf-domains"
                                  value={domainsValue}
                                  onChange={(_e, v) => setDomainsText(v)}
                                  rows={2}
                                  resizeOrientation="vertical"
                                />
                                <FormHelperText>
                                  <HelperText>
                                    <HelperTextItem>
                                      {t(
                                        'One per line. Subdomains are included. Prefilled with the cluster base domain.',
                                      )}
                                    </HelperTextItem>
                                  </HelperText>
                                </FormHelperText>
                              </FormGroup>
                            </StackItem>
                            <StackItem>
                              <FormGroup label={t('Keywords')} fieldId="mg-obf-keywords">
                                <TextArea
                                  id="mg-obf-keywords"
                                  value={keywordsText}
                                  onChange={(_e, v) => setKeywordsText(v)}
                                  rows={2}
                                  resizeOrientation="vertical"
                                  placeholder={t('For example a company or project name')}
                                />
                                <FormHelperText>
                                  <HelperText>
                                    <HelperTextItem>
                                      {t('One per line, at least 3 characters. Each is replaced with keyword-0001, keyword-0002, ...')}
                                    </HelperTextItem>
                                  </HelperText>
                                </FormHelperText>
                              </FormGroup>
                            </StackItem>
                            {obfuscationEmpty && (
                              <StackItem>
                                <HelperText>
                                  <HelperTextItem variant="error">
                                    {t('Select at least one thing to obfuscate.')}
                                  </HelperTextItem>
                                </HelperText>
                              </StackItem>
                            )}
                          </Stack>
                        )
                      }
                    />
                  </FormGroup>

                  <FormGroup label={t('Support case number')} fieldId="mg-case">
                    <TextInput
                      id="mg-case"
                      className="mg-case-input"
                      value={caseNumber}
                      onChange={(_e, v) => setCaseNumber(v.trim())}
                      inputMode="numeric"
                      placeholder="01234567"
                      validated={caseValid ? 'default' : 'error'}
                    />
                    <FormHelperText>
                      <HelperText>
                        <HelperTextItem variant={caseValid ? 'default' : 'error'}>
                          {caseValid
                            ? t('Optional. Prefixes the file name so Red Hat SFTP uploads attach to the case.')
                            : t('A support case number has 8 digits.')}
                        </HelperTextItem>
                      </HelperText>
                    </FormHelperText>
                  </FormGroup>

                  <FormGroup label={t('Archive file name')} fieldId="mg-filename">
                    <code id="mg-filename" className="mg-filename">
                      {fileNamePreview}
                    </code>
                    <FormHelperText>
                      <HelperText>
                        <HelperTextItem>
                          {t('Red Hat recommended naming: date (UTC) and cluster ID. The archive downloads automatically when ready.')}
                        </HelperTextItem>
                      </HelperText>
                    </FormHelperText>
                  </FormGroup>

                  {submitError && (
                    <Alert variant="danger" isInline title={t('Could not start must-gather')}>
                      {submitError}
                    </Alert>
                  )}
                  {busy && (
                    <Alert variant="info" isInline title={t('A must-gather is already running on this cluster')}>
                      {t('Only one must-gather runs at a time. Wait for it to finish, or cancel it.')}
                    </Alert>
                  )}

                  <ActionGroup>
                    <Button
                      variant={raisedLimit !== undefined ? 'warning' : 'primary'}
                      type="submit"
                      isDisabled={!canSubmit}
                      isLoading={submitting || (!disk.loaded && !runnerError)}
                    >
                      {!disk.loaded
                        ? t('Checking node disk space')
                        : raisedLimit !== undefined
                          ? t('Run anyway (disk limit {{raised}}%)', { raised: raisedLimit })
                          : t('Run must-gather')}
                    </Button>
                  </ActionGroup>
                </Form>
              </CardBody>
            </Card>
          </StackItem>

          {jobList && jobList.jobs.length > 0 && (
            <StackItem>
              <Card className="mg-card">
                <CardTitle>{t('Collections')}</CardTitle>
                <CardBody>
                  <Stack hasGutter>
                    <StackItem>
                      <HelperText>
                        <HelperTextItem>
                          {t('Archives are kept on the cluster for {{ttl}} after they finish, then deleted.', {
                            ttl: jobList.retainedFor,
                          })}
                        </HelperTextItem>
                      </HelperText>
                    </StackItem>
                    {jobList.jobs.map((j, i) => (
                      <StackItem key={j.id}>
                        <JobCard job={j} gatherTypes={gatherTypes} defaultExpanded={i === 0} onChange={refresh} />
                      </StackItem>
                    ))}
                  </Stack>
                </CardBody>
              </Card>
            </StackItem>
          )}
        </Stack>
      </PageSection>
    </>
  );
};

export default MustGatherPage;
