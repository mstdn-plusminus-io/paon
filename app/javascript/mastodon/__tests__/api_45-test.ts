import type { AxiosResponse } from 'axios';

import { getAsyncRefreshHeader } from '../api';

const responseWithHeader = (value?: string) =>
  ({
    headers: value ? { 'mastodon-async-refresh': value } : {},
  }) as AxiosResponse;

describe('Mastodon 4.5 async refresh header', () => {
  it('parses a quoted id and positive retry interval', () => {
    expect(
      getAsyncRefreshHeader(responseWithHeader('id="refresh-42", retry=5')),
    ).toEqual({ id: 'refresh-42', retry: 5 });
  });

  it.each([
    undefined,
    'id="refresh-42"',
    'retry=5',
    'id="refresh-42", retry=0',
    'id="refresh-42", retry=not-a-number',
  ])('ignores incomplete or malformed input: %s', (value) => {
    expect(getAsyncRefreshHeader(responseWithHeader(value))).toBeNull();
  });
});
