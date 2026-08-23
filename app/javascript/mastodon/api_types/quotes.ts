import type { ApiStatusJSON } from './notifications';

export type ApiQuoteState = 'accepted' | 'pending' | 'revoked' | 'unauthorized';

export type ApiQuotePolicy =
  | 'public'
  | 'followers'
  | 'following'
  | 'nobody'
  | 'unsupported_policy';

export type ApiUserQuotePolicy = 'automatic' | 'manual' | 'denied' | 'unknown';

export interface ApiQuoteJSON {
  state: ApiQuoteState;
  quoted_status?: ApiStatusJSON | null;
  quoted_status_id?: string | null;
}

export interface ApiQuotePolicyJSON {
  automatic: ApiQuotePolicy[];
  manual: ApiQuotePolicy[];
  current_user: ApiUserQuotePolicy;
}

export const isQuotePolicy = (policy: string): policy is ApiQuotePolicy =>
  ['public', 'followers', 'nobody'].includes(policy);
