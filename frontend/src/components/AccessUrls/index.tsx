import { useId } from 'react';
import {
  Box,
  Typography,
  Paper,
  Chip,
  IconButton,
  Link,
  Tooltip,
  List,
  ListItem,
  ListItemText,
} from '@mui/material';
import { useNotification } from '../../context/NotificationContext';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import OpenInNewIcon from '@mui/icons-material/OpenInNew';
import LanguageIcon from '@mui/icons-material/Language';
import type { NamespaceStatus, ChartStatus, ServiceInfo, IngressInfo } from '../../types';

interface AccessUrlsProps {
  status: NamespaceStatus;
  /**
   * Show a `kubectl port-forward` command for each ClusterIP service. Only
   * useful to users with kubectl access to the cluster (devops, admin).
   */
  showPortForward?: boolean;
  /**
   * Show a `stackctl stack port-forward` command for each port of each
   * ClusterIP service. The caller sets it when the server has the feature on
   * (ui-config `port_forward_enabled`) and the user can modify the instance.
   * Needs `instanceName`.
   */
  portForwardEnabled?: boolean;
  /** Name of the instance, as stackctl takes it on the command line. */
  instanceName?: string;
  /**
   * Show `stackctl stack exec` commands. The caller sets it when the server has
   * the feature on (ui-config `exec_enabled`), the cluster allows exec and the
   * user can modify the instance. Needs `instanceName`. With `status.exec_hints`
   * the lines follow the containers the server accepts (`exec_containers`, with
   * `-c` unless the single container is the default); without it (older server)
   * there is one line per chart without `-c`.
   */
  execEnabled?: boolean;
  /**
   * Streams accept API keys (ui-config `stream_api_keys_allowed`). Only changes
   * the help text of the stackctl commands: when true it no longer says that
   * API keys are not accepted.
   */
  streamApiKeysAllowed?: boolean;
}

interface AccessEntry {
  chartName: string;
  serviceName: string;
  serviceType: 'Ingress' | 'LoadBalancer' | 'NodePort' | 'ClusterIP' | 'Exec';
  url?: string;
  copyText: string;
  label: string;
  /** Accessible name of the copy button. */
  copyLabel: string;
  /** The entry is a stackctl command (exec or port-forward). */
  stackctl?: boolean;
}

const buildEntries = (
  status: NamespaceStatus,
  showPortForward: boolean,
  stackctlInstance: string | undefined,
  execInstance?: string,
): AccessEntry[] => {
  const entries: AccessEntry[] = [];

  // Ingress entries
  (status.ingresses || []).forEach((ing: IngressInfo) => {
    entries.push({
      chartName: '',
      serviceName: ing.name,
      serviceType: 'Ingress',
      url: ing.url,
      copyText: ing.url,
      label: ing.url,
      copyLabel: `Copy ${ing.name}`,
    });
  });

  // Service entries per chart
  (status.charts || []).forEach((chart: ChartStatus) => {
    if (execInstance) {
      // The backend takes the Helm release name as the chart.
      const release = chart.release_name || chart.chart_name;
      const pushExec = (container?: string) => {
        const cmd = container
          ? `stackctl stack exec ${execInstance} ${release} -c ${container} -- <command>`
          : `stackctl stack exec ${execInstance} ${release} -- <command>`;
        entries.push({
          chartName: release,
          serviceName: container ? `${release} (${container})` : release,
          serviceType: 'Exec',
          copyText: cmd,
          label: cmd,
          copyLabel: container ? `Copy stackctl exec ${release} ${container}` : `Copy stackctl exec ${release}`,
          stackctl: true,
        });
      };
      if (status.exec_hints !== true) {
        // Fallback for older servers (and responses without hints): the server did
        // not say which containers exec accepts, so show one line per chart without -c.
        pushExec();
      } else {
        const containers = chart.exec_containers ?? [];
        if (containers.length === 1 && containers[0] === chart.exec_default_container) {
          // A call without -c is accepted and picks this container.
          pushExec();
        } else {
          // No accepted container: no line. Several, or one that is not the default: -c names it.
          containers.forEach((c) => pushExec(c));
        }
      }
    }
    (chart.services || []).forEach((svc: ServiceInfo) => {
      // Skip services already covered by ingress
      const hasIngress = (svc.ingress_hosts || []).length > 0;
      if (hasIngress) return;

      if (svc.type === 'LoadBalancer' && svc.external_ip) {
        const port = (svc.ports || [])[0]?.replace(/\/.*/, '') || '';
        const portSuffix = port ? `:${port}` : '';
        const url = `http://${svc.external_ip}${portSuffix}`;
        entries.push({
          chartName: chart.chart_name || chart.release_name,
          serviceName: svc.name,
          serviceType: 'LoadBalancer',
          url,
          copyText: url,
          label: url,
          copyLabel: `Copy ${svc.name}`,
        });
      } else if (svc.type === 'NodePort' && (svc.node_ports || []).length > 0) {
        const portsStr = (svc.node_ports || []).join(', ');
        entries.push({
          chartName: chart.chart_name || chart.release_name,
          serviceName: svc.name,
          serviceType: 'NodePort',
          copyText: `NodePort: ${portsStr}`,
          label: `NodePort: ${portsStr}`,
          copyLabel: `Copy ${svc.name}`,
        });
      } else if (svc.type === 'ClusterIP') {
        const chartName = chart.chart_name || chart.release_name;
        if (showPortForward) {
          const port = (svc.ports || [])[0]?.replace(/\/.*/, '') || '80';
          const cmd = `kubectl port-forward svc/${svc.name} ${port}:${port} -n ${status.namespace}`;
          entries.push({
            chartName,
            serviceName: svc.name,
            serviceType: 'ClusterIP',
            copyText: cmd,
            label: cmd,
            copyLabel: `Copy ${svc.name}`,
          });
        }
        if (stackctlInstance) {
          (svc.ports || []).forEach((p) => {
            // The backend resolves TCP service ports only; no protocol means TCP.
            const [port, proto] = p.split('/');
            if (!port || (proto && proto.toUpperCase() !== 'TCP')) return;
            const cmd = `stackctl stack port-forward ${stackctlInstance} ${svc.name} ${port}`;
            entries.push({
              chartName,
              serviceName: svc.name,
              serviceType: 'ClusterIP',
              copyText: cmd,
              label: cmd,
              copyLabel: `Copy stackctl port-forward ${svc.name} ${port}`,
              stackctl: true,
            });
          });
        }
      }
    });
  });

  return entries;
};

const typeColor = (type: string): 'primary' | 'success' | 'warning' | 'default' => {
  switch (type) {
    case 'Ingress': return 'primary';
    case 'LoadBalancer': return 'success';
    case 'NodePort': return 'warning';
    default: return 'default';
  }
};

const AccessUrls = ({ status, showPortForward = false, portForwardEnabled = false, instanceName, execEnabled = false, streamApiKeysAllowed = false }: AccessUrlsProps) => {
  const { showSuccess, showError } = useNotification();
  // Unique per component, so two AccessUrls on a page do not share the id.
  const helpId = useId();

  const entries = buildEntries(status, showPortForward, portForwardEnabled ? instanceName : undefined, execEnabled ? instanceName : undefined);

  if (entries.length === 0) return null;

  const hasStackctl = entries.some((e) => e.stackctl);
  const stackctlHelp = (
    <>
      stackctl commands need stackctl and a session: run{' '}
      <Box component="code" sx={{ fontFamily: 'monospace' }}>stackctl login</Box> first.
      {streamApiKeysAllowed ? '' : ' API keys are not accepted.'}
    </>
  );

  const handleCopy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      showSuccess('Copied to clipboard');
    } catch {
      showError('Failed to copy to clipboard');
    }
  };

  return (
    <Box sx={{ mb: 2 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
        <LanguageIcon fontSize="small" color="primary" />
        <Typography variant="subtitle2">Access URLs</Typography>
      </Box>
      <Paper variant="outlined">
        <List dense disablePadding>
          {entries.map((entry, idx) => (
            <ListItem
              key={`${entry.serviceName}-${idx}`}
              divider={idx < entries.length - 1}
              secondaryAction={
                <Box sx={{ display: 'flex', gap: 0.5 }}>
                  <Tooltip title="Copy">
                    <IconButton size="small" onClick={() => handleCopy(entry.copyText)} aria-label={entry.copyLabel} aria-describedby={entry.stackctl ? helpId : undefined}>
                      <ContentCopyIcon fontSize="small" />
                    </IconButton>
                  </Tooltip>
                  {entry.url && (
                    <Tooltip title="Open in new tab">
                      <IconButton
                        size="small"
                        component="a"
                        href={entry.url}
                        target="_blank"
                        rel="noopener noreferrer"
                        aria-label={`Open ${entry.serviceName}`}
                      >
                        <OpenInNewIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                  )}
                </Box>
              }
            >
              <ListItemText
                primary={
                  <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                    <Chip label={entry.serviceType} size="small" color={typeColor(entry.serviceType)} variant="outlined" />
                    <Typography variant="body2" sx={{ fontWeight: 500 }}>{entry.serviceName}</Typography>
                  </Box>
                }
                secondary={
                  entry.url ? (
                    <Link href={entry.url} target="_blank" rel="noopener noreferrer" variant="body2" sx={{ fontFamily: 'monospace', fontSize: '0.8rem' }}>
                      {entry.label}
                    </Link>
                  ) : (
                    <Typography variant="body2" sx={{ fontFamily: 'monospace', fontSize: '0.8rem' }} color="text.secondary">
                      {entry.label}
                    </Typography>
                  )
                }
              />
            </ListItem>
          ))}
        </List>
      </Paper>
      {hasStackctl && (
        <Typography id={helpId} variant="caption" color="text.secondary" sx={{ display: 'block', mt: 0.5 }}>
          {stackctlHelp}
        </Typography>
      )}
    </Box>
  );
};

export default AccessUrls;
