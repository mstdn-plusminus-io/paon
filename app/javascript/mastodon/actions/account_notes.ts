import { createAppAsyncThunk } from 'mastodon/store/typed_functions';

import api from '../api';

export const submitAccountNote = createAppAsyncThunk(
  'account_note/submit',
  async (args: { id: string; value: string }) => {
    // TODO: replace `unknown` with `ApiRelationshipJSON` when it is merged
    const response = await api().post<unknown>(
      `/api/v1/accounts/${args.id}/note`,
      {
        comment: args.value,
      },
    );

    return { relationship: response.data };
  },
);
