import type { AxiosResponse, Method, RawAxiosRequestHeaders } from 'axios';
import axios from 'axios';
import LinkHeader from 'http-link-header';

import { getAccessToken } from './initial_state';
import ready from './ready';

export const getLinks = (response: AxiosResponse) => {
  const value = response.headers.link as string | undefined;

  if (!value) {
    return new LinkHeader();
  }

  return LinkHeader.parse(value);
};

export interface AsyncRefreshHeader {
  id: string;
  retry: number;
}

/**
 * Parse Mastodon's structured `Mastodon-Async-Refresh` response header.
 *
 * A malformed header is deliberately ignored. In particular, do not start a
 * polling loop unless both the opaque refresh id and a finite retry interval
 * are present.
 * @param response Axios response containing the optional refresh header.
 * @returns The validated refresh instructions, or null when unavailable.
 */
export const getAsyncRefreshHeader = (
  response: AxiosResponse,
): AsyncRefreshHeader | null => {
  const value = response.headers['mastodon-async-refresh'] as
    | string
    | undefined;

  if (!value) return null;

  const fields: Partial<AsyncRefreshHeader> = {};

  value.split(/,\s*/).forEach((pair) => {
    const [rawKey, rawValue] = pair.split('=', 2);
    const key = rawKey?.trim();
    const valuePart = rawValue?.trim();

    if (!key || !valuePart) return;

    if (key === 'id') {
      fields.id = valuePart.replace(/^"|"$/g, '');
    } else if (key === 'retry') {
      const retry = Number.parseInt(valuePart, 10);
      if (Number.isFinite(retry) && retry > 0) fields.retry = retry;
    }
  });

  return fields.id && fields.retry
    ? { id: fields.id, retry: fields.retry }
    : null;
};

const csrfHeader: RawAxiosRequestHeaders = {};

const setCSRFHeader = () => {
  const csrfToken = document.querySelector<HTMLMetaElement>(
    'meta[name=csrf-token]',
  );

  if (csrfToken) {
    csrfHeader['X-CSRF-Token'] = csrfToken.content;
  }
};

void ready(setCSRFHeader);

const authorizationTokenFromInitialState = (): RawAxiosRequestHeaders => {
  const accessToken = getAccessToken();

  if (!accessToken) return {};

  return {
    Authorization: `Bearer ${accessToken}`,
  };
};

// eslint-disable-next-line import/no-default-export
export default function api(withAuthorization = true) {
  return axios.create({
    transitional: {
      clarifyTimeoutError: true,
    },
    headers: {
      ...csrfHeader,
      ...(withAuthorization ? authorizationTokenFromInitialState() : {}),
    },

    transformResponse: [
      function (data: unknown) {
        try {
          return JSON.parse(data as string) as unknown;
        } catch {
          return data;
        }
      },
    ],
  });
}

type RequestParamsOrData = Record<string, unknown>;

export async function apiRequest<ApiResponse = unknown>(
  method: Method,
  url: string,
  args: {
    params?: RequestParamsOrData;
    data?: RequestParamsOrData;
    timeout?: number;
  } = {},
) {
  const { data } = await api().request<ApiResponse>({
    method,
    url: '/api/' + url,
    ...args,
  });

  return data;
}

export async function apiRequestGet<ApiResponse = unknown>(
  url: string,
  params?: RequestParamsOrData,
) {
  return apiRequest<ApiResponse>('GET', url, { params });
}

export async function apiRequestPost<ApiResponse = unknown>(
  url: string,
  data?: RequestParamsOrData,
) {
  return apiRequest<ApiResponse>('POST', url, { data });
}

export async function apiRequestPut<ApiResponse = unknown>(
  url: string,
  data?: RequestParamsOrData,
) {
  return apiRequest<ApiResponse>('PUT', url, { data });
}

export async function apiRequestDelete<ApiResponse = unknown>(
  url: string,
  params?: RequestParamsOrData,
) {
  return apiRequest<ApiResponse>('DELETE', url, { params });
}
