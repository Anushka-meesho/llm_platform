import { useEffect, useState } from 'react';
import { Button, Typography, Spinner } from '@meesho/merlin-ui-tailwind';
import type { TUser } from '../types';
import { api, errorMessage } from '../api/client';
import { useAuth } from '../auth/useAuth';
import { useToast } from '../toast/context';

const LoginScreen = () => {
  const { login } = useAuth();
  const toast = useToast();
  const [users, setUsers] = useState<TUser[]>([]);
  const [loading, setLoading] = useState(true);
  const [signingIn, setSigningIn] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api
      .demoUsers()
      .then((d) => setUsers(d.users))
      .catch((e) => setError(errorMessage(e)))
      .finally(() => setLoading(false));
  }, []);

  const handleLogin = async (userId: string) => {
    setSigningIn(userId);
    setError(null);
    try {
      await login(userId);
    } catch (e) {
      const msg = errorMessage(e);
      setError(msg);
      toast.error(msg);
      setSigningIn(null);
    }
  };

  return (
    <div className="flex h-screen items-center justify-center bg-secondary-bg">
      <div className="w-[400px] max-w-[92vw] bg-primary-bg border border-solid border-primary-border rounded-2xl shadow-sm p-8 flex flex-col gap-6">
        <div className="flex flex-col gap-1 text-center">
          <Typography variant="heading" size="6" className="text-primary-text">
            ⚡ LLM Platform
          </Typography>
          <Typography variant="body" size="3" className="text-tertiary-text">
            Sign in with SSO to continue
          </Typography>
        </div>

        {loading && (
          <div className="flex justify-center py-6">
            <Spinner />
          </div>
        )}

        {error && (
          <div className="bg-error-bg border border-solid border-error-border rounded-md px-3 py-2">
            <Typography variant="body" size="2" className="text-error-text">
              {error}
            </Typography>
          </div>
        )}

        {!loading && users.length > 0 && (
          <div className="flex flex-col gap-3">
            <Typography
              variant="body"
              size="1"
              className="text-tertiary-text uppercase tracking-wider text-center"
            >
              Demo accounts
            </Typography>
            {users.map((u) => (
              <Button
                key={u.id}
                variant="outline"
                onClick={() => handleLogin(u.id)}
                disabled={signingIn !== null}
                className="w-full justify-start"
              >
                <span className="flex items-center gap-3">
                  <span className="flex h-8 w-8 items-center justify-center rounded-full bg-tertiary-bg text-sm font-semi-bold text-primary-text">
                    {u.name.charAt(0)}
                  </span>
                  <span className="flex flex-col items-start">
                    <span className="text-primary-text font-medium">
                      {signingIn === u.id ? 'Signing in…' : u.name}
                    </span>
                    <span className="text-[11px] text-tertiary-text">
                      {u.email}
                      {u.role && <span className="ml-1 uppercase tracking-wide">· {u.role}</span>}
                    </span>
                  </span>
                </span>
              </Button>
            ))}
          </div>
        )}

        <Typography variant="body" size="1" className="text-tertiary-text text-center">
          Demo SSO — real single sign-on plugs in behind the same interface.
        </Typography>
      </div>
    </div>
  );
};

export default LoginScreen;
