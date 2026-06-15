import { Spinner } from '@meesho/merlin-ui-tailwind';
import { useAuth } from './auth/useAuth';
import LoginScreen from './components/LoginScreen';
import AppShell from './components/AppShell';

// App is the auth gate: show a spinner while bootstrapping, the login screen
// when signed out, and the full app shell once authenticated.
const App = () => {
  const { user, loading } = useAuth();

  if (loading) {
    return (
      <div className="flex h-screen items-center justify-center bg-secondary-bg">
        <Spinner />
      </div>
    );
  }

  if (!user) return <LoginScreen />;

  return <AppShell />;
};

export default App;
