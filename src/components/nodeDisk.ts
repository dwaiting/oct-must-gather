import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  consoleFetchJSON,
  K8sResourceCommon,
  useK8sWatchResource,
} from '@openshift-console/dynamic-plugin-sdk';
import { errorMessage } from './mustGather';

/**
 * must-gather's disk usage checker (oc adm must-gather, default
 * --volume-percentage 70) kills the gather script when the filesystem backing
 * the gather pod's emptyDir is more than this percent used.
 */
export const MUST_GATHER_DISK_LIMIT = 70;

/** Highest disk limit "Run anyway" may set (enforced by the runner too). */
export const MUST_GATHER_MAX_DISK_LIMIT = 90;

/** Room given to the gather above the fullest node when running anyway. */
const RUN_ANYWAY_HEADROOM = 10;

/**
 * The --volume-percentage for "Run anyway": the fullest candidate node plus
 * headroom, never above MUST_GATHER_MAX_DISK_LIMIT. Undefined when no node is
 * over the default limit, or when the fullest node is already at the cap (the
 * gather would stop immediately).
 */
export const runAnywayLimit = (maxUsedPercent: number): number | undefined => {
  if (maxUsedPercent <= MUST_GATHER_DISK_LIMIT || maxUsedPercent >= MUST_GATHER_MAX_DISK_LIMIT) {
    return undefined;
  }
  return Math.min(MUST_GATHER_MAX_DISK_LIMIT, maxUsedPercent + RUN_ANYWAY_HEADROOM);
};

const CONTROL_PLANE_LABEL = 'node-role.kubernetes.io/control-plane';
const UNREACHABLE_TAINTS = ['node.kubernetes.io/unreachable', 'node.kubernetes.io/not-ready'];
const RECHECK_MS = 60_000;

type NodeKind = K8sResourceCommon & {
  spec?: { unschedulable?: boolean; taints?: { key: string; effect: string }[] };
  status?: { conditions?: { type: string; status: string }[] };
};

type StatsSummary = {
  node?: { fs?: { availableBytes?: number; capacityBytes?: number; usedBytes?: number } };
};

export type NodeDisk = {
  name: string;
  usedPercent: number;
  availableBytes: number;
  capacityBytes: number;
};

export type DiskCheck = {
  /** First check finished (successfully or not). */
  loaded: boolean;
  checking: boolean;
  nodes: NodeDisk[];
  /** Candidate nodes above MUST_GATHER_DISK_LIMIT. */
  full: NodeDisk[];
  /** Set when usage could not be read; the check then does not block. */
  error?: string;
  recheck: () => void;
};

const isReady = (n: NodeKind) =>
  !!n.status?.conditions?.some((c) => c.type === 'Ready' && c.status === 'True') &&
  !n.spec?.taints?.some((t) => UNREACHABLE_TAINTS.includes(t.key));

/**
 * Nodes the must-gather pod may be scheduled on, mirroring oc's
 * getCandidateNodeNames: Ready, schedulable control plane nodes (all control
 * plane nodes if none is), or any Ready schedulable node when the cluster has
 * no control plane nodes (hosted control planes).
 */
export const candidateNodes = (nodes: NodeKind[]): string[] => {
  const usable = nodes.filter((n) => isReady(n) && !n.spec?.unschedulable);
  const cp = nodes.filter((n) => n.metadata?.labels?.[CONTROL_PLANE_LABEL] !== undefined);
  let picked: NodeKind[];
  if (cp.length > 0) {
    const usableCP = usable.filter((n) => n.metadata?.labels?.[CONTROL_PLANE_LABEL] !== undefined);
    picked = usableCP.length > 0 ? usableCP : cp;
  } else {
    picked = usable.length > 0 ? usable : nodes;
  }
  return picked.map((n) => n.metadata?.name || '').filter(Boolean).sort();
};

/** Same rounding as `df -P` "Use%" (what the checker compares): used / (used + avail), rounded up. */
export const usedPercent = (usedBytes: number, availableBytes: number): number => {
  const total = usedBytes + availableBytes;
  return total > 0 ? Math.ceil((usedBytes * 100) / total) : 0;
};

const fetchNodeDisk = async (name: string): Promise<NodeDisk> => {
  // Kubelet stats for the node filesystem (where emptyDir volumes live).
  const summary: StatsSummary = await consoleFetchJSON(
    `/api/kubernetes/api/v1/nodes/${encodeURIComponent(name)}/proxy/stats/summary`,
  );
  const fs = summary.node?.fs;
  if (fs?.availableBytes === undefined || fs?.capacityBytes === undefined) {
    throw new Error(`no filesystem stats for node ${name}`);
  }
  const used = fs.usedBytes ?? fs.capacityBytes - fs.availableBytes;
  return {
    name,
    usedPercent: usedPercent(used, fs.availableBytes),
    availableBytes: fs.availableBytes,
    capacityBytes: fs.capacityBytes,
  };
};

export const useNodeDiskCheck = (): DiskCheck => {
  const [nodes, nodesLoaded, nodesError] = useK8sWatchResource<NodeKind[]>({
    groupVersionKind: { version: 'v1', kind: 'Node' },
    isList: true,
  });
  const candidateKey = useMemo(
    () => (nodesLoaded && !nodesError ? candidateNodes(nodes || []).join(',') : ''),
    [nodes, nodesLoaded, nodesError],
  );

  const [state, setState] = useState<{ loaded: boolean; checking: boolean; nodes: NodeDisk[]; error?: string }>({
    loaded: false,
    checking: false,
    nodes: [],
  });
  const [tick, setTick] = useState(0);
  const recheck = useCallback(() => setTick((n) => n + 1), []);

  useEffect(() => {
    if (nodesError) {
      setState({ loaded: true, checking: false, nodes: [], error: errorMessage(nodesError) });
      return undefined;
    }
    if (!candidateKey) {
      if (nodesLoaded) {
        setState({ loaded: true, checking: false, nodes: [], error: 'no candidate nodes found' });
      }
      return undefined;
    }
    let cancelled = false;
    setState((s) => ({ ...s, checking: true }));
    Promise.allSettled(candidateKey.split(',').map(fetchNodeDisk)).then((results) => {
      if (cancelled) return;
      const ok = results.flatMap((r) => (r.status === 'fulfilled' ? [r.value] : []));
      const failed = results.find((r): r is PromiseRejectedResult => r.status === 'rejected');
      setState({
        loaded: true,
        checking: false,
        nodes: ok,
        error: failed ? errorMessage(failed.reason) : undefined,
      });
    });
    const timer = window.setTimeout(recheck, RECHECK_MS);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [candidateKey, nodesLoaded, nodesError, tick, recheck]);

  const full = useMemo(() => state.nodes.filter((n) => n.usedPercent > MUST_GATHER_DISK_LIMIT), [state.nodes]);
  return { ...state, full, recheck };
};
