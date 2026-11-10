import React, { FC, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Alert,
  Button,
  Card,
  CardBody,
  CardHeader,
  CardTitle,
  CodeBlock,
  CodeBlockCode,
  ExpandableSection,
  Flex,
  FlexItem,
  Label,
  Popover,
  Spinner,
  Stack,
  StackItem,
} from '@patternfly/react-core';
import { DownloadIcon } from '@patternfly/react-icons';
import {
  archiveURL,
  cancelJob,
  deleteJob,
  errorMessage,
  formatBytes,
  GatherTypeDef,
  I18N,
  isActive,
  JobPhase,
  JobStatus,
  reportURL,
} from './mustGather';

type Props = {
  job: JobStatus;
  gatherTypes: GatherTypeDef[];
  defaultExpanded: boolean;
  onChange: () => void;
};

const phaseColor = (p: JobPhase): 'blue' | 'green' | 'red' | 'grey' =>
  p === 'Succeeded' ? 'green' : p === 'Failed' ? 'red' : p === 'Canceled' ? 'grey' : 'blue';

const JobCard: FC<Props> = ({ job, gatherTypes, defaultExpanded, onChange }) => {
  const { t } = useTranslation(I18N);
  const [logOpen, setLogOpen] = useState(defaultExpanded && isActive(job.phase));
  const [actionError, setActionError] = useState<string>();
  const [working, setWorking] = useState(false);
  const logRef = useRef<HTMLDivElement>(null);
  const active = isActive(job.phase);

  const phaseLabel: Record<JobPhase, string> = {
    Pending: t('Pending'),
    Gathering: t('Gathering'),
    Obfuscating: t('Obfuscating'),
    Archiving: t('Archiving'),
    Succeeded: t('Succeeded'),
    Failed: t('Failed'),
    Canceled: t('Canceled'),
  };

  useEffect(() => {
    // Follow the log while the job runs.
    if (logOpen && active && logRef.current) {
      logRef.current.scrollTop = logRef.current.scrollHeight;
    }
  }, [job.log.length, logOpen, active]);

  const act = async (fn: () => Promise<unknown>) => {
    setWorking(true);
    setActionError(undefined);
    try {
      await fn();
      onChange();
    } catch (e) {
      setActionError(errorMessage(e));
    } finally {
      setWorking(false);
    }
  };

  const typeLabel = gatherTypes.find((g) => g.id === job.type)?.label || job.type;
  const options = [
    job.auditLogs && t('audit logs'),
    job.obfuscated && t('obfuscated'),
    job.caseNumber && t('case {{caseNumber}}', { caseNumber: job.caseNumber }),
  ].filter(Boolean);

  return (
    <Card isCompact isPlain className="mg-job">
      <CardHeader>
        <CardTitle>
          <Flex spaceItems={{ default: 'spaceItemsSm' }} alignItems={{ default: 'alignItemsCenter' }}>
            <FlexItem>{typeLabel}</FlexItem>
            <FlexItem>
              <Label color={phaseColor(job.phase)} icon={active ? <Spinner size="sm" aria-hidden /> : undefined}>
                {phaseLabel[job.phase]}
              </Label>
            </FlexItem>
            {options.length > 0 && <FlexItem className="mg-muted">{options.join(' · ')}</FlexItem>}
          </Flex>
        </CardTitle>
      </CardHeader>
      <CardBody>
        <Stack hasGutter>
          <StackItem className="mg-muted">
            {t('Started {{time}}', { time: new Date(job.created).toLocaleString() })}
            {job.finished && ` · ${t('Finished {{time}}', { time: new Date(job.finished).toLocaleString() })}`}
            {job.image && ` · ${job.image}`}
          </StackItem>
          {job.message && <StackItem>{job.message}</StackItem>}
          {job.warning && (
            <StackItem>
              <Alert variant="warning" isInline isPlain title={job.warning} />
            </StackItem>
          )}
          {actionError && (
            <StackItem>
              <Alert variant="danger" isInline isPlain title={actionError} />
            </StackItem>
          )}

          <StackItem>
            <Flex spaceItems={{ default: 'spaceItemsSm' }}>
              {job.phase === 'Succeeded' && job.archiveName && (
                <FlexItem>
                  <Button variant="primary" component="a" href={archiveURL(job.id)} icon={<DownloadIcon />}>
                    {t('Download {{name}} ({{size}})', { name: job.archiveName, size: formatBytes(job.archiveSize) })}
                  </Button>
                </FlexItem>
              )}
              {job.hasReport && (
                <FlexItem>
                  <Popover
                    headerContent={t('Obfuscation report')}
                    bodyContent={t(
                      'The report maps every original value to its replacement. Keep it to translate answers from support, but never attach it to a support case.',
                    )}
                    footerContent={
                      <Button variant="secondary" component="a" href={reportURL(job.id)} icon={<DownloadIcon />}>
                        {t('Download report')}
                      </Button>
                    }
                  >
                    <Button variant="secondary">{t('Obfuscation report')}</Button>
                  </Popover>
                </FlexItem>
              )}
              {active && (
                <FlexItem>
                  <Button variant="secondary" isDanger isDisabled={working} onClick={() => act(() => cancelJob(job.id))}>
                    {t('Cancel')}
                  </Button>
                </FlexItem>
              )}
              {!active && (
                <FlexItem>
                  <Button variant="link" isDanger isDisabled={working} onClick={() => act(() => deleteJob(job.id))}>
                    {t('Delete')}
                  </Button>
                </FlexItem>
              )}
            </Flex>
          </StackItem>

          <StackItem>
            <ExpandableSection
              toggleText={logOpen ? t('Hide log') : t('Show log')}
              isExpanded={logOpen}
              onToggle={(_e, v) => setLogOpen(v)}
            >
              <div ref={logRef} className="mg-log">
                <CodeBlock>
                  <CodeBlockCode>{job.log.length ? job.log.join('\n') : t('No output yet.')}</CodeBlockCode>
                </CodeBlock>
              </div>
            </ExpandableSection>
          </StackItem>
        </Stack>
      </CardBody>
    </Card>
  );
};

export default JobCard;
