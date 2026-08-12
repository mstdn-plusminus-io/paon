import { isAction } from '@reduxjs/toolkit';
import type { Middleware, UnknownAction } from '@reduxjs/toolkit';
import { showLoading, hideLoading } from 'react-redux-loading-bar';

import type { RootState } from '..';

interface Config {
  promiseTypeSuffixes?: string[];
}

const defaultTypeSuffixes: Config['promiseTypeSuffixes'] = [
  'PENDING',
  'FULFILLED',
  'REJECTED',
];

interface LoadableAction extends UnknownAction {
  skipLoading?: boolean;
}

const isLoadableAction = (action: unknown): action is LoadableAction =>
  isAction(action) &&
  (!('skipLoading' in action) || typeof action.skipLoading === 'boolean');

export const loadingBarMiddleware = (
  config: Config = {},
): Middleware<{ skipLoading?: boolean }, RootState> => {
  const [pending = 'PENDING', fulfilled = 'FULFILLED', rejected = 'REJECTED'] =
    config.promiseTypeSuffixes ?? defaultTypeSuffixes;

  return ({ dispatch }) =>
    (next) =>
    (action) => {
      if (isLoadableAction(action) && !action.skipLoading) {
        if (action.type.endsWith(pending)) {
          dispatch(showLoading());
        } else if (
          action.type.endsWith(fulfilled) ||
          action.type.endsWith(rejected)
        ) {
          dispatch(hideLoading());
        }
      }

      return next(action);
    };
};
