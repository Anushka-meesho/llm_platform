// Static service-token auth for the client portal.
//
// There is no login screen: every API call carries a long-lived service JWT in
// the Authorization header, the same way a machine caller (e.g. CIS) talks to
// the platform. The baked-in token below is a real token for the demo
// principal `svc:demo-client`, signed with the dev JWT_SECRET and minted via:
//
//   go run ./cmd/issue-token -sub svc:demo-client -email demo-client@svc.local \
//     -name "Demo Client" -ttl 87600h
//
// To act as a different principal (or once JWT_SECRET changes), mint a new
// token and set VITE_API_TOKEN in llm_platform_client/.env.local — it takes
// precedence over the sample token.
const SAMPLE_TOKEN =
  'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJlbWFpbCI6ImRlbW8tY2xpZW50QHN2Yy5sb2NhbCIsIm5hbWUiOiJEZW1vIENsaWVudCIsImlzcyI6ImxsbS1wbGF0Zm9ybS1kZW1vIiwic3ViIjoic3ZjOmRlbW8tY2xpZW50IiwiZXhwIjoyMDk2NjA3Mjk2LCJpYXQiOjE3ODEyNDcyOTZ9.haxd4B9OIQ5juApGJ77l7DYmb8azyqmf08ENYJimH6k';

export const API_TOKEN: string = import.meta.env.VITE_API_TOKEN ?? SAMPLE_TOKEN;

export type TPrincipal = {
  sub: string;
  email: string;
  name?: string;
  exp?: number;
};

// Decode the JWT payload for display only — the backend does the real
// signature validation on every request.
export function decodePrincipal(token: string): TPrincipal | null {
  try {
    const payload = token.split('.')[1];
    const json = atob(payload.replace(/-/g, '+').replace(/_/g, '/'));
    const claims = JSON.parse(json) as TPrincipal;
    return claims.sub ? claims : null;
  } catch {
    return null;
  }
}

export const principal = decodePrincipal(API_TOKEN);
