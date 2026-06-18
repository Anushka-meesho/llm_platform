import { useState, useEffect, useCallback, type ReactNode } from 'react';
import { api, ApiError, errorMessage } from '../api/client';
import { AuthContext } from './context';
import type { TUser } from '../types';

export const AuthProvider = ({ children }: { children: ReactNode }) => {
  const [user, setUser] = useState<TUser | null>(null);
  const [loading, setLoading] = useState(true);

  // Bootstrap: ask the backend who we are. A 401 simply means "not logged in".
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const { user } = await api.me();
        if (!cancelled) setUser(user);
      } catch (err) {
        if (!cancelled && !(err instanceof ApiError && err.status === 401)) {
          // A 401 here is expected when signed out — stay quiet. Anything else
          // is genuinely unexpected: stay logged out, but log for diagnosis.
          console.error('auth bootstrap failed:', errorMessage(err));
        }
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const login = useCallback(async (userId: string) => {
    const { user } = await api.login(userId);
    setUser(user);
  }, []);

  const logout = useCallback(async () => {
    await api.logout().catch((e) => console.error('logout failed:', errorMessage(e)));
    setUser(null);
  }, []);

  return (
    <AuthContext.Provider value={{ user, loading, login, logout }}>
      {children}
    </AuthContext.Provider>
  );
};
