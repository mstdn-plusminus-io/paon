import PropTypes from 'prop-types';
import { createContext, useContext } from 'react';

import hoistStatics from 'hoist-non-react-statics';

import type { InitialState } from 'mastodon/initial_state';

export interface IdentityContextType {
  signedIn: boolean;
  accountId: string | undefined;
  disabledAccountId: string | undefined;
  permissions: number;
}

export const identityContextPropShape = PropTypes.shape({
  signedIn: PropTypes.bool.isRequired,
  accountId: PropTypes.string,
  disabledAccountId: PropTypes.string,
  permissions: PropTypes.number.isRequired,
}).isRequired;

export const createIdentityContext = (state: InitialState) => ({
  signedIn: !!state.meta.me,
  accountId: state.meta.me,
  disabledAccountId: state.meta.disabled_account_id,
  permissions: Number(state.role?.permissions ?? 0),
});

export const IdentityContext = createContext<IdentityContextType>({
  signedIn: false,
  permissions: 0,
  accountId: undefined,
  disabledAccountId: undefined,
});

export const useIdentity = () => useContext(IdentityContext);

export interface IdentityProps {
  wrappedComponentRef?: unknown;
}

/* Injects an `identity` props into the wrapped component to be able to use the new context in class components */
export function withIdentity<ComponentProps extends object>(
  Component: React.ComponentType<ComponentProps>,
) {
  type OuterProps = Omit<ComponentProps, 'identity'> & IdentityProps;

  const C: React.FC<OuterProps> = (props) => {
    const { wrappedComponentRef, ...remainingProps } = props;

    return (
      <IdentityContext.Consumer>
        {(context) => {
          const componentProps = {
            ...remainingProps,
            identity: context,
            ...(wrappedComponentRef ? { ref: wrappedComponentRef } : undefined),
          } as unknown as ComponentProps;

          return <Component {...componentProps} />;
        }}
      </IdentityContext.Consumer>
    );
  };

  C.displayName = `withIdentity(${Component.displayName ?? 'Component'})`;

  return hoistStatics(
    Object.assign(C, { WrappedComponent: Component }),
    Component,
  );
}
