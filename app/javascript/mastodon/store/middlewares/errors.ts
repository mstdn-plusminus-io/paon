import { isAction } from '@reduxjs/toolkit';
import type { Middleware, UnknownAction } from '@reduxjs/toolkit';

import type { RootState } from '..';
import { showAlertForError } from '../../actions/alerts';

const defaultFailSuffix = 'FAIL';

interface ErrorAction extends UnknownAction {
  error?: unknown;
  skipAlert?: boolean;
  skipNotFound?: boolean;
}

const isErrorAction = (action: unknown): action is ErrorAction =>
  isAction(action) &&
  (!('skipAlert' in action) || typeof action.skipAlert === 'boolean') &&
  (!('skipNotFound' in action) || typeof action.skipNotFound === 'boolean');

export const errorsMiddleware: Middleware<object, RootState> =
  ({ dispatch }) =>
  (next) =>
  (action) => {
    if (isErrorAction(action) && !action.skipAlert) {
      const isFail = new RegExp(`${defaultFailSuffix}$`, 'g');

      if (action.type.match(isFail)) {
        dispatch(showAlertForError(action.error, action.skipNotFound));
      }
    }

    return next(action);
  };
