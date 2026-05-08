import { useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Box,
  Typography,
  Button,
  Paper,
  Stepper,
  Step,
  StepLabel,
  Alert,
} from '@mui/material';
import CloudOutlinedIcon from '@mui/icons-material/CloudOutlined';
import ViewModuleOutlinedIcon from '@mui/icons-material/ViewModuleOutlined';
import RocketLaunchOutlinedIcon from '@mui/icons-material/RocketLaunchOutlined';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';

interface SetupWizardProps {
  hasClusters: boolean;
  hasTemplates: boolean;
  hasInstances: boolean;
  isAdmin: boolean;
  onDismiss: () => void;
}

const steps = [
  {
    label: 'Register a Cluster',
    icon: <CloudOutlinedIcon sx={{ fontSize: 48 }} />,
    description: 'Register a Kubernetes cluster where your stacks will run. Clusters provide the compute environment for deployments.',
    adminRoute: '/admin/clusters',
  },
  {
    label: 'Create a Template',
    icon: <ViewModuleOutlinedIcon sx={{ fontSize: 48 }} />,
    description: 'Templates define reusable stack configurations with Helm charts. Create one to standardize deployments across your team.',
    route: '/templates/new',
  },
  {
    label: 'Deploy an Instance',
    icon: <RocketLaunchOutlinedIcon sx={{ fontSize: 48 }} />,
    description: 'Deploy a running instance of your stack to a cluster. Instances are created from templates or stack definitions.',
    route: '/stack-instances/new',
  },
];

const SetupWizard = ({ hasClusters, hasTemplates, hasInstances, isAdmin, onDismiss }: SetupWizardProps) => {
  const navigate = useNavigate();

  const activeStep = useMemo(() => {
    if (!hasClusters) return 0;
    if (!hasTemplates) return 1;
    if (!hasInstances) return 2;
    return 3;
  }, [hasClusters, hasTemplates, hasInstances]);

  const completedSteps = useMemo(() => {
    const completed = new Set<number>();
    if (hasClusters) completed.add(0);
    if (hasTemplates) completed.add(1);
    if (hasInstances) completed.add(2);
    return completed;
  }, [hasClusters, hasTemplates, hasInstances]);

  return (
    <Box sx={{ maxWidth: 720, mx: 'auto', mt: 4 }}>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 4 }}>
        <Box>
          <Typography variant="h4" component="h1" gutterBottom>
            Welcome to Stack Manager
          </Typography>
          <Typography variant="body1" color="text.secondary">
            Get started by completing these steps to deploy your first stack.
          </Typography>
        </Box>
        <Button size="small" onClick={onDismiss} sx={{ whiteSpace: 'nowrap' }}>
          Skip setup
        </Button>
      </Box>

      <Stepper activeStep={activeStep} alternativeLabel sx={{ mb: 4 }}>
        {steps.map((step, index) => (
          <Step key={step.label} completed={completedSteps.has(index)}>
            <StepLabel>{step.label}</StepLabel>
          </Step>
        ))}
      </Stepper>

      {activeStep < steps.length && (
        <Paper sx={{ p: 4, textAlign: 'center' }}>
          <Box sx={{ color: completedSteps.has(activeStep) ? 'success.main' : 'text.secondary', mb: 2 }}>
            {completedSteps.has(activeStep) ? <CheckCircleIcon sx={{ fontSize: 48 }} /> : steps[activeStep].icon}
          </Box>

          <Typography variant="h6" gutterBottom>
            {steps[activeStep].label}
          </Typography>

          <Typography variant="body2" color="text.secondary" sx={{ mb: 3, maxWidth: 480, mx: 'auto' }}>
            {steps[activeStep].description}
          </Typography>

          {activeStep === 0 && !isAdmin ? (
            <Alert severity="info" sx={{ maxWidth: 480, mx: 'auto' }}>
              Only administrators can register clusters. Ask your admin to add one, then return here.
            </Alert>
          ) : (
            <Button
              variant="contained"
              size="large"
              onClick={() => navigate(activeStep === 0 ? steps[0].adminRoute! : steps[activeStep].route!)}
            >
              {steps[activeStep].label}
            </Button>
          )}
        </Paper>
      )}

      <Typography variant="body2" color="text.secondary" sx={{ mt: 3, textAlign: 'center' }}>
        Step {Math.min(activeStep + 1, steps.length)} of {steps.length}
      </Typography>
    </Box>
  );
};

export default SetupWizard;
