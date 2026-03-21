import { Routes, Route } from 'react-router-dom';
import ProtectedRoute from './components/ProtectedRoute';
import Login from './pages/Login';
import Dashboard from './pages/StackInstances/Dashboard';
import Gallery from './pages/Templates/Gallery';
import Builder from './pages/Templates/Builder';
import Preview from './pages/Templates/Preview';
import Instantiate from './pages/Templates/Instantiate';
import DefinitionList from './pages/StackDefinitions/List';
import DefinitionForm from './pages/StackDefinitions/Form';
import InstanceForm from './pages/StackInstances/Form';
import InstanceDetail from './pages/StackInstances/Detail';
import AuditLog from './pages/AuditLog';
import AdminUsers from './pages/Admin/Users';
import OrphanedNamespaces from './pages/Admin/OrphanedNamespaces';
import Clusters from './pages/Admin/Clusters';
import ClusterHealth from './pages/ClusterHealth';
import Analytics from './pages/Analytics';
import SharedValues from './pages/SharedValues';
import Profile from './pages/Profile';

const AppRoutes = () => {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/" element={<ProtectedRoute><Dashboard /></ProtectedRoute>} />
      <Route path="/templates" element={<ProtectedRoute><Gallery /></ProtectedRoute>} />
      <Route path="/templates/new" element={<ProtectedRoute><Builder /></ProtectedRoute>} />
      <Route path="/templates/:id" element={<ProtectedRoute><Preview /></ProtectedRoute>} />
      <Route path="/templates/:id/edit" element={<ProtectedRoute><Builder /></ProtectedRoute>} />
      <Route path="/templates/:id/use" element={<ProtectedRoute><Instantiate /></ProtectedRoute>} />
      <Route path="/stack-definitions" element={<ProtectedRoute><DefinitionList /></ProtectedRoute>} />
      <Route path="/stack-definitions/new" element={<ProtectedRoute><DefinitionForm /></ProtectedRoute>} />
      <Route path="/stack-definitions/:id/edit" element={<ProtectedRoute><DefinitionForm /></ProtectedRoute>} />
      <Route path="/stack-instances/new" element={<ProtectedRoute><InstanceForm /></ProtectedRoute>} />
      <Route path="/stack-instances/:id" element={<ProtectedRoute><InstanceDetail /></ProtectedRoute>} />
      <Route path="/audit-log" element={<ProtectedRoute><AuditLog /></ProtectedRoute>} />
      <Route path="/admin/users" element={<ProtectedRoute requiredRole="admin"><AdminUsers /></ProtectedRoute>} />
      <Route path="/admin/orphaned-namespaces" element={<ProtectedRoute requiredRole="admin"><OrphanedNamespaces /></ProtectedRoute>} />
      <Route path="/admin/clusters" element={<ProtectedRoute requiredRole="admin"><Clusters /></ProtectedRoute>} />
      <Route path="/admin/cluster-health" element={<ProtectedRoute requiredRole="admin"><ClusterHealth /></ProtectedRoute>} />
      <Route path="/admin/analytics" element={<ProtectedRoute requiredRole="admin"><Analytics /></ProtectedRoute>} />
      <Route path="/admin/shared-values" element={<ProtectedRoute requiredRole="admin"><SharedValues /></ProtectedRoute>} />
      <Route path="/profile" element={<ProtectedRoute><Profile /></ProtectedRoute>} />
    </Routes>
  );
};

export default AppRoutes;
