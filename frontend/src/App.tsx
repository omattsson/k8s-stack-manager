import { BrowserRouter } from 'react-router-dom';
import AppRoutes from './routes';
import Layout from './components/Layout';
import ErrorBoundary from './components/ErrorBoundary';
import { AuthProvider } from './context/AuthContext';
import { NotificationProvider } from './context/NotificationContext';
import { ThemeModeProvider } from './context/ThemeContext';

function App() {
  return (
    <ThemeModeProvider>
      <BrowserRouter>
        <NotificationProvider>
        <AuthProvider>
          <Layout>
            <ErrorBoundary>
              <AppRoutes />
            </ErrorBoundary>
          </Layout>
        </AuthProvider>
        </NotificationProvider>
      </BrowserRouter>
    </ThemeModeProvider>
  );
}

export default App;
