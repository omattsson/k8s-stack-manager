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
}

interface AccessEntry {
  chartName: string;
  serviceName: string;
  serviceType: 'Ingress' | 'LoadBalancer' | 'NodePort' | 'ClusterIP';
  url?: string;
  copyText: string;
  label: string;
}

const buildEntries = (status: NamespaceStatus): AccessEntry[] => {
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
    });
  });

  // Service entries per chart
  (status.charts || []).forEach((chart: ChartStatus) => {
    (chart.services || []).forEach((svc: ServiceInfo) => {
      // Skip services already covered by ingress
      const hasIngress = (svc.ingress_hosts || []).length > 0;
      if (hasIngress) return;

      if (svc.type === 'LoadBalancer' && svc.external_ip) {
        const port = (svc.ports || [])[0]?.replace(/\/.*/, '') || '';
        const url = `http://${svc.external_ip}${port ? `:${port}` : ''}`;
        entries.push({
          chartName: chart.chart_name || chart.release_name,
          serviceName: svc.name,
          serviceType: 'LoadBalancer',
          url,
          copyText: url,
          label: url,
        });
      } else if (svc.type === 'NodePort' && (svc.node_ports || []).length > 0) {
        const portsStr = (svc.node_ports || []).join(', ');
        entries.push({
          chartName: chart.chart_name || chart.release_name,
          serviceName: svc.name,
          serviceType: 'NodePort',
          copyText: `NodePort: ${portsStr}`,
          label: `NodePort: ${portsStr}`,
        });
      } else if (svc.type === 'ClusterIP') {
        const port = (svc.ports || [])[0]?.replace(/\/.*/, '') || '80';
        const cmd = `kubectl port-forward svc/${svc.name} ${port}:${port} -n ${status.namespace}`;
        entries.push({
          chartName: chart.chart_name || chart.release_name,
          serviceName: svc.name,
          serviceType: 'ClusterIP',
          copyText: cmd,
          label: cmd,
        });
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

const AccessUrls = ({ status }: AccessUrlsProps) => {
  const { showSuccess, showError } = useNotification();

  const entries = buildEntries(status);

  if (entries.length === 0) return null;

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
                    <IconButton size="small" onClick={() => handleCopy(entry.copyText)} aria-label={`Copy ${entry.serviceName}`}>
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
                    <Typography variant="body2" fontWeight={500}>{entry.serviceName}</Typography>
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
    </Box>
  );
};

export default AccessUrls;
