import { useContext } from 'react';
import { AuthContext, type TAuthContext } from './context';

export const useAuth = (): TAuthContext => {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth must be used within AuthProvider');
  return ctx;
};
