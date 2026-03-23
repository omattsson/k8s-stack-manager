import { useState, useEffect, useCallback, useRef } from 'react';
import { useSearchParams } from 'react-router-dom';
import {
  Box,
  Typography,
  Button,
  Alert,
  Card,
  CardContent,
  Tabs,
  Tab,
  Autocomplete,
  TextField,
  Chip,
} from '@mui/material';
import CompareArrowsIcon from '@mui/icons-material/CompareArrows';
import CheckCircleOutlineIcon from '@mui/icons-material/CheckCircleOutline';
import LoadingState from '../../components/LoadingState';
import { instanceService } from '../../api/client';
import { useNotification } from '../../context/NotificationContext';
import type {
  StackInstance,
  CompareInstancesResponse,
  CompareChartDiff,
} from '../../types';
import ReactDiffViewer from 'react-diff-viewer-continued';

const Compare = () => {
  const [searchParams, setSearchParams] = useSearchParams();
  const { showError } = useNotification();

  const [instances, setInstances] = useState<StackInstance[]>([]);
  const [loadingInstances, setLoadingInstances] = useState(true);
  const [leftId, setLeftId] = useState<string | null>(searchParams.get('left'));
  const [rightId, setRightId] = useState<string | null>(searchParams.get('right'));
  const [result, setResult] = useState<CompareInstancesResponse | null>(null);
  const [comparing, setComparing] = useState(false);
  const [compareError, setCompareError] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState(0);

  // Fetch instance list for selectors
  useEffect(() => {
    const fetchInstances = async () => {
      try {
        const data = await instanceService.list();
        setInstances(data || []);
      } catch {
        showError('Failed to load instance list');
      } finally {
        setLoadingInstances(false);
      }
    };
    fetchInstances();
  }, [showError]);

  const runComparison = useCallback(async (left: string, right: string) => {
    setComparing(true);
    setCompareError(null);
    setResult(null);
    setActiveTab(0);
    try {
      const data = await instanceService.compareInstances(left, right);
      setResult(data);
    } catch {
      setCompareError('Failed to compare instances. Please try again.');
    } finally {
      setComparing(false);
    }
  }, []);

  // Auto-trigger comparison if URL has both params and instances are loaded
  const hasAutoTriggered = useRef(false);
  useEffect(() => {
    if (!loadingInstances && leftId && rightId && !hasAutoTriggered.current) {
      hasAutoTriggered.current = true;
      runComparison(leftId, rightId);
    }
  }, [loadingInstances, leftId, rightId, runComparison]);

  const handleCompare = () => {
    if (!leftId || !rightId) return;
    setSearchParams({ left: leftId, right: rightId });
    runComparison(leftId, rightId);
  };

  const getTabLabel = (chart: CompareChartDiff): string => {
    if (!chart.left_values && chart.right_values) return `${chart.chart_name} (right only)`;
    if (chart.left_values && !chart.right_values) return `${chart.chart_name} (left only)`;
    return chart.chart_name;
  };

  const leftInstance = instances.find((i) => i.id === leftId) ?? null;
  const rightInstance = instances.find((i) => i.id === rightId) ?? null;

  if (loadingInstances) {
    return <LoadingState label="Loading instances..." />;
  }

  return (
    <Box>
      <Typography variant="h4" component="h1" sx={{ mb: 3 }}>
        Compare Stack Instances
      </Typography>

      {/* Instance selectors */}
      <Box sx={{ display: 'flex', gap: 2, mb: 3, alignItems: 'flex-start', flexWrap: 'wrap' }}>
        <Autocomplete
          options={instances}
          getOptionLabel={(option) => option.name}
          value={leftInstance}
          onChange={(_e, value) => setLeftId(value?.id ?? null)}
          renderInput={(params) => (
            <TextField {...params} label="Left instance" size="small" />
          )}
          renderOption={(props, option) => (
            <Box component="li" {...props} key={option.id}>
              <Box>
                <Typography variant="body2">{option.name}</Typography>
                <Typography variant="caption" color="text.secondary">
                  {option.branch} &middot; {option.namespace}
                </Typography>
              </Box>
            </Box>
          )}
          isOptionEqualToValue={(option, value) => option.id === value.id}
          sx={{ minWidth: 280, flex: 1 }}
        />
        <Autocomplete
          options={instances}
          getOptionLabel={(option) => option.name}
          value={rightInstance}
          onChange={(_e, value) => setRightId(value?.id ?? null)}
          renderInput={(params) => (
            <TextField {...params} label="Right instance" size="small" />
          )}
          renderOption={(props, option) => (
            <Box component="li" {...props} key={option.id}>
              <Box>
                <Typography variant="body2">{option.name}</Typography>
                <Typography variant="caption" color="text.secondary">
                  {option.branch} &middot; {option.namespace}
                </Typography>
              </Box>
            </Box>
          )}
          isOptionEqualToValue={(option, value) => option.id === value.id}
          sx={{ minWidth: 280, flex: 1 }}
        />
        <Button
          variant="contained"
          startIcon={<CompareArrowsIcon />}
          onClick={handleCompare}
          disabled={!leftId || !rightId || leftId === rightId || comparing}
        >
          Compare
        </Button>
      </Box>

      {leftId && rightId && leftId === rightId && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          Please select two different instances to compare.
        </Alert>
      )}

      {/* Loading state */}
      {comparing && <LoadingState label="Comparing instances..." />}

      {/* Error state */}
      {compareError && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {compareError}
        </Alert>
      )}

      {/* Empty state */}
      {!comparing && !compareError && !result && (
        <Alert severity="info" sx={{ mb: 2 }}>
          Select two instances above and click Compare to see their differences.
        </Alert>
      )}

      {/* Results */}
      {result && (
        <Box>
          {/* Summary cards */}
          <Box sx={{ display: 'flex', gap: 2, mb: 3, flexWrap: 'wrap' }}>
            <Card variant="outlined" sx={{ flex: 1, minWidth: 250 }}>
              <CardContent>
                <Typography variant="overline" color="text.secondary">
                  Left Instance
                </Typography>
                <Typography variant="h6">{result.left.name}</Typography>
                <Typography variant="body2" color="text.secondary">
                  Definition: {result.left.definition_name}
                </Typography>
                <Typography variant="body2" color="text.secondary">
                  Branch: {result.left.branch}
                </Typography>
                <Typography variant="body2" color="text.secondary">
                  Owner: {result.left.owner}
                </Typography>
              </CardContent>
            </Card>
            <Card variant="outlined" sx={{ flex: 1, minWidth: 250 }}>
              <CardContent>
                <Typography variant="overline" color="text.secondary">
                  Right Instance
                </Typography>
                <Typography variant="h6">{result.right.name}</Typography>
                <Typography variant="body2" color="text.secondary">
                  Definition: {result.right.definition_name}
                </Typography>
                <Typography variant="body2" color="text.secondary">
                  Branch: {result.right.branch}
                </Typography>
                <Typography variant="body2" color="text.secondary">
                  Owner: {result.right.owner}
                </Typography>
              </CardContent>
            </Card>
          </Box>

          {/* Chart diff summary */}
          {result.charts.length === 0 ? (
            <Alert severity="info">No charts found for comparison.</Alert>
          ) : (
            <Box>
              <Tabs
                value={activeTab}
                onChange={(_e, newValue: number) => setActiveTab(newValue)}
                variant="scrollable"
                scrollButtons="auto"
                sx={{ mb: 2, borderBottom: 1, borderColor: 'divider' }}
              >
                {result.charts.map((chart, index) => (
                  <Tab
                    key={chart.chart_name}
                    label={getTabLabel(chart)}
                    id={`chart-tab-${index}`}
                    aria-controls={`chart-tabpanel-${index}`}
                    icon={
                      !chart.has_differences ? (
                        <CheckCircleOutlineIcon color="success" fontSize="small" />
                      ) : undefined
                    }
                    iconPosition="end"
                  />
                ))}
              </Tabs>

              {result.charts.map((chart, index) => (
                <Box
                  key={chart.chart_name}
                  role="tabpanel"
                  id={`chart-tabpanel-${index}`}
                  aria-labelledby={`chart-tab-${index}`}
                  hidden={activeTab !== index}
                >
                  {activeTab === index && (
                    <Box>
                      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
                        <Typography variant="subtitle1" fontWeight={600}>
                          {chart.chart_name}
                        </Typography>
                        {!chart.has_differences && (
                          <Chip
                            label="No differences"
                            color="success"
                            size="small"
                            variant="outlined"
                          />
                        )}
                        {!chart.left_values && chart.right_values && (
                          <Chip label="Right only" color="warning" size="small" variant="outlined" />
                        )}
                        {chart.left_values && !chart.right_values && (
                          <Chip label="Left only" color="warning" size="small" variant="outlined" />
                        )}
                      </Box>
                      <Box sx={{ border: 1, borderColor: 'divider', borderRadius: 1, overflow: 'auto' }}>
                        <ReactDiffViewer
                          oldValue={chart.left_values ?? ''}
                          newValue={chart.right_values ?? ''}
                          splitView={true}
                          leftTitle={result.left.name}
                          rightTitle={result.right.name}
                          showDiffOnly={false}
                        />
                      </Box>
                    </Box>
                  )}
                </Box>
              ))}
            </Box>
          )}
        </Box>
      )}
    </Box>
  );
};

export default Compare;
